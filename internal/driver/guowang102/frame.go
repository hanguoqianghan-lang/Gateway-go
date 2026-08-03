// internal/driver/guowang102/frame.go - 国网102规约 FT1.2 帧编解码 (TCP/IP 版)
// 基于 IEC 60870-5-102 帧格式，适配以太网传输
package guowang102

import (
	"errors"
	"fmt"
)

// ─────────────────────────────────────────────────────────────────────────────
// 帧常量定义
// ─────────────────────────────────────────────────────────────────────────────

const (
	// 启动/结束字节
	StartByteFixed    = 0x10 // 固定长度帧启动字节
	StartByteVariable = 0x68 // 可变长度帧启动字节
	EndByte           = 0x16 // 结束字节
	SingleByteACK     = 0xE5 // 单字节确认 (E5)

	// 控制域类型 (低2位)
	FrameTypeMask = 0x03
	C_U           = 0x00 // U帧：无编号控制帧
	C_S           = 0x01 // S帧：确认帧
	C_I           = 0x03 // I帧：信息帧

	// U帧功能码 (Bit3-Bit0)
	FC_RESET_REMOTE_LINK   = 0x00 // 复位远方链路
	FC_SEND_CONFIRM        = 0x03 // 发送/确认 (传送数据，FC=3)
	FC_REQUEST_RESPOND     = 0x03 // 请求/响应 (上行使用相同值，通过PRM区分)
	FC_START_DATA_TRANSFER = 0x04 // 启动数据传输

	// 请求/响应功能码 (FC=9/10/11, FCV=1)
	FC_REQUEST_LINK_STATUS = 0x09 // 召唤链路状态
	FC_REQUEST_LEVEL1_DATA = 0x0A // 召唤1级用户数据
	FC_REQUEST_LEVEL2_DATA = 0x0B // 召唤2级用户数据

	// 上行响应功能码
	FC_DATA_RESPONSE = 0x08   // 以数据回答请求帧
	FC_NO_DATA = 0x09         // 无所召唤数据
	FC_STATUS_RESPONSE = 0x0B // 以链路状态/访问请求回答

	// 固定地址
	DefaultLinkAddress   = 0xFFFF // 链路地址固定 0xFFFF
	DefaultCommonAddress = 0xFFFF // 公共地址固定 0xFFFF

	// 帧长度限制
	MinFixedFrameLen     = 5  // 10H C A CS 16H
	MinVariableFrameLen  = 9  // 68H L L 68H C A CS 16H (无ASDU)
	MaxVariableFrameLen  = 260 // 最大可变帧长度
)

// ─────────────────────────────────────────────────────────────────────────────
// 控制域位定义
// ─────────────────────────────────────────────────────────────────────────────

// 下行控制域 (主站→子站, PRM=1, DIR=0)
// Bit7: DIR=0(固定), Bit6: PRM=1(固定), Bit5: FCB, Bit4: FCV, Bit3-Bit0: FC
type DownlinkControl struct {
	FCB bool // 帧计数位
	FCV bool // 帧计数有效位
	FC  uint8 // 功能码 (0-15)
}

func (c *DownlinkControl) Encode() byte {
	var b byte
	b |= 0x40 // PRM=1 (Bit6)
	if c.FCB {
		b |= 0x20 // FCB=1 (Bit5)
	}
	if c.FCV {
		b |= 0x10 // FCV=1 (Bit4)
	}
	b |= (c.FC & 0x0F) // FC (Bit3-Bit0)
	return b
}

func DecodeDownlinkControl(b byte) *DownlinkControl {
	return &DownlinkControl{
		FCB: (b & 0x20) != 0,
		FCV: (b & 0x10) != 0,
		FC:  b & 0x0F,
	}
}

// 上行控制域 (子站→主站, PRM=0)
// Bit7: 备用, Bit6: ACD(要求访问), Bit5: DFC(流控), Bit4: 备用, Bit3-Bit0: FC
type UplinkControl struct {
	ACD bool // 要求访问位：1=有1级数据待传
	DFC bool // 数据流控制位：1=缓冲区满，暂停发送
	FC  uint8 // 功能码
}

func (c *UplinkControl) Encode() byte {
	var b byte
	if c.ACD {
		b |= 0x40 // ACD (Bit6)
	}
	if c.DFC {
		b |= 0x20 // DFC (Bit5)
	}
	b |= (c.FC & 0x0F) // FC (Bit3-Bit0)
	return b
}

func DecodeUplinkControl(b byte) *UplinkControl {
	return &UplinkControl{
		ACD: (b & 0x40) != 0,
		DFC: (b & 0x20) != 0,
		FC:  b & 0x0F,
	}
}

// I帧控制域
// 下行: Bit7=0(DIR), Bit6=1(PRM), Bit5=FCB, Bit4=FCV=1, Bit3-Bit0=3(C_I)
// 上行: Bit7=0, Bit6=ACD, Bit5=DFC, Bit4=0, Bit3-Bit0=3(C_I)
// 序列号在发送/接收端单独维护，不在控制域中（FT1.2单比特FCB）

// ─────────────────────────────────────────────────────────────────────────────
// 帧结构体
// ─────────────────────────────────────────────────────────────────────────────

type FrameType int

const (
	FrameTypeFixed     FrameType = 0 // 固定长度帧
	FrameTypeVariable  FrameType = 1 // 可变长度帧
	FrameTypeSingleACK FrameType = 2 // 单字节确认
)

type Frame struct {
	Type       FrameType
	Control    byte       // 原始控制域字节
	Address    uint16     // 链路地址 (低字节在前)
	ASDU       []byte     // 可变帧携带的 ASDU 数据
	Raw        []byte     // 原始完整帧 (调试用)
}

// ─────────────────────────────────────────────────────────────────────────────
// 校验和计算
// ─────────────────────────────────────────────────────────────────────────────

// CalcCS 计算校验和：字节累加和，保留低8位
func CalcCS(data []byte) byte {
	var sum uint16
	for _, b := range data {
		sum += uint16(b)
	}
	return byte(sum & 0xFF)
}

// ─────────────────────────────────────────────────────────────────────────────
// 帧构建 (发送方使用)
// ─────────────────────────────────────────────────────────────────────────────

// BuildFixedFrame 构建固定长度帧
// 格式：10H | C | A(低) | A(高) | CS | 16H
func BuildFixedFrame(control byte, address uint16) []byte {
	buf := make([]byte, 6)
	buf[0] = StartByteFixed
	buf[1] = control
	buf[2] = byte(address & 0xFF)       // 地址低字节
	buf[3] = byte((address >> 8) & 0xFF) // 地址高字节
	buf[4] = CalcCS(buf[1:4])           // CS = C + A_low + A_high
	buf[5] = EndByte
	return buf
}

// BuildVariableFrame 构建可变长度帧
// 格式：68H | L | L | 68H | C | A(低) | A(高) | ASDU | CS | 16H
// L = ASDU长度 + 3 (C + A_low + A_high)
func BuildVariableFrame(control byte, address uint16, asdu []byte) []byte {
	asduLen := len(asdu)
	l := asduLen + 3 // C(1) + A(2)
	if l > 255 {
		l = 255 // 限制最大长度
		asdu = asdu[:252]
		asduLen = 252
	}

	totalLen := 4 + l + 2 // 头部4 + L字节数据 + CS(1) + End(1) = l + 6
	buf := make([]byte, totalLen)

	buf[0] = StartByteVariable
	buf[1] = byte(l)
	buf[2] = byte(l)
	buf[3] = StartByteVariable
	buf[4] = control
	buf[5] = byte(address & 0xFF)
	buf[6] = byte((address >> 8) & 0xFF)
	copy(buf[7:], asdu)
	csIndex := 7 + asduLen
	buf[csIndex] = CalcCS(buf[4 : csIndex]) // CS = C + A + ASDU
	buf[csIndex+1] = EndByte

	return buf
}

// BuildSingleACK 构建单字节确认帧
func BuildSingleACK() []byte {
	return []byte{SingleByteACK}
}

// ─────────────────────────────────────────────────────────────────────────────
// 帧解析 (接收方使用)
// ─────────────────────────────────────────────────────────────────────────────

var (
	ErrFrameTooShort     = errors.New("frame too short")
	ErrInvalidStartByte  = errors.New("invalid start byte")
	ErrInvalidEndByte    = errors.New("invalid end byte")
	ErrCSMismatch        = errors.New("checksum mismatch")
	ErrLengthMismatch    = errors.New("length mismatch")
	ErrInvalidFrameType  = errors.New("invalid frame type")
)

// ParseFrame 解析完整帧数据
// 支持固定帧、可变帧、单字节确认帧
func ParseFrame(data []byte) (*Frame, error) {
	if len(data) == 0 {
		return nil, ErrFrameTooShort
	}

	switch data[0] {
	case StartByteFixed:
		return parseFixedFrame(data)
	case StartByteVariable:
		return parseVariableFrame(data)
	case SingleByteACK:
		return parseSingleACK(data)
	default:
		return nil, fmt.Errorf("%w: 0x%02X", ErrInvalidStartByte, data[0])
	}
}

func parseFixedFrame(data []byte) (*Frame, error) {
	if len(data) < MinFixedFrameLen {
		return nil, ErrFrameTooShort
	}

	// 验证结束字节
	if data[5] != EndByte {
		return nil, ErrInvalidEndByte
	}

	// 验证校验和：C + A_low + A_high
	expectedCS := CalcCS(data[1:4])
	if data[4] != expectedCS {
		return nil, fmt.Errorf("%w: expected 0x%02X, got 0x%02X", ErrCSMismatch, expectedCS, data[4])
	}

	address := uint16(data[2]) | (uint16(data[3]) << 8)

	return &Frame{
		Type:    FrameTypeFixed,
		Control: data[1],
		Address: address,
		ASDU:    nil,
		Raw:     data,
	}, nil
}

func parseVariableFrame(data []byte) (*Frame, error) {
	if len(data) < MinVariableFrameLen {
		return nil, ErrFrameTooShort
	}

	// 验证长度字段重复
	l1 := data[1]
	l2 := data[2]
	if l1 != l2 {
		return nil, fmt.Errorf("%w: L1=0x%02X != L2=0x%02X", ErrLengthMismatch, l1, l2)
	}

	// 验证第二个启动字节
	if data[3] != StartByteVariable {
		return nil, ErrInvalidStartByte
	}

	// 计算期望总长度：头部4 + L + CS(1) + End(1) = L + 6
	expectedTotalLen := int(l1) + 6
	if len(data) < expectedTotalLen {
		return nil, ErrFrameTooShort
	}

	// 验证结束字节
	endIdx := expectedTotalLen - 1
	if data[endIdx] != EndByte {
		return nil, ErrInvalidEndByte
	}

	// 验证校验和：从控制域开始到ASDU结束
	csStart := 4
	csEnd := csStart + int(l1) // 包含 C(1) + A(2) + ASDU
	expectedCS := CalcCS(data[csStart:csEnd])
	if data[csEnd] != expectedCS {
		return nil, fmt.Errorf("%w: expected 0x%02X, got 0x%02X", ErrCSMismatch, expectedCS, data[csEnd])
	}

	address := uint16(data[5]) | (uint16(data[6]) << 8)
	asduLen := int(l1) - 3 // 减去 C(1) + A(2)
	var asdu []byte
	if asduLen > 0 {
		asdu = make([]byte, asduLen)
		copy(asdu, data[7:7+asduLen])
	}

	return &Frame{
		Type:    FrameTypeVariable,
		Control: data[4],
		Address: address,
		ASDU:    asdu,
		Raw:     data[:expectedTotalLen],
	}, nil
}

func parseSingleACK(data []byte) (*Frame, error) {
	if len(data) < 1 || data[0] != SingleByteACK {
		return nil, ErrInvalidFrameType
	}
	return &Frame{
		Type:    FrameTypeSingleACK,
		Control: 0,
		Address: 0,
		ASDU:    nil,
		Raw:     data[:1],
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 便捷构建函数
// ─────────────────────────────────────────────────────────────────────────────

// BuildResetLink 复位链路帧 (FC=0, FCV=0)
func BuildResetLink(address uint16) []byte {
	ctrl := DownlinkControl{FCB: false, FCV: false, FC: FC_RESET_REMOTE_LINK}
	return BuildFixedFrame(ctrl.Encode(), address)
}

// BuildStartDataTransfer 启动数据传输帧 (FC=4, FCV=0)
func BuildStartDataTransfer(address uint16) []byte {
	ctrl := DownlinkControl{FCB: false, FCV: false, FC: FC_START_DATA_TRANSFER}
	return BuildFixedFrame(ctrl.Encode(), address)
}

// BuildRequestLinkStatus 请求链路状态 (FC=9, FCV=0)
func BuildRequestLinkStatus(address uint16) []byte {
	ctrl := DownlinkControl{FCB: false, FCV: false, FC: FC_REQUEST_LINK_STATUS}
	return BuildFixedFrame(ctrl.Encode(), address)
}

// BuildRequestLevel1Data 请求1级用户数据 (FC=10, FCV=1)
func BuildRequestLevel1Data(address uint16, fcb bool) []byte {
	ctrl := DownlinkControl{FCB: fcb, FCV: true, FC: FC_REQUEST_LEVEL1_DATA}
	return BuildFixedFrame(ctrl.Encode(), address)
}

// BuildRequestLevel2Data 请求2级用户数据 (FC=11, FCV=1)
func BuildRequestLevel2Data(address uint16, fcb bool) []byte {
	ctrl := DownlinkControl{FCB: fcb, FCV: true, FC: FC_REQUEST_LEVEL2_DATA}
	return BuildFixedFrame(ctrl.Encode(), address)
}

// BuildSendConfirmData 发送确认数据帧 (FC=3, FCV=1) - 用于发送ASDU
func BuildSendConfirmData(address uint16, fcb bool, asdu []byte) []byte {
	ctrl := DownlinkControl{FCB: fcb, FCV: true, FC: FC_SEND_CONFIRM}
	return BuildVariableFrame(ctrl.Encode(), address, asdu)
}

// BuildSFrame 发送S帧确认 (接收序号确认)
func BuildSFrame(address uint16, recvSeq bool) []byte {
	// S帧控制域: 0x01 | (recvSeq << 1)
	var ctrl byte = C_S
	if recvSeq {
		ctrl |= 0x02
	}
	return BuildFixedFrame(ctrl, address)
}

// IsDownlinkFrame 判断是否为下行帧 (主站发送)
func (f *Frame) IsDownlinkFrame() bool {
	if f.Type == FrameTypeSingleACK {
		return false // 单字节确认没有方向性
	}
	if f.Type == FrameTypeFixed || f.Type == FrameTypeVariable {
		// PRM=1 (Bit6)
		return (f.Control & 0x40) != 0
	}
	return false
}

// IsUplinkFrame 判断是否为上行帧 (子站发送)
func (f *Frame) IsUplinkFrame() bool {
	if f.Type == FrameTypeSingleACK {
		return false
	}
	return !f.IsDownlinkFrame()
}

// GetDownlinkControl 解析下行控制域
func (f *Frame) GetDownlinkControl() *DownlinkControl {
	return DecodeDownlinkControl(f.Control)
}

// GetUplinkControl 解析上行控制域
func (f *Frame) GetUplinkControl() *UplinkControl {
	return DecodeUplinkControl(f.Control)
}

// GetFunctionCode 获取功能码
func (f *Frame) GetFunctionCode() uint8 {
	return f.Control & 0x0F
}

// IsACK 判断是否为确认帧 (固定帧 FC=0 或单字节 E5)
func (f *Frame) IsACK() bool {
	if f.Type == FrameTypeSingleACK {
		return true
	}
	if f.Type == FrameTypeFixed {
		fc := f.GetFunctionCode()
		return fc == FC_RESET_REMOTE_LINK || fc == 0 // 复位命令或确认
	}
	return false
}