// internal/driver/iec101/client.go - IEC 60870-5-101 FT1.2 帧处理核心逻辑
package iec101

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/goburrow/serial"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// FT1.2 帧常量定义
// ─────────────────────────────────────────────────────────────────────────────

const (
	// 启动字节
	StartByteFixed    = 0x10 // 固定长度帧启动字节
	StartByteVariable = 0x68 // 可变长度帧启动字节
	EndByte           = 0x16 // 结束字节
	SingleByte        = 0xE5 // 单字节确认（E5）

	// 控制域类型
	C_U   = 0x00 // U 帧（无编号控制帧）
	C_S   = 0x01 // S 帧（确认帧）
	C_I   = 0x03 // I 帧（信息帧）

	// U 帧功能码
	FC_RESET_REMOTE_LINK        = 0x00 // 复位远方链路
	FC_RESET_PROCESS            = 0x01 // 复位进程
	FC_SEND_CONFIRM             = 0x02 // 发送/确认
	FC_REQUEST_RESPOND          = 0x03 // 请求/响应
	FC_START_DATA_TRANSFER      = 0x04 // 启动数据传输
	FC_STOP_DATA_TRANSFER       = 0x05 // 停止数据传输
	FC_TEST_LINK                = 0x09 // 测试链路

	// ASDU 类型标识（监视方向）
	M_SP_NA_1 = 1  // 单点信息
	M_SP_TA_1 = 2  // 单点信息带时标
	M_DP_NA_1 = 3  // 双点信息
	M_DP_TA_1 = 4  // 双点信息带时标
	M_ST_NA_1 = 5  // 步位置信息
	M_ST_TA_1 = 6  // 步位置信息带时标
	M_BO_NA_1 = 7  // 32位比特串
	M_BO_TA_1 = 8  // 32位比特串带时标
	M_ME_NA_1 = 9  // 测量值归一化值
	M_ME_TA_1 = 10 // 测量值归一化值带时标
	M_ME_NB_1 = 11 // 测量值标度化值
	M_ME_TB_1 = 12 // 测量值标度化值带时标
	M_ME_NC_1 = 13 // 测量值短浮点数
	M_ME_TC_1 = 14 // 测量值短浮点数带时标
	M_IT_NA_1 = 15 // 累积量
	M_IT_TA_1 = 16 // 累积量带时标

	// ASDU 类型标识（控制方向）
	C_SC_NA_1 = 45 // 单点命令
	C_DC_NA_1 = 46 // 双点命令
	C_RC_NA_1 = 47 // 调节步命令
	C_SE_NA_1 = 48 // 设定值命令归一化值
	C_SE_NB_1 = 49 // 设定值命令标度化值
	C_SE_NC_1 = 50 // 设定值命令短浮点数

	// 系统命令
	C_IC_NA_1 = 100 // 总召唤命令
	C_CI_NA_1 = 101 // 计数量召唤命令
	C_RD_NA_1 = 102 // 读命令
	C_CS_NA_1 = 103 // 时钟同步命令
	C_TS_NA_1 = 104 // 测试命令
	C_RP_NA_1 = 105 // 复位进程命令
	C_CD_NA_1 = 106 // 延迟获取命令
	C_TS_TA_1 = 107 // 测试命令带时标

	// 传输原因（COT）
	COT_PERIODIC          = 1  // 周期/循环
	COT_BACKGROUND        = 2  // 背景扫描
	COT_SPONTANEOUS       = 3  // 突发（自发）
	COT_INITIALIZED       = 4  // 初始化
	COT_REQUEST           = 5  // 请求
	COT_ACTIVATION        = 6  // 激活
	COT_ACTIVATION_CON    = 7  // 激活确认
	COT_DEACTIVATION      = 8  // 停止激活
	COT_DEACTIVATION_CON  = 9  // 停止激活确认
	COT_ACTIVATION_TERM   = 10 // 激活终止
	COT_RETURN_INFO_REMOTE = 11 // 远程命令引起的返回信息
	COT_RETURN_INFO_LOCAL  = 12 // 本地命令引起的返回信息
	COT_INTERROGATED_BY_STATION = 20 // 站召唤
	COT_INTERROGATED_BY_GROUP1  = 21 // 第1组召唤
	COT_INTERROGATED_BY_GROUP2  = 22 // 第2组召唤
	COT_INTERROGATED_BY_GROUP3  = 23 // 第3组召唤
	COT_INTERROGATED_BY_GROUP4  = 24 // 第4组召唤
)

// ─────────────────────────────────────────────────────────────────────────────
// 帧结构定义
// ─────────────────────────────────────────────────────────────────────────────

// Frame FT1.2 帧结构
type Frame struct {
	// 帧类型
	Type FrameType

	// 控制域
	Control byte

	// 链路地址
	Address byte

	// ASDU（仅 I 帧）
	ASDU []byte
}

// FrameType 帧类型
type FrameType int

const (
	FrameTypeFixed    FrameType = iota // 固定长度帧
	FrameTypeVariable                  // 可变长度帧
	FrameTypeSingle                    // 单字节帧
)

// ─────────────────────────────────────────────────────────────────────────────
// Client IEC101 客户端
// ─────────────────────────────────────────────────────────────────────────────

// Client IEC101 客户端
type Client struct {
	config Config
	logger *zap.Logger

	// 串口
	port     serial.Port
	portMu   sync.Mutex
	isOpen   bool

	// 链路状态
	sendSeq  byte // 发送序列号
	recvSeq  byte // 接收序列号
	seqMu    sync.Mutex

	// 统计
	stats ClientStats
}

// ClientStats 客户端统计信息
type ClientStats struct {
	TxCount uint64 // 发送帧数
	RxCount uint64 // 接收帧数
	ErrCount uint64 // 错误计数
}

// NewClient 创建 IEC101 客户端
func NewClient(config Config, logger *zap.Logger) *Client {
	return &Client{
		config: config,
		logger: logger,
	}
}

// Connect 连接串口
func (c *Client) Connect() error {
	c.portMu.Lock()
	defer c.portMu.Unlock()

	if c.isOpen {
		return nil
	}

	// 配置串口参数
	cfg := &serial.Config{
		Address:  c.config.SerialPort,
		BaudRate: c.config.BaudRate,
		DataBits: c.config.DataBits,
		StopBits: c.config.StopBits,
		Parity:   c.parseParity(c.config.Parity),
		Timeout:  c.config.CharTimeout,
	}

	// 打开串口
	port, err := serial.Open(cfg)
	if err != nil {
		return fmt.Errorf("open serial port failed: %w", err)
	}

	c.port = port
	c.isOpen = true

	c.logger.Info("serial port opened",
		zap.String("port", c.config.SerialPort),
		zap.Int("baud_rate", c.config.BaudRate),
		zap.String("parity", c.config.Parity),
	)

	return nil
}

// parseParity 解析校验位
func (c *Client) parseParity(parity string) string {
	switch parity {
	case "even":
		return "E"
	case "odd":
		return "O"
	default:
		return "N"
	}
}

// Close 关闭串口
func (c *Client) Close() error {
	c.portMu.Lock()
	defer c.portMu.Unlock()

	if !c.isOpen {
		return nil
	}

	if err := c.port.Close(); err != nil {
		return err
	}

	c.isOpen = false
	c.logger.Info("serial port closed")
	return nil
}

// IsConnected 检查连接状态
func (c *Client) IsConnected() bool {
	c.portMu.Lock()
	defer c.portMu.Unlock()
	return c.isOpen
}

// ─────────────────────────────────────────────────────────────────────────────
// 帧发送
// ─────────────────────────────────────────────────────────────────────────────

// SendFrame 发送帧
func (c *Client) SendFrame(frame *Frame) error {
	c.portMu.Lock()
	defer c.portMu.Unlock()

	if !c.isOpen {
		return errors.New("serial port not open")
	}

	// 序列化帧
	data, err := c.serializeFrame(frame)
	if err != nil {
		return err
	}

	// 发送
	_, err = c.port.Write(data)
	if err != nil {
		c.stats.ErrCount++
		return err
	}

	c.stats.TxCount++
	return nil
}

// serializeFrame 序列化帧
func (c *Client) serializeFrame(frame *Frame) ([]byte, error) {
	switch frame.Type {
	case FrameTypeFixed:
		return c.serializeFixedFrame(frame), nil
	case FrameTypeVariable:
		return c.serializeVariableFrame(frame), nil
	case FrameTypeSingle:
		return []byte{SingleByte}, nil
	default:
		return nil, errors.New("unknown frame type")
	}
}

// serializeFixedFrame 序列化固定长度帧
// 格式：10H | C | A | CS | 16H
func (c *Client) serializeFixedFrame(frame *Frame) []byte {
	buf := make([]byte, 5)
	buf[0] = StartByteFixed
	buf[1] = frame.Control
	buf[2] = frame.Address
	buf[3] = c.calcCS(buf[1:3]) // CS = C + A
	buf[4] = EndByte
	return buf
}

// serializeVariableFrame 序列化可变长度帧
// 格式：68H | L | L | 68H | C | A | ASDU | CS | 16H
func (c *Client) serializeVariableFrame(frame *Frame) []byte {
	asduLen := len(frame.ASDU)
	totalLen := 6 + asduLen + 2 // 头部(6) + ASDU + CS(1) + End(1)

	buf := make([]byte, totalLen)
	buf[0] = StartByteVariable
	buf[1] = byte(asduLen + 2) // L = ASDU长度 + C(1) + A(1)
	buf[2] = buf[1]
	buf[3] = StartByteVariable
	buf[4] = frame.Control
	buf[5] = frame.Address
	copy(buf[6:], frame.ASDU)
	buf[6+asduLen] = c.calcCS(buf[4 : 6+asduLen]) // CS = C + A + ASDU
	buf[6+asduLen+1] = EndByte

	return buf
}

// calcCS 计算 CS 校验和
func (c *Client) calcCS(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return sum
}

// ─────────────────────────────────────────────────────────────────────────────
// 帧接收
// ─────────────────────────────────────────────────────────────────────────────

// ReceiveFrame 接收帧（带超时）
func (c *Client) ReceiveFrame(timeout time.Duration) (*Frame, error) {
	c.portMu.Lock()
	defer c.portMu.Unlock()

	if !c.isOpen {
		return nil, errors.New("serial port not open")
	}

	// 读取启动字节
	startByte := make([]byte, 1)
	_, err := c.port.Read(startByte)
	if err != nil {
		return nil, err
	}

	switch startByte[0] {
	case StartByteFixed:
		return c.receiveFixedFrame()
	case StartByteVariable:
		return c.receiveVariableFrame()
	case SingleByte:
		return &Frame{Type: FrameTypeSingle}, nil
	default:
		return nil, fmt.Errorf("invalid start byte: 0x%02X", startByte[0])
	}
}

// receiveFixedFrame 接收固定长度帧
// 格式：10H | C | A | CS | 16H
func (c *Client) receiveFixedFrame() (*Frame, error) {
	buf := make([]byte, 4)
	_, err := io.ReadFull(c.port, buf)
	if err != nil {
		return nil, err
	}

	// 验证结束字节
	if buf[3] != EndByte {
		return nil, errors.New("invalid end byte")
	}

	// 验证 CS
	expectedCS := c.calcCS(buf[0:2])
	if buf[2] != expectedCS {
		return nil, fmt.Errorf("CS mismatch: expected 0x%02X, got 0x%02X", expectedCS, buf[2])
	}

	c.stats.RxCount++

	return &Frame{
		Type:    FrameTypeFixed,
		Control: buf[0],
		Address: buf[1],
	}, nil
}

// receiveVariableFrame 接收可变长度帧
// 格式：68H | L | L | 68H | C | A | ASDU | CS | 16H
func (c *Client) receiveVariableFrame() (*Frame, error) {
	// 读取 L 和第二个启动字节
	header := make([]byte, 3)
	_, err := io.ReadFull(c.port, header)
	if err != nil {
		return nil, err
	}

	// 验证 L 重复
	if header[0] != header[1] {
		return nil, fmt.Errorf("L mismatch: %d != %d", header[0], header[1])
	}

	// 验证第二个启动字节
	if header[2] != StartByteVariable {
		return nil, errors.New("invalid second start byte")
	}

	length := int(header[0])

	// 读取 C, A, ASDU, CS, End
	data := make([]byte, length+2) // +2 for CS and End
	_, err = io.ReadFull(c.port, data)
	if err != nil {
		return nil, err
	}

	// 验证结束字节
	if data[length+1] != EndByte {
		return nil, errors.New("invalid end byte")
	}

	// 验证 CS
	expectedCS := c.calcCS(data[0:length])
	if data[length] != expectedCS {
		return nil, fmt.Errorf("CS mismatch: expected 0x%02X, got 0x%02X", expectedCS, data[length])
	}

	c.stats.RxCount++

	return &Frame{
		Type:    FrameTypeVariable,
		Control: data[0],
		Address: data[1],
		ASDU:    data[2:length],
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 链路控制
// ─────────────────────────────────────────────────────────────────────────────

// SendResetLink 发送链路复位命令
func (c *Client) SendResetLink() error {
	control := c.buildControlField(C_U, FC_RESET_REMOTE_LINK)
	frame := &Frame{
		Type:    FrameTypeFixed,
		Control: control,
		Address: c.config.LinkAddress,
	}
	return c.SendFrame(frame)
}

// SendGeneralInterrogation 发送总召唤命令
func (c *Client) SendGeneralInterrogation() error {
	// 构建 ASDU
	asdu := c.buildASDU(C_IC_NA_1, COT_ACTIVATION, c.config.CommonAddress, []byte{0x14}) // QOI=20 站召唤

	control := c.buildControlField(C_I, 0)
	frame := &Frame{
		Type:    FrameTypeVariable,
		Control: control,
		Address: c.config.LinkAddress,
		ASDU:    asdu,
	}
	return c.SendFrame(frame)
}

// SendReadCommand 发送读命令
func (c *Client) SendReadCommand(ca uint8, ioa uint16) error {
	// 构建 ASDU
	ioaBytes := make([]byte, 2)
	binary.LittleEndian.PutUint16(ioaBytes, ioa)
	asdu := c.buildASDU(C_RD_NA_1, COT_REQUEST, ca, ioaBytes)

	control := c.buildControlField(C_I, 0)
	frame := &Frame{
		Type:    FrameTypeVariable,
		Control: control,
		Address: c.config.LinkAddress,
		ASDU:    asdu,
	}
	return c.SendFrame(frame)
}

// buildControlField 构建控制域
func (c *Client) buildControlField(frameType int, functionCode int) byte {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()

	switch frameType {
	case C_U:
		// U 帧：功能码在低 4 位
		return byte(functionCode & 0x0F)
	case C_S:
		// S 帧：确认接收序列号
		return 0x01 | (c.recvSeq << 1)
	case C_I:
		// I 帧：发送和接收序列号
		control := (c.sendSeq << 1) | (c.recvSeq << 5) | 0x03
		c.sendSeq = (c.sendSeq + 1) & 0x0F
		return control
	default:
		return 0
	}
}

// buildASDU 构建 ASDU
func (c *Client) buildASDU(typeID uint8, cot uint8, ca uint8, infoObj []byte) []byte {
	// ASDU 结构：TypeID(1) | VSQ(1) | COT(2) | CA(1) | InfoObj
	asdu := make([]byte, 5+len(infoObj))
	asdu[0] = typeID
	asdu[1] = 0x01 // VSQ = 1（单个信息对象）
	asdu[2] = cot
	asdu[3] = 0    // 源发地址
	asdu[4] = ca
	copy(asdu[5:], infoObj)
	return asdu
}

// ─────────────────────────────────────────────────────────────────────────────
// 统计信息
// ─────────────────────────────────────────────────────────────────────────────

// GetStats 获取统计信息
func (c *Client) GetStats() ClientStats {
	return c.stats
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// ParseASDU 解析 ASDU
func ParseASDU(data []byte) (*ASDU, error) {
	if len(data) < 5 {
		return nil, errors.New("ASDU too short")
	}

	asdu := &ASDU{
		TypeID: data[0],
		VSQ:    data[1],
		COT:    data[2],
		OA:     data[3],
		CA:     data[4],
	}

	if len(data) > 5 {
		asdu.InfoObj = data[5:]
	}

	return asdu, nil
}

// ASDU 应用服务数据单元
type ASDU struct {
	TypeID  uint8 // 类型标识
	VSQ     uint8 // 可变结构限定词
	COT     uint8 // 传输原因
	OA      uint8 // 源发地址
	CA      uint8 // 公共地址
	InfoObj []byte // 信息对象
}

// GetInfoObjCount 获取信息对象数量
func (a *ASDU) GetInfoObjCount() int {
	return int(a.VSQ & 0x7F)
}

// IsSequence 是否为序列模式
func (a *ASDU) IsSequence() bool {
	return (a.VSQ & 0x80) != 0
}

// String 返回 ASDU 字符串表示
func (a *ASDU) String() string {
	return fmt.Sprintf("ASDU{TypeID=%d, VSQ=%d, COT=%d, CA=%d, InfoObjLen=%d}",
		a.TypeID, a.VSQ, a.COT, a.CA, len(a.InfoObj))
}

// ─────────────────────────────────────────────────────────────────────────────
// 帧缓冲区（用于处理字符间超时）
// ─────────────────────────────────────────────────────────────────────────────

// FrameBuffer 帧缓冲区
type FrameBuffer struct {
	buf       bytes.Buffer
	lastRead  time.Time
	timeout   time.Duration
}

// NewFrameBuffer 创建帧缓冲区
func NewFrameBuffer(timeout time.Duration) *FrameBuffer {
	return &FrameBuffer{
		timeout: timeout,
	}
}

// Write 写入数据
func (fb *FrameBuffer) Write(data []byte) {
	fb.buf.Write(data)
	fb.lastRead = time.Now()
}

// IsTimeout 检查是否超时
func (fb *FrameBuffer) IsTimeout() bool {
	return time.Since(fb.lastRead) > fb.timeout
}

// Reset 重置缓冲区
func (fb *FrameBuffer) Reset() {
	fb.buf.Reset()
}

// Bytes 获取缓冲区数据
func (fb *FrameBuffer) Bytes() []byte {
	return fb.buf.Bytes()
}

// Len 获取缓冲区长度
func (fb *FrameBuffer) Len() int {
	return fb.buf.Len()
}
