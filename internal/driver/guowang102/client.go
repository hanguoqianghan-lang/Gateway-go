// internal/driver/guowang102/client.go - 国网102规约 TCP 客户端
package guowang102

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// 客户端配置
// ─────────────────────────────────────────────────────────────────────────────

type ClientConfig struct {
	Host              string        // 子站 IP
	Port              int           // 端口，默认 6960
	LinkAddress       uint16        // 链路地址，默认 0xFFFF
	CommonAddress     uint16        // 公共地址，默认 0xFFFF
	ConnectTimeout    time.Duration // 连接超时
	ReadTimeout       time.Duration // 读超时
	WriteTimeout      time.Duration // 写超时
	KeepAliveInterval time.Duration // TCP Keepalive 间隔
	ReconnectInterval time.Duration // 重连基础间隔
	MaxReconnectInterval time.Duration // 最大重连间隔
}

// DefaultClientConfig 返回默认配置
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Host:                 "127.0.0.1",
		Port:                 6960,
		LinkAddress:          DefaultLinkAddress,
		CommonAddress:        DefaultCommonAddress,
		ConnectTimeout:       10 * time.Second,
		ReadTimeout:          30 * time.Second,
		WriteTimeout:         10 * time.Second,
		KeepAliveInterval:    30 * time.Second,
		ReconnectInterval:    5 * time.Second,
		MaxReconnectInterval: 60 * time.Second,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 客户端统计
// ─────────────────────────────────────────────────────────────────────────────

type ClientStats struct {
	TxFrames      uint64 // 发送帧数
	RxFrames      uint64 // 接收帧数
	TxBytes       uint64 // 发送字节数
	RxBytes       uint64 // 接收字节数
	Errors        uint64 // 错误计数
	Reconnects    uint64 // 重连次数
	LastConnect   int64  // 最后连接时间 (UnixNano)
	LastDisconnect int64 // 最后断开时间
}

// ─────────────────────────────────────────────────────────────────────────────
// TCP 客户端
// ─────────────────────────────────────────────────────────────────────────────

type Client struct {
	cfg    ClientConfig
	logger *zap.Logger

	// 连接
	conn     net.Conn
	connMu   sync.RWMutex
	connected atomic.Bool

	// 发送序列号 (FCB 状态)
	fcb     bool
	fcbMu   sync.Mutex

	// 接收缓冲
	readBuf []byte

	// 统计
	stats ClientStats

	// 生命周期
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewClient 创建 TCP 客户端
func NewClient(cfg ClientConfig, logger *zap.Logger) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		cfg:     cfg,
		logger:  logger.Named("client"),
		readBuf: make([]byte, 4096),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Connect 连接到子站
func (c *Client) Connect() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.connected.Load() {
		return nil // 已连接
	}

	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)
	c.logger.Info("connecting to substation", zap.String("addr", addr))

	// 拨号连接
	dialer := &net.Dialer{
		Timeout:   c.cfg.ConnectTimeout,
		KeepAlive: c.cfg.KeepAliveInterval,
	}
	conn, err := dialer.DialContext(c.ctx, "tcp", addr)
	if err != nil {
		atomic.AddUint64(&c.stats.Errors, 1)
		return fmt.Errorf("dial failed: %w", err)
	}

	// 设置 TCP 选项
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true) // 禁用 Nagle 算法
	}

	c.conn = conn
	c.connected.Store(true)
	atomic.StoreInt64(&c.stats.LastConnect, time.Now().UnixNano())
	atomic.AddUint64(&c.stats.Reconnects, 1)

	c.logger.Info("connected to substation", zap.String("addr", addr))
	return nil
}

// Close 关闭连接
func (c *Client) Close() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if !c.connected.Load() {
		return nil
	}

	c.cancel()
	c.wg.Wait()

	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		c.connected.Store(false)
		atomic.StoreInt64(&c.stats.LastDisconnect, time.Now().UnixNano())
		c.logger.Info("connection closed")
		return err
	}
	return nil
}

// IsConnected 检查连接状态
func (c *Client) IsConnected() bool {
	return c.connected.Load()
}

// GetStats 获取统计信息
func (c *Client) GetStats() ClientStats {
	return ClientStats{
		TxFrames:       atomic.LoadUint64(&c.stats.TxFrames),
		RxFrames:       atomic.LoadUint64(&c.stats.RxFrames),
		TxBytes:        atomic.LoadUint64(&c.stats.TxBytes),
		RxBytes:        atomic.LoadUint64(&c.stats.RxBytes),
		Errors:         atomic.LoadUint64(&c.stats.Errors),
		Reconnects:     atomic.LoadUint64(&c.stats.Reconnects),
		LastConnect:    atomic.LoadInt64(&c.stats.LastConnect),
		LastDisconnect: atomic.LoadInt64(&c.stats.LastDisconnect),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 帧发送
// ─────────────────────────────────────────────────────────────────────────────

// SendFrame 发送帧 (线程安全)
func (c *Client) SendFrame(frame []byte) error {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil || !c.connected.Load() {
		return errors.New("not connected")
	}

	// 设置写超时
	if c.cfg.WriteTimeout > 0 {
		conn.SetWriteDeadline(time.Now().Add(c.cfg.WriteTimeout))
	}

	n, err := conn.Write(frame)
	if err != nil {
		atomic.AddUint64(&c.stats.Errors, 1)
		c.logger.Error("write frame failed", zap.Error(err))
		// 连接可能已断开，标记为断开
		c.markDisconnected()
		return err
	}

	atomic.AddUint64(&c.stats.TxFrames, 1)
	atomic.AddUint64(&c.stats.TxBytes, uint64(n))
	c.logger.Debug("frame sent", zap.Int("len", n), zap.String("hex", fmt.Sprintf("%X", frame[:min(n, 64)])))
	return nil
}

// SendFixedFrame 发送固定帧
func (c *Client) SendFixedFrame(control byte) error {
	frame := BuildFixedFrame(control, c.cfg.LinkAddress)
	return c.SendFrame(frame)
}

// SendVariableFrame 发送可变帧
func (c *Client) SendVariableFrame(control byte, asdu []byte) error {
	frame := BuildVariableFrame(control, c.cfg.LinkAddress, asdu)
	return c.SendFrame(frame)
}

// SendSingleACK 发送单字节确认
func (c *Client) SendSingleACK() error {
	return c.SendFrame(BuildSingleACK())
}

// ─────────────────────────────────────────────────────────────────────────────
// FCB 管理
// ─────────────────────────────────────────────────────────────────────────────

// GetFCB 获取当前 FCB 状态
func (c *Client) GetFCB() bool {
	c.fcbMu.Lock()
	defer c.fcbMu.Unlock()
	return c.fcb
}

// ToggleFCB 翻转 FCB (发送新 I 帧时调用)
func (c *Client) ToggleFCB() bool {
	c.fcbMu.Lock()
	defer c.fcbMu.Unlock()
	c.fcb = !c.fcb
	return c.fcb
}

// ResetFCB 复位 FCB (链路复位后)
func (c *Client) ResetFCB() {
	c.fcbMu.Lock()
	defer c.fcbMu.Unlock()
	c.fcb = false
}

// ─────────────────────────────────────────────────────────────────────────────
// 帧接收
// ─────────────────────────────────────────────────────────────────────────────

// ReceiveFrame 接收一帧 (阻塞直到收到完整帧或出错/超时)
// 返回: 帧数据, 是否为单字节确认, 错误
func (c *Client) ReceiveFrame() ([]byte, bool, error) {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil || !c.connected.Load() {
		return nil, false, errors.New("not connected")
	}

	// 设置读超时
	if c.cfg.ReadTimeout > 0 {
		conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
	}

	// 读取起始字节
	startByte := make([]byte, 1)
	n, err := conn.Read(startByte)
	if err != nil {
		atomic.AddUint64(&c.stats.Errors, 1)
		return nil, false, err
	}
	if n != 1 {
		return nil, false, errors.New("incomplete start byte read")
	}

	atomic.AddUint64(&c.stats.RxBytes, 1)

	switch startByte[0] {
	case StartByteFixed:
		return c.receiveFixedFrame(conn, startByte[0])
	case StartByteVariable:
		return c.receiveVariableFrame(conn, startByte[0])
	case SingleByteACK:
		atomic.AddUint64(&c.stats.RxFrames, 1)
		return []byte{SingleByteACK}, true, nil
	default:
		atomic.AddUint64(&c.stats.Errors, 1)
		return nil, false, fmt.Errorf("invalid start byte: 0x%02X", startByte[0])
	}
}

// receiveFixedFrame 接收固定长度帧 (已读取起始字节 0x10)
func (c *Client) receiveFixedFrame(conn net.Conn, startByte byte) ([]byte, bool, error) {
	// 固定帧剩余 5 字节: C(1) + A(2) + CS(1) + 16H(1)
	remaining := make([]byte, 5)
	n, err := ioReadFull(conn, remaining)
	if err != nil {
		atomic.AddUint64(&c.stats.Errors, 1)
		return nil, false, err
	}
	if n != 5 {
		return nil, false, errors.New("incomplete fixed frame")
	}

	atomic.AddUint64(&c.stats.RxBytes, uint64(n))

	// 组装完整帧
	frame := make([]byte, 6)
	frame[0] = startByte
	copy(frame[1:], remaining)

	atomic.AddUint64(&c.stats.RxFrames, 1)
	return frame, false, nil
}

// receiveVariableFrame 接收可变长度帧 (已读取起始字节 0x68)
func (c *Client) receiveVariableFrame(conn net.Conn, startByte byte) ([]byte, bool, error) {
	// 读取 L, L, 68H (3字节)
	header := make([]byte, 3)
	n, err := ioReadFull(conn, header)
	if err != nil {
		atomic.AddUint64(&c.stats.Errors, 1)
		return nil, false, err
	}
	if n != 3 {
		return nil, false, errors.New("incomplete variable frame header")
	}

	// 验证 L 重复
	if header[0] != header[1] {
		atomic.AddUint64(&c.stats.Errors, 1)
		return nil, false, fmt.Errorf("L mismatch: 0x%02X != 0x%02X", header[0], header[1])
	}
	if header[2] != StartByteVariable {
		atomic.AddUint64(&c.stats.Errors, 1)
		return nil, false, fmt.Errorf("invalid second start byte: 0x%02X", header[2])
	}

	l := int(header[0]) // L = ASDU长度 + 3 (C+A+A)
	if l < 3 || l > 255 {
		atomic.AddUint64(&c.stats.Errors, 1)
		return nil, false, fmt.Errorf("invalid L: %d", l)
	}

	// 读取剩余: C(1) + A(2) + ASDU(L-3) + CS(1) + 16H(1) = L + 2 字节
	remainingLen := l + 2
	remaining := make([]byte, remainingLen)
	n, err = ioReadFull(conn, remaining)
	if err != nil {
		atomic.AddUint64(&c.stats.Errors, 1)
		return nil, false, err
	}
	if n != remainingLen {
		return nil, false, errors.New("incomplete variable frame body")
	}

	atomic.AddUint64(&c.stats.RxBytes, uint64(n+3))

	// 组装完整帧
	totalLen := 1 + 3 + remainingLen // 起始字节 + 头部3字节 + 剩余
	frame := make([]byte, totalLen)
	frame[0] = startByte
	frame[1] = header[0]
	frame[2] = header[1]
	frame[3] = header[2]
	copy(frame[4:], remaining)

	atomic.AddUint64(&c.stats.RxFrames, 1)
	return frame, false, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 内部辅助
// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) markDisconnected() {
	if c.connected.CompareAndSwap(true, false) {
		atomic.StoreInt64(&c.stats.LastDisconnect, time.Now().UnixNano())
		c.logger.Warn("connection marked as disconnected")
	}
}

// ioReadFull 类似 io.ReadFull 但带上下文取消检查
func ioReadFull(conn net.Conn, buf []byte) (int, error) {
	return io.ReadFull(conn, buf)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}