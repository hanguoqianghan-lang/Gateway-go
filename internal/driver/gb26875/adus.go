// internal/driver/gb26875/adus.go - GB/T 26875.3 应用数据单元（ADU）类型与解析
package gb26875

import (
	"encoding/binary"
	"fmt"
)

// ── ADU 头部 ────────────────────────────────────────────────────────

// ADU 应用数据单元
// 格式：类型标识(1B) + 信息对象数目(1B) + 信息对象1..N
type ADU struct {
	Type         uint8   // 类型标识（TypeFlag）
	ObjectCount  uint8   // 信息对象数目
	Objects      []byte  // 信息对象原始数据（按Type解析）
}

// ADUHeaderLen ADU头部固定长度（类型 + 数目 = 2字节）
const ADUHeaderLen = 2

// ParseADU 解析应用数据单元
// 输入：完整 ADU 字节切片（不含控制单元和校验和）
func ParseADU(data []byte) (*ADU, error) {
	if len(data) < ADUHeaderLen {
		return nil, fmt.Errorf("gb26875: ADU too short: %d bytes", len(data))
	}

	a := &ADU{
		Type:        data[0],
		ObjectCount: data[1],
		Objects:     make([]byte, len(data)-ADUHeaderLen),
	}
	copy(a.Objects, data[ADUHeaderLen:])

	return a, nil
}

// BuildADU 构建应用数据单元
func BuildADU(typeFlag uint8, objects []byte) []byte {
	adu := make([]byte, ADUHeaderLen+len(objects))
	adu[0] = typeFlag
	adu[1] = 1 // 默认1个信息对象
	copy(adu[ADUHeaderLen:], objects)
	return adu
}

// ── 系统类型（表4）──────────────────────────────────────────────────

const (
	SysTypeGeneral                uint8 = 0  // 通用
	SysTypeFireAlarm              uint8 = 1  // 火灾报警系统
	SysTypeFireLinkage            uint8 = 10 // 消防联动控制器
	SysTypeHydrant                uint8 = 11 // 消火栓系统
	SysTypeAutoSprinkler          uint8 = 12 // 自动喷水灭火系统
	SysTypeGasSuppression         uint8 = 13 // 气体灭火系统
	SysTypeWaterSprayPump         uint8 = 14 // 水喷雾灭火系统（泵启动方式）
	SysTypeWaterSprayVessel       uint8 = 15 // 水喷雾灭火系统（压力容器启动方式）
	SysTypeFoamSuppression        uint8 = 16 // 泡沫灭火系统
	SysTypeDryPowder              uint8 = 17 // 干粉灭火系统
	SysTypeSmokeExhaust           uint8 = 18 // 防烟排烟系统
	SysTypeFireDoorCurtain        uint8 = 19 // 防火门及卷帘系统
	SysTypeFireElevator           uint8 = 20 // 消防电梯
	SysTypeFireBroadcast          uint8 = 21 // 消防应急广播
	SysTypeEmergencyLighting      uint8 = 22 // 消防应急照明和疏散指示系统
	SysTypeFirePower              uint8 = 23 // 消防电源
	SysTypeFirePhone              uint8 = 24 // 消防电话
)

// ── 部件类型（表5，部分）─────────────────────────────────────────────

const (
	CompTypeGeneral              uint8 = 0   // 通用
	CompTypeFireAlarmController  uint8 = 1   // 火灾报警控制器
	CompTypeCombustibleGas       uint8 = 10  // 可燃气体探测器
	CompTypeElectricFireMonitor  uint8 = 16  // 电气火灾监控报警器
	CompTypeDetectionLoop        uint8 = 21  // 探测回路
	CompTypeFireDisplayPanel     uint8 = 22  // 火灾显示盘
	CompTypeManualAlarmButton    uint8 = 23  // 手动火灾报警按钮
	CompTypeHydrantButton        uint8 = 24  // 消火栓按钮
	CompTypeFireDetector         uint8 = 25  // 火灾探测器
	CompTypeHeatDetector         uint8 = 30  // 感温火灾探测器
	CompTypeSmokeDetector        uint8 = 40  // 感烟火灾探测器
	CompTypeIonSmokeDetector     uint8 = 41  // 点型离子感烟火灾探测器
	CompTypePhotoSmokeDetector   uint8 = 42  // 点型光电感烟火灾探测器
	CompTypeBeamSmokeDetector    uint8 = 43  // 线型光束感烟火灾探测器
	CompTypeAspiratingSmoke      uint8 = 44  // 吸气式感烟火灾探测器
	CompTypeCompositeDetector    uint8 = 50  // 复合式火灾探测器
	CompTypeUVFlame              uint8 = 61  // 紫外火焰探测器
	CompTypeIRFlame              uint8 = 62  // 红外火焰探测器
	CompTypeGasSuppressionCtrl   uint8 = 81  // 气体灭火控制器
	CompTypeFireElecCtrl         uint8 = 82  // 消防电气控制装置
	CompTypeModule              uint8 = 84  // 模块
	CompTypeFirePump            uint8 = 91  // 消防水泵
	CompTypeFireWaterTank        uint8 = 92  // 消防水箱
	CompTypeSprinklerPump       uint8 = 95  // 喷淋泵
	CompTypeWaterFlowIndicator   uint8 = 96  // 水流指示器
	CompTypeSignalValve          uint8 = 97  // 信号阀
	CompTypeAlarmValve           uint8 = 98  // 报警阀
	CompTypePressureSwitch       uint8 = 99  // 压力开关
	CompTypeFireDoor            uint8 = 102 // 防火门
	CompTypeSmokeExhaustFan      uint8 = 111 // 防烟排烟风机
	CompTypeSmokeExhaustValve    uint8 = 113 // 排烟防火阀
	CompTypeNormallyClosedInlet  uint8 = 114 // 常闭送风口
	CompTypeSmokeOutlet          uint8 = 115 // 排烟口
	CompTypeFireCurtainCtrl      uint8 = 117 // 防火卷帘控制器
	CompTypeAlarmDevice          uint8 = 121 // 警报装置
)

// ── 模拟量类型（表6）───────────────────────────────────────────────

const (
	AnalogTypeUnused        uint8 = 0  // 未用
	AnalogTypeEventCount   uint8 = 1  // 事件计数（件）
	AnalogTypeHeight       uint8 = 2  // 高度（m）
	AnalogTypeTemperature  uint8 = 3  // 温度（℃）
	AnalogTypePressureMPa  uint8 = 4  // 压力（MPa）
	AnalogTypePressureKPa  uint8 = 5  // 压力（kPa）
	AnalogTypeGasConc      uint8 = 6  // 气体浓度（%LEL）
	AnalogTypeTime         uint8 = 7  // 时间（s）
	AnalogTypeVoltage      uint8 = 8  // 电压（V）
	AnalogTypeCurrent      uint8 = 9  // 电流（A）
	AnalogTypeFlow         uint8 = 10 // 流量（L/s）
	AnalogTypeAirVolume    uint8 = 11 // 风量（m³/min）
	AnalogTypeWindSpeed    uint8 = 12 // 风速（m/s）
)

// AnalogScale 模拟量最小计量单位（用于将原始值乘以系数得到工程值）
var AnalogScale = map[uint8]float64{
	AnalogTypeEventCount:   1.0,    // 件
	AnalogTypeHeight:       0.01,   // m
	AnalogTypeTemperature:  0.1,    // ℃
	AnalogTypePressureMPa:  0.1,    // MPa
	AnalogTypePressureKPa:  0.1,    // kPa
	AnalogTypeGasConc:      0.1,    // %LEL
	AnalogTypeTime:         1.0,    // s
	AnalogTypeVoltage:      0.1,    // V
	AnalogTypeCurrent:      0.1,    // A
	AnalogTypeFlow:         0.1,    // L/s
	AnalogTypeAirVolume:    0.1,    // m³/min
	AnalogTypeWindSpeed:    1.0,    // m/s
}

// AnalogUnit 模拟量单位
var AnalogUnit = map[uint8]string{
	AnalogTypeEventCount:   "件",
	AnalogTypeHeight:       "m",
	AnalogTypeTemperature:  "℃",
	AnalogTypePressureMPa:  "MPa",
	AnalogTypePressureKPa:  "kPa",
	AnalogTypeGasConc:      "%LEL",
	AnalogTypeTime:         "s",
	AnalogTypeVoltage:      "V",
	AnalogTypeCurrent:      "A",
	AnalogTypeFlow:         "L/s",
	AnalogTypeAirVolume:    "m³/min",
	AnalogTypeWindSpeed:    "m/s",
}

// ── 地址编码方式（6种）─────────────────────────────────────────────

const (
	AddrFormatCircuitAddr   uint8 = 1 // 部件地址0(回路) + 1 + 部件地址2(地址号) + 3
	AddrFormatSingleNumber  uint8 = 2 // 4字节解析为1个10进制地址号
	AddrFormatCircuitAddr2  uint8 = 3 // 部件地址0(回路) + 1 + 2(地址号) + 3
	AddrFormatBuilding       uint8 = 4 // 系统地址(楼栋)+部件地址0(区)+1(楼)+2(层)+3
	AddrFormatBuilding2     uint8 = 5 // 系统地址(楼栋)+部件地址0(设备码)+1(户码)
	AddrFormatPointNumber   uint8 = 6 // 部件地址0~3(点位号)
)

// ── 信息对象数据结构 ────────────────────────────────────────────────

// SystemStatus 系统状态（8.2.1.1）— 4 字节
// 系统类型(1B) + 系统地址(1B) + 系统状态数据(2B)
type SystemStatus struct {
	SystemType uint8   // 系统类型（表4）
	SystemAddress uint8   // 系统地址
	StatusData uint16  // 系统状态数据
	Time       TimeLabel // 时间标签（部分场景下追加）
}

// ParseSystemStatus 解析系统状态
func ParseSystemStatus(data []byte) (*SystemStatus, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("gb26875: SystemStatus needs 4 bytes, got %d", len(data))
	}
	s := &SystemStatus{
		SystemType: data[0],
		SystemAddress: data[1],
		StatusData: binary.LittleEndian.Uint16(data[2:4]),
	}
	// 部分场景追加6字节时间标签
	if len(data) >= 10 {
		copy(s.Time[:], data[4:10])
	}
	return s, nil
}

// BuildSystemStatus 构建系统状态信息对象（返回字节切片）
func BuildSystemStatus(s *SystemStatus) []byte {
	buf := make([]byte, 4)
	buf[0] = s.SystemType
	buf[1] = s.SystemAddress
	binary.LittleEndian.PutUint16(buf[2:4], s.StatusData)
	return buf
}

// ComponentStatus 部件运行状态（8.2.1.2）— 40 字节
// 系统类型(1B) + 系统地址(1B) + 部件类型(1B) + 部件地址(4B) + 运行状态(2B) + 部件说明(31B, GB18030)
// 部分设备实现：第3字节为"保留"而非"部件类型"——按文档仍以ComponentType表示
type ComponentStatus struct {
	SystemType    uint8     // 系统类型
	SystemAddress    uint8     // 系统地址
	ComponentType uint8     // 部件类型（或保留位）
	ComponentAddr [4]byte   // 部件地址（低字节在前）
	RunStatus     uint16    // 运行状态
	Description   [31]byte  // 部件说明（GB18030，0填充）
	Time          TimeLabel // 时间标签（部分场景下追加）
}

// ComponentStatusLen 部件运行状态信息对象固定字节数
const ComponentStatusLen = 40

// ParseComponentStatus 解析部件运行状态
func ParseComponentStatus(data []byte) (*ComponentStatus, error) {
	if len(data) < ComponentStatusLen {
		return nil, fmt.Errorf("gb26875: ComponentStatus needs %d bytes, got %d",
			ComponentStatusLen, len(data))
	}
	c := &ComponentStatus{
		SystemType:    data[0],
		SystemAddress:    data[1],
		ComponentType: data[2],
		RunStatus:     binary.LittleEndian.Uint16(data[7:9]),
	}
	copy(c.ComponentAddr[:], data[3:7])
	copy(c.Description[:], data[9:40])
	// 部分场景在40字节后追加6字节时间标签
	if len(data) >= ComponentStatusLen+6 {
		copy(c.Time[:], data[ComponentStatusLen:ComponentStatusLen+6])
	}
	return c, nil
}

// BuildComponentStatus 构建部件运行状态信息对象
func BuildComponentStatus(c *ComponentStatus) []byte {
	buf := make([]byte, ComponentStatusLen)
	buf[0] = c.SystemType
	buf[1] = c.SystemAddress
	buf[2] = c.ComponentType
	copy(buf[3:7], c.ComponentAddr[:])
	binary.LittleEndian.PutUint16(buf[7:9], c.RunStatus)
	copy(buf[9:40], c.Description[:])
	return buf
}

// ComponentAnalog 部件模拟量值（8.2.1.3）— 10 字节
// 系统类型(1B) + 系统地址(1B) + 部件类型(1B) + 部件地址(4B) + 模拟量类型(1B) + 模拟量值(2B, 有符号)
type ComponentAnalog struct {
	SystemType    uint8    // 系统类型
	SystemAddress    uint8    // 系统地址
	ComponentType uint8    // 部件类型
	ComponentAddr [4]byte  // 部件地址
	AnalogType    uint8    // 模拟量类型（表6）
	AnalogValue   int16    // 模拟量值（有符号）
}

// ComponentAnalogLen 部件模拟量值固定字节数
const ComponentAnalogLen = 10

// ParseComponentAnalog 解析部件模拟量值
func ParseComponentAnalog(data []byte) (*ComponentAnalog, error) {
	if len(data) < ComponentAnalogLen {
		return nil, fmt.Errorf("gb26875: ComponentAnalog needs %d bytes, got %d",
			ComponentAnalogLen, len(data))
	}
	ca := &ComponentAnalog{
		SystemType:    data[0],
		SystemAddress: data[1],
		ComponentType: data[2],
		AnalogType:    data[7],
		AnalogValue:   int16(binary.LittleEndian.Uint16(data[8:10])),
	}
	copy(ca.ComponentAddr[:], data[3:7])
	return ca, nil
}

// BuildComponentAnalog 构建部件模拟量值信息对象
func BuildComponentAnalog(c *ComponentAnalog) []byte {
	buf := make([]byte, ComponentAnalogLen)
	buf[0] = c.SystemType
	buf[1] = c.SystemAddress
	buf[2] = c.ComponentType
	copy(buf[3:7], c.ComponentAddr[:])
	buf[7] = c.AnalogType
	binary.LittleEndian.PutUint16(buf[8:10], uint16(c.AnalogValue))
	return buf
}

// ScaledValue 返回工程量（原始值 * 最小计量单元）
func (c *ComponentAnalog) ScaledValue() float64 {
	scale, ok := AnalogScale[c.AnalogType]
	if !ok {
		scale = 1.0
	}
	return float64(c.AnalogValue) * scale
}

// OperationInfo 操作信息（8.2.1.4）— 4 字节
// 系统类型(1B) + 系统地址(1B) + 操作员编号(1B) + 操作代码(1B)
type OperationInfo struct {
	SystemType  uint8 // 系统类型
	SystemAddress  uint8 // 系统地址
	OperatorID  uint8 // 操作员编号
	OpCode      uint8 // 操作代码
}

// OperationInfoLen 操作信息固定字节数
const OperationInfoLen = 4

// ParseOperationInfo 解析操作信息
func ParseOperationInfo(data []byte) (*OperationInfo, error) {
	if len(data) < OperationInfoLen {
		return nil, fmt.Errorf("gb26875: OperationInfo needs %d bytes, got %d",
			OperationInfoLen, len(data))
	}
	return &OperationInfo{
		SystemType: data[0],
		SystemAddress: data[1],
		OperatorID: data[2],
		OpCode:     data[3],
	}, nil
}

// BuildOperationInfo 构建操作信息
func BuildOperationInfo(o *OperationInfo) []byte {
	return []byte{o.SystemType, o.SystemAddress, o.OperatorID, o.OpCode}
}

// SoftwareVersion 软件版本（8.2.1.5）— 4 字节
// 系统类型(1B) + 系统地址(1B) + 主版本号(1B) + 次版本号(1B)
type SoftwareVersion struct {
	SystemType   uint8 // 系统类型
	SystemAddress   uint8 // 系统地址
	MajorVersion uint8 // 主版本号
	MinorVersion uint8 // 次版本号
}

// SoftwareVersionLen 软件版本固定字节数
const SoftwareVersionLen = 4

// ParseSoftwareVersion 解析软件版本
func ParseSoftwareVersion(data []byte) (*SoftwareVersion, error) {
	if len(data) < SoftwareVersionLen {
		return nil, fmt.Errorf("gb26875: SoftwareVersion needs %d bytes, got %d",
			SoftwareVersionLen, len(data))
	}
	return &SoftwareVersion{
		SystemType:   data[0],
		SystemAddress:   data[1],
		MajorVersion: data[2],
		MinorVersion: data[3],
	}, nil
}

// BuildSoftwareVersion 构建软件版本
func BuildSoftwareVersion(s *SoftwareVersion) []byte {
	return []byte{s.SystemType, s.SystemAddress, s.MajorVersion, s.MinorVersion}
}

// TimeSync 时钟同步信息（对应类型90 同步传输装置时钟）
// 部分实现直接使用6字节BCD时间标签
type TimeSync struct {
	Time TimeLabel // 同步的目标时间
}

// ParseTimeSync 解析时钟同步信息对象
func ParseTimeSync(data []byte) (*TimeSync, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("gb26875: TimeSync needs 6 bytes, got %d", len(data))
	}
	ts := &TimeSync{}
	copy(ts.Time[:], data[:6])
	return ts, nil
}

// BuildTimeSync 构建时钟同步信息对象
func BuildTimeSync(t TimeLabel) []byte {
	buf := make([]byte, 6)
	copy(buf, t[:])
	return buf
}

// ── 部件地址编码解析（6种格式）─────────────────────────────────────

// AddressInfo 部件地址解析后的可读表示
type AddressInfo struct {
	Circuit uint16 // 回路号
	Addr    uint16 // 地址号
	Raw     uint32 // 原始4字节地址（小端转整数）
}

// ParseComponentAddr 按指定编码方式解析部件地址
// format: 地址编码方式（1~6）
func ParseComponentAddr(addr [4]byte, format uint8) AddressInfo {
	raw := binary.LittleEndian.Uint32(addr[:])
	info := AddressInfo{Raw: raw}

	switch format {
	case AddrFormatCircuitAddr, AddrFormatCircuitAddr2:
		// 部件地址0+1拼成回路号，部件地址2+3拼成地址号
		info.Circuit = uint16(addr[0]) + uint16(addr[1])*256
		info.Addr = uint16(addr[2]) + uint16(addr[3])*256
	case AddrFormatSingleNumber, AddrFormatPointNumber:
		// 4字节解析为1个10进制地址号
		info.Addr = uint16(raw)
	default:
		// 其它格式暂以原始值填充
		info.Addr = uint16(raw)
	}

	return info
}

// StringComponentAddr 返回部件地址的字符串表示（默认按格式1/3回路+地址形式）
func StringComponentAddr(addr [4]byte, format uint8) string {
	info := ParseComponentAddr(addr, format)
	switch format {
	case AddrFormatCircuitAddr, AddrFormatCircuitAddr2:
		return fmt.Sprintf("circuit=%d,addr=%d", info.Circuit, info.Addr)
	default:
		return fmt.Sprintf("addr=%d", info.Addr)
	}
}