// internal/driver/gb26875/frame.go - GB/T 26875.3 帧解析与编码
package gb26875

import (
	"encoding/binary"
	"fmt"
)

// ── 帧常量 ──────────────────────────────────────────────────────────

const (
	FrameStart1 = 0x40 // 启动符第1字节 '@'
	FrameStart2 = 0x40 // 启动符第2字节 '@'
	FrameEnd1   = 0x23 // 结束符第1字节 '#'
	FrameEnd2   = 0x23 // 结束符第2字节 '#'

	FrameStartLen  = 2    // 启动符长度
	FrameEndLen    = 2    // 结束符长度
	ControlUnitLen = 25   // 控制单元固定长度
	MaxADULen      = 1024 // 应用数据单元最大长度
	MinFrameLen    = FrameStartLen + ControlUnitLen + 1 + FrameEndLen // 最小帧 30 字节
)

// ── 命令字节 ────────────────────────────────────────────────────────

// Command byte values (Table 2 of GB/T 26875.3)
const (
	CmdReserved uint8 = 0 // 预留
	CmdControl  uint8 = 1 // 控制命令（时间同步）
	CmdSendData uint8 = 2 // 发送数据（上传报警/状态信息）
	CmdConfirm  uint8 = 3 // 确认
	CmdRequest  uint8 = 4 // 请求（查询操作）
	CmdReply    uint8 = 5 // 应答（返回查询信息）
	CmdDeny     uint8 = 6 // 否认
	// 7~127   预留
	// 128~255 用户自定义
)

// ── 类型标志（应用数据单元第一字节）────────────────────────────────

// 上行方向（装置 → 监控中心）
const (
	TypeUploadSystemStatus              uint8 = 1  // 上传建筑消防设施系统状态
	TypeUploadComponentStatus           uint8 = 2  // 上传建筑消防设施部件运行状态
	TypeUploadComponentAnalog           uint8 = 3  // 上传建筑消防设施部件模拟量值
	TypeUploadOperationInfo             uint8 = 4  // 上传建筑消防设施操作信息
	TypeUploadSWVersion                 uint8 = 5  // 上传建筑消防设施软件版本
	TypeUploadSysConfig                 uint8 = 6  // 上传建筑消防设施系统配置情况
	TypeUploadComponentConfig           uint8 = 7  // 上传建筑消防设施部件配置情况
	TypeUploadSystemTime                uint8 = 8  // 上传建筑消防设施系统时间
	TypeUploadTransmissionDeviceStatus  uint8 = 21 // 上传用户信息传输装置运行状态
	TypeUploadTransmissionOpInfo        uint8 = 24 // 上传用户信息传输装置操作信息
	TypeUploadTransmissionSWVer         uint8 = 25 // 上传用户信息传输装置软件版本
	TypeUploadTransmissionConfig        uint8 = 26 // 上传用户信息传输装置配置情况
	TypeUploadTransmissionTime          uint8 = 28 // 上传用户信息传输装置系统时间
)

// 下行方向（监控中心 → 装置）
const (
	TypeReadFireSystemStatus          uint8 = 61 // 读建筑消防设施系统状态
	TypeReadFireComponentStatus       uint8 = 62 // 读建筑消防设施部件运行状态
	TypeReadFireComponentAnalog       uint8 = 63 // 读建筑消防设施部件模拟量值
	TypeReadFireOperationInfo         uint8 = 64 // 读建筑消防设施操作信息
	TypeReadFireSWVersion             uint8 = 65 // 读建筑消防设施软件版本
	TypeReadFireSystemConfig          uint8 = 66 // 读建筑消防设施系统配置情况
	TypeReadFireComponentConfig       uint8 = 67 // 读建筑消防设施部件配置情况
	TypeReadFireSystemTime            uint8 = 68 // 读建筑消防设施系统时间
	TypeReadTransmissionDeviceStatus  uint8 = 81 // 读用户信息传输装置运行状态
	TypeReadTransmissionOperation     uint8 = 84 // 读用户信息传输装置操作信息记录
	TypeReadTransmissionSWVer         uint8 = 85 // 读用户信息传输装置软件版本
	TypeReadTransmissionConfig        uint8 = 86 // 读用户信息传输装置配置情况
	TypeReadTransmissionTime          uint8 = 88 // 读用户信息传输装置系统时间
	TypeInitializeTransmissionDevice  uint8 = 89 // 初始化用户信息传输装置
	TypeSyncClock                     uint8 = 90 // 同步用户信息传输装置时钟
	TypeCheckPost                     uint8 = 91 // 查岗命令
)

// ── 帧结构体 ────────────────────────────────────────────────────────

// TimeLabel 时间标签（6字节，BCD编码）
// 顺序：秒(1) 分(1) 时(1) 日(1) 月(1) 年(1)
type TimeLabel [6]byte

// Frame GB/T 26875.3 帧
// 控制单元字段平铺，避免嵌入带来的字段访问不便
type Frame struct {
	// 控制单元（25字节）
	SequenceNo uint16    // 业务流水号（低字节在前）
	Version    uint8     // 主版本号（固定=1）
	UserVer    uint8     // 用户版本号
	Time       TimeLabel // 时间标签
	SrcAddr    [6]byte   // 源地址
	DstAddr    [6]byte   // 目的地址
	ADULength  uint16    // 应用数据单元长度（低字节在前）
	Cmd        uint8     // 命令字节

	// 数据部分
	ADU []byte // 应用数据单元
	CS  uint8  // 校验和
	Raw []byte // 完整原始帧数据，调试用
}

// ── 帧解析 ──────────────────────────────────────────────────────────

// ParseFrame 从字节切片解析 GB/T 26875.3 帧
func ParseFrame(data []byte) (*Frame, error) {
	if len(data) < MinFrameLen {
		return nil, fmt.Errorf("gb26875: frame too short: %d bytes, need at least %d",
			len(data), MinFrameLen)
	}

	// 1. 验证启动符 (0x40 0x40)
	if data[0] != FrameStart1 || data[1] != FrameStart2 {
		return nil, fmt.Errorf("gb26875: invalid start marker: [%02X %02X], expected [%02X %02X]",
			data[0], data[1], FrameStart1, FrameStart2)
	}

	// 2. 验证结束符 (0x23 0x23)
	endOffset := len(data) - FrameEndLen
	if data[endOffset] != FrameEnd1 || data[endOffset+1] != FrameEnd2 {
		return nil, fmt.Errorf("gb26875: invalid end marker: [%02X %02X], expected [%02X %02X]",
			data[endOffset], data[endOffset+1], FrameEnd1, FrameEnd2)
	}

	f := &Frame{}
	f.Raw = make([]byte, len(data))
	copy(f.Raw, data)

	// 3. 解析控制单元（25字节，从索引2开始）
	offset := FrameStartLen // = 2

	// 业务流水号（2字节，低字节在前）
	f.SequenceNo = binary.LittleEndian.Uint16(data[offset : offset+2])
	offset += 2

	// 协议版本号
	f.Version = data[offset]
	f.UserVer = data[offset+1]
	offset += 2

	// 时间标签（6字节，BCD编码）
	copy(f.Time[:], data[offset:offset+6])
	offset += 6

	// 源地址（6字节）
	copy(f.SrcAddr[:], data[offset:offset+6])
	offset += 6

	// 目的地址（6字节）
	copy(f.DstAddr[:], data[offset:offset+6])
	offset += 6

	// 应用数据单元长度（2字节，小端）
	f.ADULength = binary.LittleEndian.Uint16(data[offset : offset+2])
	offset += 2

	// 命令字节
	f.Cmd = data[offset]
	offset += 1

	// 4. 验证 ADU 长度合理性
	if f.ADULength > MaxADULen {
		return nil, fmt.Errorf("gb26875: ADU length %d exceeds maximum %d",
			f.ADULength, MaxADULen)
	}

	// 5. 检查帧完整性
	expectedLen := FrameStartLen + ControlUnitLen + int(f.ADULength) + 1 + FrameEndLen
	if len(data) < expectedLen {
		return nil, fmt.Errorf("gb26875: incomplete frame: need %d bytes, got %d",
			expectedLen, len(data))
	}

	// 6. 应用数据单元（从第28字节开始，即offset=27）
	aduStart := offset // = 2 + 25 = 27
	aduEnd := aduStart + int(f.ADULength)
	if f.ADULength > 0 {
		f.ADU = make([]byte, f.ADULength)
		copy(f.ADU, data[aduStart:aduEnd])
	}

	// 7. 校验和
	f.CS = data[aduEnd]

	// 8. 验证校验和：控制单元(第3~27字节) + 应用数据单元，算术和 mod 256
	checkData := data[FrameStartLen:aduEnd]
	calcCS := calculateChecksum(checkData)
	if calcCS != f.CS {
		return nil, fmt.Errorf("gb26875: checksum mismatch: calculated 0x%02X, got 0x%02X",
			calcCS, f.CS)
	}

	return f, nil
}

// calculateChecksum 计算校验和：所有字节算术和，模256舍去进位
func calculateChecksum(data []byte) uint8 {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	return uint8(sum) // 模256，舍去进位
}

// ── 帧构建 ──────────────────────────────────────────────────────────

// BuildFrame 构建完整的 GB/T 26875.3 帧
func BuildFrame(seqNo uint16, ver, userVer uint8, t TimeLabel, src, dst [6]byte,
	cmd uint8, adu []byte) []byte {

	aduLen := len(adu)

	// 总帧大小：启动符(2) + 控制单元(25) + ADU + CS(1) + 结束符(2)
	totalLen := FrameStartLen + ControlUnitLen + aduLen + 1 + FrameEndLen
	frame := make([]byte, totalLen)

	offset := 0

	// 1. 启动符
	frame[offset] = FrameStart1
	frame[offset+1] = FrameStart2
	offset += FrameStartLen

	// 记录控制单元起始位（用于后续校验和计算）
	cuStart := offset

	// 2. 业务流水号（2字节，低字节在前）
	binary.LittleEndian.PutUint16(frame[offset:offset+2], seqNo)
	offset += 2

	// 3. 协议版本号（2字节）
	frame[offset] = ver
	frame[offset+1] = userVer
	offset += 2

	// 4. 时间标签（6字节）
	copy(frame[offset:offset+6], t[:])
	offset += 6

	// 5. 源地址（6字节）
	copy(frame[offset:offset+6], src[:])
	offset += 6

	// 6. 目的地址（6字节）
	copy(frame[offset:offset+6], dst[:])
	offset += 6

	// 7. 应用数据单元长度（2字节，低字节在前）
	binary.LittleEndian.PutUint16(frame[offset:offset+2], uint16(aduLen))
	offset += 2

	// 8. 命令字节
	frame[offset] = cmd
	offset += 1

	// 9. 应用数据单元（变长，可为空）
	if aduLen > 0 {
		copy(frame[offset:offset+aduLen], adu)
	}
	offset += aduLen

	// 10. 校验和（控制单元 + ADU，算术和 mod 256）
	frame[offset] = calculateChecksum(frame[cuStart:offset])
	offset += 1

	// 11. 结束符
	frame[offset] = FrameEnd1
	frame[offset+1] = FrameEnd2

	return frame
}

// BuildAckFrame 构建确认帧（命令字=3，无 ADU）
func BuildAckFrame(seqNo uint16, ver, userVer uint8, t TimeLabel, src, dst [6]byte) []byte {
	return BuildFrame(seqNo, ver, userVer, t, src, dst, CmdConfirm, nil)
}

// BuildDenyFrame 构建否认帧（命令字=6，无 ADU）
func BuildDenyFrame(seqNo uint16, ver, userVer uint8, t TimeLabel, src, dst [6]byte) []byte {
	return BuildFrame(seqNo, ver, userVer, t, src, dst, CmdDeny, nil)
}

// BuildRequestFrame 构建请求帧（命令字=4）
func BuildRequestFrame(seqNo uint16, ver, userVer uint8, t TimeLabel, src, dst [6]byte, adu []byte) []byte {
	return BuildFrame(seqNo, ver, userVer, t, src, dst, CmdRequest, adu)
}

// BuildControlFrame 构建控制命令帧（命令字=1）
func BuildControlFrame(seqNo uint16, ver, userVer uint8, t TimeLabel, src, dst [6]byte, adu []byte) []byte {
	return BuildFrame(seqNo, ver, userVer, t, src, dst, CmdControl, adu)
}

// BuildReplyFrame 构建应答帧（命令字=5）
func BuildReplyFrame(seqNo uint16, ver, userVer uint8, t TimeLabel, src, dst [6]byte, adu []byte) []byte {
	return BuildFrame(seqNo, ver, userVer, t, src, dst, CmdReply, adu)
}

// ── 地址工具函数 ────────────────────────────────────────────────────

// StringAddr 将6字节地址转换为可读字符串
// 按线网字节序（addr[0]..addr[5]）输出，与线网传输顺序一致。
// 例如线网字节 {0x80,0x0D,0,0,0,0} → "800D00000000"，与 CSV DeviceAddress 一一对应。
func StringAddr(addr [6]byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X%02X%02X",
		addr[0], addr[1], addr[2], addr[3], addr[4], addr[5])
}

// ParseAddrString 解析地址字符串 → [6]byte
// 支持 "800D00000000" 或 "80-0D-00-00-00-00" 格式
// 字符串为线网字节序（与人可读的 CSV DeviceAddress 一致），存储按线网顺序，不做反转。
func ParseAddrString(s string) ([6]byte, error) {
	var addr [6]byte

	// 去除分隔符
	clean := ""
	for _, c := range s {
		if c != '-' {
			clean += string(c)
		}
	}

	if len(clean) < 12 {
		return addr, fmt.Errorf("gb26875: address too short: %s (need 12 hex digits)", s)
	}

	// 按线网字节序直接解析（高字节对在前，与线网传输顺序一致）
	for i := 0; i < 6; i++ {
		hi := hexDigit(clean[i*2])
		lo := hexDigit(clean[i*2+1])
		if hi == 0xFF || lo == 0xFF {
			return addr, fmt.Errorf("gb26875: invalid hex in address: %s", s)
		}
		addr[i] = (hi << 4) | lo
	}

	return addr, nil
}

// ParseComponentAddrString 解析部件地址（4字节）字符串 → [4]byte
// 支持 "01000100" 或 "01-00-01-00" 格式
// 字符串为大端序表示（人类可读），存储为低字节在前格式（协议传输顺序）
func ParseComponentAddrString(s string) ([4]byte, error) {
	var addr [4]byte

	clean := ""
	for _, c := range s {
		if c != '-' {
			clean += string(c)
		}
	}

	if len(clean) < 8 {
		return addr, fmt.Errorf("gb26875: component address too short: %s (need 8 hex digits)", s)
	}

	// 先按大端序解析到临时数组
	var temp [4]byte
	for i := 0; i < 4; i++ {
		hi := hexDigit(clean[i*2])
		lo := hexDigit(clean[i*2+1])
		if hi == 0xFF || lo == 0xFF {
			return addr, fmt.Errorf("gb26875: invalid hex in component address: %s", s)
		}
		temp[i] = (hi << 4) | lo
	}

	// 转为低字节在前存储（协议传输顺序）
	for i := 0; i < 4; i++ {
		addr[i] = temp[3-i]
	}

	return addr, nil
}

// StringComponentAddr4 4字节部件地址 → 可读字符串（高字节在前显示）
func StringComponentAddr4(addr [4]byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X", addr[3], addr[2], addr[1], addr[0])
}

// hexDigit 十六进制字符 → 值
func hexDigit(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return 0xFF
	}
}

// ── 时间标签工具 ─────────────────────────────────────────────────────

// String 返回时间标签的可读表示
func (t TimeLabel) String() string {
	return fmt.Sprintf("20%02X-%02X-%02X %02X:%02X:%02X",
		t[5], t[4], t[3], t[2], t[1], t[0])
}

// FormatTimeLabel 从年月日时分秒创建时间标签（BCD编码）
// year: 20xx 年的后两位（如 2021 → 21）
func FormatTimeLabel(year, month, day, hour, minute, second int) TimeLabel {
	return TimeLabel{
		bcdEncode(second),
		bcdEncode(minute),
		bcdEncode(hour),
		bcdEncode(day),
		bcdEncode(month),
		bcdEncode(year),
	}
}

// bcdEncode 整数 → BCD 字节（0-99）
func bcdEncode(v int) byte {
	return byte(((v/10)&0x0F)<<4) | byte((v%10)&0x0F)
}

// IsZero 检查时间标签是否为全零
func (t TimeLabel) IsZero() bool {
	for _, b := range t {
		if b != 0 {
			return false
		}
	}
	return true
}

// ── 帧类型判断 ─────────────────────────────────────────────────────

// IsUpload 是否为上传帧（上行）
func (f *Frame) IsUpload() bool {
	return f.Cmd == CmdSendData || f.Cmd == CmdReply
}

// IsCommand 是否为命令/请求帧（下行）
func (f *Frame) IsCommand() bool {
	return f.Cmd == CmdControl || f.Cmd == CmdRequest
}

// IsAck 是否为确认类帧
func (f *Frame) IsAck() bool {
	return f.Cmd == CmdConfirm || f.Cmd == CmdDeny
}