// internal/driver/dlt645/frame.go - DL/T 645 帧解析与编码
package dlt645

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// FrameType 帧类型
type FrameType int

const (
	FrameTypeUnknown FrameType = iota
	FrameTypeRequest
	FrameTypeResponse
)

// Frame DL/T 645 帧
type Frame struct {
	Type     FrameType
	Address  [6]byte // 6字节地址（BCD码）
	C        uint8   // 控制码
	L        uint8   // 数据域长度
	Data     []byte  // 数据域
	CS       uint8   // 校验和
	Complete bool    // 是否完整
}

// 帧常量
const (
	FrameStart     = 0x68
	FrameEnd       = 0x16
	FrameStartFE   = 0xFE
)

// 控制码
const (
	// 1997版本
	CtrlRead1997    = 0x01
	CtrlReadRes1997 = 0x81
	// 2007版本
	CtrlRead2007    = 0x11
	CtrlReadRes2007 = 0x91
)

// 数据域处理常数
const (
	DataXOR = 0x33 // 数据域异或值
)

// ParseFrame 从原始数据解析帧
func ParseFrame(data []byte, version ProtocolVersion) (*Frame, error) {
	frame := &Frame{}

	// 跳过前导字节 FE
	offset := 0
	for offset < len(data) && data[offset] == FrameStartFE {
		offset++
	}

	if offset >= len(data) {
		return nil, fmt.Errorf("no frame start found")
	}

	// 查找第一个 0x68
	idx := bytes.IndexByte(data[offset:], FrameStart)
	if idx < 0 {
		return nil, fmt.Errorf("no 0x68 found")
	}
	offset += idx

	// 最小帧长度检查: 68 + 6地址 + 68 + C + L + CS + 16 = 12字节
	if len(data) < offset+12 {
		return nil, fmt.Errorf("frame too short: %d bytes", len(data)-offset)
	}

	// 检查第一个 0x68
	if data[offset] != FrameStart {
		return nil, fmt.Errorf("expected 0x68 at offset %d, got 0x%02X", offset, data[offset])
	}

	// 读取地址域 (6字节) - 地址低字节在前，直接存储
	addrBytes := data[offset+1 : offset+7]
	for i := 0; i < 6; i++ {
		frame.Address[i] = addrBytes[i]
	}

	// 检查第二个 0x68
	if data[offset+7] != FrameStart {
		return nil, fmt.Errorf("expected 0x68 at offset %d, got 0x%02X", offset+7, data[offset+7])
	}

	// 控制码
	frame.C = data[offset+8]

	// 长度
	frame.L = data[offset+9]

	// 检查帧长度
	frameLen := 12 + int(frame.L)
	if len(data) < offset+frameLen {
		return nil, fmt.Errorf("incomplete frame: need %d bytes, got %d", frameLen, len(data)-offset)
	}

	// 数据域
	frame.Data = make([]byte, frame.L)
	copy(frame.Data, data[offset+10:offset+10+int(frame.L)])

	// 校验和
	frame.CS = data[offset+10+int(frame.L)]

	// 结束符
	if data[offset+11+int(frame.L)] != FrameEnd {
		return nil, fmt.Errorf("expected 0x16 at end, got 0x%02X", data[offset+11+int(frame.L)])
	}

	// 验证校验和
	calcCS := calculateCS(data[offset : offset+10+int(frame.L)])
	if calcCS != frame.CS {
		return nil, fmt.Errorf("checksum mismatch: calculated 0x%02X, got 0x%02X", calcCS, frame.CS)
	}

	// 确定帧类型
	if version == Version2007 {
		if frame.C == CtrlRead2007 {
			frame.Type = FrameTypeRequest
		} else if frame.C == CtrlReadRes2007 {
			frame.Type = FrameTypeResponse
		}
	} else {
		if frame.C == CtrlRead1997 {
			frame.Type = FrameTypeRequest
		} else if frame.C == CtrlReadRes1997 {
			frame.Type = FrameTypeResponse
		}
	}

	frame.Complete = true
	return frame, nil
}

// BuildRequest 构建读数据请求帧
func BuildRequest(address [6]byte, dataID []byte, version ProtocolVersion, useLeadingByte bool) ([]byte, error) {
	// 计算数据域长度: 数据标识长度 + 数据(读请求无数据)
	dataIDLen := len(dataID)
	dataLen := dataIDLen // 读请求只有数据标识

	// 计算帧长度
	frameLen := 12 + dataLen // 固定12字节 + 数据
	if useLeadingByte {
		frameLen += 4 // 4字节前导FE
	}

	frame := make([]byte, frameLen)
	offset := 0

	// 前导字节
	if useLeadingByte {
		for i := 0; i < 4; i++ {
			frame[i] = FrameStartFE
		}
		offset = 4
	}

	// 帧开始
	frame[offset] = FrameStart
	offset++

	// 地址域 (6字节) - DL/T 645 地址低字节在前
	// 例如 "123456789012" 存储为 [0x12, 0x34, 0x56, 0x78, 0x90, 0x12]
	// 发送时需要反转: [0x12, 0x90, 0x78, 0x56, 0x34, 0x12]
	for i := 0; i < 6; i++ {
		frame[offset+i] = address[5-i]
	}
	offset += 6

	// 帧开始
	frame[offset] = FrameStart
	offset++

	// 控制码
	if version == Version2007 {
		frame[offset] = CtrlRead2007
	} else {
		frame[offset] = CtrlRead1997
	}
	offset++

	// 长度
	frame[offset] = byte(dataLen)
	offset++

	// 数据域 (数据标识 + 0x33) - DL/T 645-2007 标准规定
	// 数据标识按"低字节在前"发送（index 0 是低字节，index 3 是高字节）
	// 例如 DataID = [0x00, 0x01, 0x00, 0x00] 发送顺序为 00 01 00 00
	for i := 0; i < dataIDLen; i++ {
		frame[offset+i] = dataID[i] + DataXOR
	}
	offset += dataIDLen

	// 校验和
	frame[offset] = calculateCS(frame[:offset])
	offset++

	// 结束符
	frame[offset] = FrameEnd

	return frame, nil
}

// ParseResponse 解析响应数据
// 数据域格式: [数据标识] [数据值]
func ParseResponse(frame *Frame, version ProtocolVersion) (dataID []byte, values []byte, err error) {
	if frame.Type != FrameTypeResponse {
		return nil, nil, fmt.Errorf("not a response frame")
	}

	// 数据标识长度
	dataIDLen := frame.DataIDLen(version)
	if len(frame.Data) < dataIDLen {
		return nil, nil, fmt.Errorf("data too short for dataID: need %d, got %d", dataIDLen, len(frame.Data))
	}

	// 读取数据标识 (减 0x33 解码 - DL/T 645-2007 标准规定)
	// 帧中数据标识是低字节在前，直接存储
	dataID = make([]byte, dataIDLen)
	for i := 0; i < dataIDLen; i++ {
		dataID[i] = frame.Data[i] - DataXOR
	}

	// 数据值 (减 0x33 解码)
	values = make([]byte, len(frame.Data)-dataIDLen)
	for i := 0; i < len(values); i++ {
		values[i] = frame.Data[dataIDLen+i] - DataXOR
	}

	return dataID, values, nil
}

// DataIDLen 返回数据标识字节长度
func (f *Frame) DataIDLen(version ProtocolVersion) int {
	if version == Version1997 {
		return 2
	}
	return 4
}

// calculateCS 计算校验和
// 从第一个0x68开始，到数据域结束（不包括CS本身）
func calculateCS(data []byte) uint8 {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	return uint8(sum)
}

// StringAddress 将6字节地址转换为字符串
func StringAddress(addr [6]byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X%02X%02X",
		addr[5], addr[4], addr[3], addr[2], addr[1], addr[0])
}

// ParseAddressString 解析地址字符串 (12位BCD码)
func ParseAddressString(s string) ([6]byte, error) {
	var addr [6]byte
	if len(s) < 12 {
		return addr, fmt.Errorf("address too short: %s", s)
	}

	// 去掉可能的分隔符 '-'
	clean := ""
	for _, c := range s[:12] {
		if c != '-' {
			clean += string(c)
		}
	}

	if len(clean) < 12 {
		return addr, fmt.Errorf("address too short after cleaning: %s", s)
	}

	// 每2个字符转1字节BCD
	for i := 0; i < 6; i++ {
		hi := char2BCD(clean[i*2])
		lo := char2BCD(clean[i*2+1])
		addr[i] = (hi << 4) | lo
	}

	return addr, nil
}

// ParseDataIDString 解析数据标识字符串
// DL/T 645-2007 数据标识格式：DI3 DI2 DI1 DI0（高字节在前，与地址逻辑相反）
// 例如 "00010000" → DI3=0x00, DI2=0x01, DI1=0x00, DI0=0x00
// 帧中按低字节在前发送：[DI0, DI1, DI2, DI3]
// 本函数存储为低字节在前（与帧一致）：
// "00010000" → []byte{0x00, 0x00, 0x01, 0x00}
// 1997: 4字符如 "9010" -> 2字节
// 2007: 8字符如 "00010000" -> 4字节
func ParseDataIDString(s string, version ProtocolVersion) ([]byte, error) {
	// 去掉可能的分隔符 '-'
	clean := ""
	for _, c := range s {
		if c != '-' {
			clean += string(c)
		}
	}

	var expectedLen int
	if version == Version1997 {
		expectedLen = 4
	} else {
		expectedLen = 8
	}

	if len(clean) < expectedLen {
		return nil, fmt.Errorf("dataID too short: %s, expected %d chars", s, expectedLen)
	}

	dataIDLen := expectedLen / 2
	data := make([]byte, dataIDLen)
	// 反转存储：CSV 字符串是高字节在前 (DI3,DI2,DI1,DI0)
	// 帧中需要低字节在前 (DI0,DI1,DI2,DI3)，BuildRequest/SendRequest 会按索引顺序直接发送
	for i := 0; i < dataIDLen; i++ {
		hi := char2BCD(clean[(dataIDLen-1-i)*2])
		lo := char2BCD(clean[(dataIDLen-1-i)*2+1])
		data[i] = (hi << 4) | lo
	}

	return data, nil
}

// char2BCD 字符转BCD码
func char2BCD(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return 0xFF // 无效字符
}

// BCD2Float64 将BCD码转换为float64
// DL/T 645-2007 数据解码步骤：
// 1. 数据值 XOR 解码后: [45, 23, 01, 00] (4字节)
// 2. 反转字节序: [00, 01, 23, 45] (高字节在前)
// 3. 每字节作为 BCD 数字（0-99），从前往后拼接
// 4. 结果: 00*1000000 + 01*10000 + 23*100 + 45 = 12345
func BCD2Float64(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}

	// 反转字节序（低字节在前 -> 高字节在前）
	reversed := make([]byte, len(data))
	for i := 0; i < len(data); i++ {
		reversed[i] = data[len(data)-1-i]
	}

	// 从前往后，每字节作为一个 BCD 数字（0-99），依次拼接
	// 例如: reversed = [00, 01, 23, 45] -> 0*1000000 + 1*10000 + 23*100 + 45 = 12345
	var result float64
	for i := 0; i < len(reversed); i++ {
		hi := float64((reversed[i] >> 4) & 0x0F)
		lo := float64(reversed[i] & 0x0F)
		result = result*100 + hi*10 + lo
	}
	return result
}

// BCD2Uint64 将BCD码转换为uint64
// 与 BCD2Float64 相同逻辑，但返回整数
func BCD2Uint64(data []byte) uint64 {
	if len(data) == 0 {
		return 0
	}

	// 反转字节序（低字节在前 -> 高字节在前）
	reversed := make([]byte, len(data))
	for i := 0; i < len(data); i++ {
		reversed[i] = data[len(data)-1-i]
	}

	// 从前往后，每字节作为一个 BCD 数字，依次拼接
	var result uint64
	for i := 0; i < len(reversed); i++ {
		hi := uint64((reversed[i] >> 4) & 0x0F)
		lo := uint64(reversed[i] & 0x0F)
		result = result*100 + hi*10 + lo
	}
	return result
}

// ReverseBytes 反转字节序（从高字节在前转为低字节在前）
func ReverseBytes(data []byte) []byte {
	result := make([]byte, len(data))
	for i := 0; i < len(data); i++ {
		result[i] = data[len(data)-1-i]
	}
	return result
}

// ReadUint16BE 从大端序读取uint16
func ReadUint16BE(data []byte) uint16 {
	return binary.BigEndian.Uint16(data)
}

// ReadUint32BE 从大端序读取uint32
func ReadUint32BE(data []byte) uint32 {
	return binary.BigEndian.Uint32(data)
}