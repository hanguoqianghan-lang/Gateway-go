// internal/driver/gb26875/connection.go - GB/T 26875.3 传输装置连接封装
package gb26875

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Connection 表示一个传输装置（TCP 客户端）连接
type Connection struct {
	id      string // 连接 ID（以源地址为 key，未识别时暂用 remote addr）
	raw     net.Conn
	remote  string

	// 同步写（同一连接多协程不能并发写）
	writeMu sync.Mutex

	// 已识别的源地址（首条上行帧的 SrcAddr）
	srcAddr    [6]byte
	srcAddrSet bool
	addrMu     sync.RWMutex

	// 父驱动指针
	driver *Driver

	// 接收缓冲（用于帧切分）
	recvBuf []byte
	bufMu   sync.Mutex

	// 关闭标志
	closed   int32
	closeCh  chan struct{}
}

// newConnection 创建 Connection
func newConnection(raw net.Conn, drv *Driver) *Connection {
	return &Connection{
		id:      raw.RemoteAddr().String(),
		raw:     raw,
		remote:  raw.RemoteAddr().String(),
		driver:  drv,
		recvBuf: make([]byte, 0, 4096),
		closeCh: make(chan struct{}),
	}
}

// key 连接 key（用于 map 索引）：识别出 srcAddr 后用地址字符串，否则用 remote
func (c *Connection) key() string {
	c.addrMu.RLock()
	defer c.addrMu.RUnlock()
	if c.srcAddrSet {
		return StringAddr(c.srcAddr)
	}
	return c.id
}

// Close 关闭底层连接
func (c *Connection) Close() {
	if !atomic.CompareAndSwapInt32(&c.closed, 0, 1) {
		return
	}
	close(c.closeCh)
	c.raw.Close()
}

// sendBytes 写入帧字节（带锁）
func (c *Connection) sendBytes(data []byte) error {
	if atomic.LoadInt32(&c.closed) != 0 {
		return errors.New("connection closed")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.raw.SetWriteDeadline(time.Now().Add(c.driver.cfg.WriteTimeout))
	_, err := c.raw.Write(data)
	return err
}

// recvLoop 接收循环（按帧头/帧尾切分报文）
func (c *Connection) recvLoop(ctx context.Context) {
	logger := c.driver.logger.With(
		zap.String("remote", c.remote),
	)

	// 设置初始读超时（用于帧内字节切分）
	if err := c.raw.SetReadDeadline(time.Now().Add(c.driver.cfg.ReadTimeout)); err != nil {
		logger.Warn("设置读超时失败", zap.Error(err))
	}

	tmp := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closeCh:
			return
		default:
		}

		n, err := c.raw.Read(tmp)
		if n > 0 {
			c.appendAndParse(tmp[:n], logger)
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-c.closeCh:
				return
			default:
			}
			// 网络错误，关闭连接
			if !isClosedErr(err) {
				logger.Debug("读取错误，关闭连接", zap.Error(err))
			}
			return
		}
	}
}

// appendAndParse 将新数据追加到缓冲，然后扫描所有完整帧
func (c *Connection) appendAndParse(data []byte, logger *zap.Logger) {
	c.bufMu.Lock()
	c.recvBuf = append(c.recvBuf, data...)
	c.bufMu.Unlock()

	// 重复扫描所有完整帧
	for {
		frame, consumed := c.tryExtractFrame()
		if frame == nil {
			return
		}
		atomic.AddUint64(&c.driver.atomicStats.framesReceived, 1)
		c.handleFrame(frame, logger)
		// 截断已消费的部分
		c.bufMu.Lock()
		if consumed >= len(c.recvBuf) {
			c.recvBuf = c.recvBuf[:0]
		} else {
			// copy 剩余部分到新切片（避免内存泄漏）
			remaining := make([]byte, len(c.recvBuf)-consumed)
			copy(remaining, c.recvBuf[consumed:])
			c.recvBuf = remaining
		}
		c.bufMu.Unlock()
	}
}

// tryExtractFrame 尝试从缓冲区提取一帧
// 返回：frame 完整帧字节；consumed 已消费的字节数
func (c *Connection) tryExtractFrame() (frame []byte, consumed int) {
	c.bufMu.Lock()
	defer c.bufMu.Unlock()

	buf := c.recvBuf
	if len(buf) < 2 {
		return nil, 0
	}

	// 寻找帧头 40 40
	startIdx := indexStartMarker(buf)
	if startIdx < 0 {
		// 没有帧头；丢弃至最近一个可能的位置
		// 保留最后一个字节（可能是 0x40 起始）
		if buf[len(buf)-1] == FrameStart1 {
			consumed = len(buf) - 1
		} else {
			consumed = len(buf)
		}
		return nil, consumed
	}

	// 丢弃帧头之前的数据
	if startIdx > 0 {
		consumed = startIdx
	}
	buf = buf[startIdx:]

	// 最小帧长度：2 + 25 + 0 + 1 + 2 = 30
	if len(buf) < MinFrameLen {
		return nil, consumed
	}

	// 解析 ADU 长度：控制单元第25~26字节 = buf[24..25]（小端）
	// 偏移量：启动符(2)+业务流水号(2)+版本号(2)+时间(6)+源地址(6)+目的地址(6)=24
	aduLen := int(uint16(buf[24]) | uint16(buf[25])<<8)
	if aduLen > MaxADULen {
		// 长度异常，跳过当前帧头继续找下一个
		consumed += 1
		return nil, consumed
	}

	// 期望总长
	expectedLen := FrameStartLen + ControlUnitLen + aduLen + 1 + FrameEndLen
	if len(buf) < expectedLen {
		// 数据未到齐
		return nil, consumed
	}

	// 验证结束符
	endIdx := expectedLen - 1
	if buf[endIdx-1] != FrameEnd1 || buf[endIdx] != FrameEnd2 {
		// 结束符不匹配，跳过当前帧头继续
		consumed += 1
		return nil, consumed
	}

	// 提取完整帧（包含启动符和结束符）
	frame = make([]byte, expectedLen)
	copy(frame, buf[:expectedLen])
	consumed += expectedLen
	return frame, consumed
}

// indexStartMarker 寻找 0x40 0x40 起始位置
func indexStartMarker(buf []byte) int {
	for i := 0; i < len(buf)-1; i++ {
		if buf[i] == FrameStart1 && buf[i+1] == FrameStart2 {
			return i
		}
	}
	// 末尾如果是 0x40，可能是下一个帧的起点
	if buf[len(buf)-1] == FrameStart1 {
		return len(buf) - 1
	}
	return -1
}

// handleFrame 处理完整帧
func (c *Connection) handleFrame(raw []byte, logger *zap.Logger) {
	f, err := ParseFrame(raw)
	if err != nil {
		atomic.AddUint64(&c.driver.atomicStats.framesRejected, 1)
		logger.Debug("帧解析失败",
			zap.String("remote", c.remote),
			zap.Error(err),
			zap.String("raw", fmt.Sprintf("%X", raw)),
		)
		return
	}

	// 记录源地址（首条上行帧后用作连接 key）
	c.addrMu.Lock()
	if !c.srcAddrSet {
		c.srcAddr = f.SrcAddr
		c.srcAddrSet = true
		// 通知驱动重新注册连接（按地址而非 remote）
		newKey := StringAddr(c.srcAddr)
		if newKey != c.id {
			c.driver.reRegisterConnection(c, newKey)
		}
	}
	c.addrMu.Unlock()

	atomic.AddUint64(&c.driver.atomicStats.framesParsed, 1)

	logger.Debug("收到上行帧",
		zap.String("remote", c.remote),
		zap.String("src", StringAddr(f.SrcAddr)),
		zap.String("dst", StringAddr(f.DstAddr)),
		zap.Uint16("seq", f.SequenceNo),
		zap.Uint8("cmd", f.Cmd),
		zap.Uint16("adu_len", f.ADULength),
		zap.String("time", f.Time.String()),
	)

	// 根据命令字处理
	switch {
	case f.IsUpload():
		// 上传数据：解析 + 自动回复确认
		c.handleUploadFrame(f, logger)
	case f.IsCommand():
		// 命令/请求：监控中心一般不接收此方向的命令，仅记录
		logger.Debug("收到下行方向帧（监控中心一般不应收到）",
			zap.String("cmd", cmdName(f.Cmd)),
		)
	case f.IsAck():
		logger.Debug("收到确认帧",
			zap.Uint8("cmd", f.Cmd),
		)
	default:
		logger.Debug("收到未识别命令字",
			zap.Uint8("cmd", f.Cmd),
		)
	}
}

// handleUploadFrame 处理上传数据帧
func (c *Connection) handleUploadFrame(f *Frame, logger *zap.Logger) {
	// 解析 ADU
	if f.ADULength < ADUHeaderLen {
		atomic.AddUint64(&c.driver.atomicStats.framesRejected, 1)
		c.sendDeny(f)
		return
	}
	adu, err := ParseADU(f.ADU)
	if err != nil {
		atomic.AddUint64(&c.driver.atomicStats.framesRejected, 1)
		c.sendDeny(f)
		return
	}

	// 业务处理（解析为 PointData 并发布）
	published := c.driver.processUploadADU(f, adu)
	atomic.AddUint64(&c.driver.atomicStats.pointsPublished, uint64(published))

	// 回复确认帧（无论是否有点匹配都应确认，避免装置重发）
	c.sendAck(f)
}

// sendAck 回复确认帧（命令字 3）
func (c *Connection) sendAck(f *Frame) {
	// BuildAckFrame 参数顺序：(seqNo, ver, userVer, t, src, dst)
	// 把本机作为 src、装置作为 dst
	ack := BuildAckFrame(
		f.SequenceNo,
		f.Version,
		f.UserVer,
		f.Time,
		f.DstAddr, // 本机 = 报文中的目的地址
		f.SrcAddr, // 装置 = 报文中的源地址
	)
	if err := c.sendBytes(ack); err != nil {
		c.driver.logger.Warn("发送确认帧失败",
			zap.String("remote", c.remote),
			zap.Error(err),
		)
		return
	}
	atomic.AddUint64(&c.driver.atomicStats.ackSent, 1)
	c.driver.logger.Debug("ACK 已发送",
		zap.String("remote", c.remote),
		zap.Int("ack_len", len(ack)),
	)
}

// sendDeny 回复否认帧（命令字 6）
func (c *Connection) sendDeny(f *Frame) {
	deny := BuildDenyFrame(
		f.SequenceNo,
		f.Version,
		f.UserVer,
		f.Time,
		f.DstAddr,
		f.SrcAddr,
	)
	if err := c.sendBytes(deny); err != nil {
		c.driver.logger.Warn("发送否认帧失败", zap.Error(err))
		return
	}
	atomic.AddUint64(&c.driver.atomicStats.denySent, 1)
}

// cmdName 返回命令字可读名
func cmdName(cmd uint8) string {
	switch cmd {
	case CmdControl:
		return "Control"
	case CmdSendData:
		return "SendData"
	case CmdConfirm:
		return "Confirm"
	case CmdRequest:
		return "Request"
	case CmdReply:
		return "Reply"
	case CmdDeny:
		return "Deny"
	default:
		return "Unknown"
	}
}
