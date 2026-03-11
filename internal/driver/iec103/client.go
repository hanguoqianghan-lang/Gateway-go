// internal/driver/iec103/client.go - IEC 60870-5-103 FT1.2 帧调度逻辑
package iec103

import (
	"bytes"
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
	FC_RESET_REMOTE_LINK   = 0x00 // 复位远方链路
	FC_SEND_CONFIRM        = 0x02 // 发送/确认
	FC_REQUEST_RESPOND     = 0x03 // 请求/响应
	FC_START_DATA_TRANSFER = 0x04 // 启动数据传输

	// 传输原因（COT）
	COT_SPONTANEOUS     = 3  // 突发（自发）
	COT_ACTIVATION      = 6  // 激活
	COT_ACTIVATION_CON  = 7  // 激活确认
	COT_ACTIVATION_TERM = 10 // 激活终止
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
// Client IEC103 客户端
// ─────────────────────────────────────────────────────────────────────────────

// Client IEC103 客户端
type Client struct {
	config Config
	logger *zap.Logger

	// 串口
	port   serial.Port
	portMu sync.Mutex
	isOpen bool

	// 链路状态
	sendSeq byte // 发送序列号
	recvSeq byte // 接收序列号
	seqMu   sync.Mutex

	// 统计
	stats ClientStats
}

// ClientStats 客户端统计信息
type ClientStats struct {
	TxCount  uint64 // 发送帧数
	RxCount  uint64 // 接收帧数
	ErrCount uint64 // 错误计数
}

// NewClient 创建 IEC103 客户端
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
func (c *Client) serializeFixedFrame(frame *Frame) []byte {
	buf := make([]byte, 5)
	buf[0] = StartByteFixed
	buf[1] = frame.Control
	buf[2] = frame.Address
	buf[3] = c.calcCS(buf[1:3])
	buf[4] = EndByte
	return buf
}

// serializeVariableFrame 序列化可变长度帧
func (c *Client) serializeVariableFrame(frame *Frame) []byte {
	asduLen := len(frame.ASDU)
	totalLen := 6 + asduLen + 2

	buf := make([]byte, totalLen)
	buf[0] = StartByteVariable
	buf[1] = byte(asduLen + 2)
	buf[2] = buf[1]
	buf[3] = StartByteVariable
	buf[4] = frame.Control
	buf[5] = frame.Address
	copy(buf[6:], frame.ASDU)
	buf[6+asduLen] = c.calcCS(buf[4 : 6+asduLen])
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

// ReceiveFrame 接收帧
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
func (c *Client) receiveFixedFrame() (*Frame, error) {
	buf := make([]byte, 4)
	_, err := io.ReadFull(c.port, buf)
	if err != nil {
		return nil, err
	}

	if buf[3] != EndByte {
		return nil, errors.New("invalid end byte")
	}

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
func (c *Client) receiveVariableFrame() (*Frame, error) {
	header := make([]byte, 3)
	_, err := io.ReadFull(c.port, header)
	if err != nil {
		return nil, err
	}

	if header[0] != header[1] {
		return nil, fmt.Errorf("L mismatch: %d != %d", header[0], header[1])
	}

	if header[2] != StartByteVariable {
		return nil, errors.New("invalid second start byte")
	}

	length := int(header[0])

	data := make([]byte, length+2)
	_, err = io.ReadFull(c.port, data)
	if err != nil {
		return nil, err
	}

	if data[length+1] != EndByte {
		return nil, errors.New("invalid end byte")
	}

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
	// 构建 ASDU（IEC103 格式）
	asdu := c.buildASDU103(FUN_GENERAL_INTERROG, 0, COT_ACTIVATION, c.config.CommonAddress, []byte{0x14})

	control := c.buildControlField(C_I, 0)
	frame := &Frame{
		Type:    FrameTypeVariable,
		Control: control,
		Address: c.config.LinkAddress,
		ASDU:    asdu,
	}
	return c.SendFrame(frame)
}

// SendReadCommand 发送读命令（基于 FUN/INF）
func (c *Client) SendReadCommand(ca uint8, fun uint8, inf uint8) error {
	// 构建 ASDU（IEC103 格式）
	infoObj := []byte{fun, inf}
	asdu := c.buildASDU103(FUN_READ, 0, COT_ACTIVATION, ca, infoObj)

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
		return byte(functionCode & 0x0F)
	case C_S:
		return 0x01 | (c.recvSeq << 1)
	case C_I:
		control := (c.sendSeq << 1) | (c.recvSeq << 5) | 0x03
		c.sendSeq = (c.sendSeq + 1) & 0x0F
		return control
	default:
		return 0
	}
}

// buildASDU103 构建 ASDU（IEC103 格式）
// IEC103 ASDU 结构：TI(1) | VSQ(1) | COT(1) | FUN(1) | INF(1) | CA(1) | Data
func (c *Client) buildASDU103(fun uint8, ti uint8, cot uint8, ca uint8, data []byte) []byte {
	// 如果未指定 TI，根据 FUN 推断
	if ti == 0 {
		ti = c.inferTI(fun)
	}

	asdu := make([]byte, 6+len(data))
	asdu[0] = ti          // 类型标识（TI）
	asdu[1] = 0x01        // VSQ = 1（单个信息对象）
	asdu[2] = cot         // 传输原因（COT）
	asdu[3] = fun         // 功能类型（FUN）
	asdu[4] = 0           // 信息号（INF）- 命令时为 0
	asdu[5] = ca          // 公共地址（CA）
	copy(asdu[6:], data)  // 数据部分

	return asdu
}

// inferTI 根据 FUN 推断 TI
func (c *Client) inferTI(fun uint8) uint8 {
	switch fun {
	case FUN_TIME_SYNC:
		return TI_TIME_SYNC
	case FUN_SPONTANEOUS:
		return TI_TIME_SYNC // 突发传输默认带时标
	case FUN_GENERIC_CLASS_DATA:
		return TI_GENERIC_CLASS_DATA
	default:
		return TI_TIME_SYNC
	}
}

// GetStats 获取统计信息
func (c *Client) GetStats() ClientStats {
	return c.stats
}

// ─────────────────────────────────────────────────────────────────────────────
// ASDU 解析（IEC103 格式）
// ─────────────────────────────────────────────────────────────────────────────

// ParseASDU 解析 ASDU（IEC103 格式）
// IEC103 ASDU 结构：TI(1) | VSQ(1) | COT(1) | FUN(1) | INF(1) | CA(1) | Data
func ParseASDU(data []byte) (*ASDU, error) {
	if len(data) < 6 {
		return nil, errors.New("ASDU too short")
	}

	asdu := &ASDU{
		TI:  data[0], // 类型标识
		VSQ: data[1], // 可变结构限定词
		COT: data[2], // 传输原因
		FUN: data[3], // 功能类型（IEC103 特有）
		INF: data[4], // 信息号（IEC103 特有）
		CA:  data[5], // 公共地址
	}

	if len(data) > 6 {
		asdu.Data = data[6:]
	}

	return asdu, nil
}

// ASDU 应用服务数据单元（IEC103 格式）
type ASDU struct {
	TI   uint8 // 类型标识（Type Identification）
	VSQ  uint8 // 可变结构限定词
	COT  uint8 // 传输原因
	FUN  uint8 // 功能类型（IEC103 特有）
	INF  uint8 // 信息号（IEC103 特有）
	CA   uint8 // 公共地址
	Data []byte // 数据部分
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
	return fmt.Sprintf("ASDU{TI=%d, COT=%d, FUN=%d, INF=%d, CA=%d, DataLen=%d}",
		a.TI, a.COT, a.FUN, a.INF, a.CA, len(a.Data))
}

// ─────────────────────────────────────────────────────────────────────────────
// 点位查找（基于 FUN/INF）- IEC103 关键差异
// ─────────────────────────────────────────────────────────────────────────────

// BuildPointKey 构建点位查找 Key
// Key 格式：CA-FUN-INF（确保 O(1) 查找）
func BuildPointKey(ca uint8, fun uint8, inf uint8) string {
	return fmt.Sprintf("%d-%d-%d", ca, fun, inf)
}

// FrameBuffer 帧缓冲区
type FrameBuffer struct {
	buf      bytes.Buffer
	lastRead time.Time
	timeout  time.Duration
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
