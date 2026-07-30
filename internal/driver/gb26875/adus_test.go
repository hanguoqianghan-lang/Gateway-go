package gb26875

import (
	"encoding/hex"
	"testing"
)

func h2b2(s string) []byte {
	c := ""
	for _, ch := range s {
		if ch != ' ' {
			c += string(ch)
		}
	}
	b, _ := hex.DecodeString(c)
	return b
}

// TestParseADU_Header ADU 头部解析
func TestParseADU_Header(t *testing.T) {
	// 案例1时间同步ADU: 19 01 01 00
	// Type=0x19(25 上传传输装置软件版本), 数目=1, 信息体2字节
	data := h2b2("19010100")
	a, err := ParseADU(data)
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != 25 {
		t.Errorf("Type: want 25, got %d", a.Type)
	}
	if a.ObjectCount != 1 {
		t.Errorf("ObjectCount: want 1, got %d", a.ObjectCount)
	}
	if len(a.Objects) != 2 {
		t.Errorf("Objects len: want 2, got %d", len(a.Objects))
	}
}

func TestBuildADU(t *testing.T) {
	obj := []byte{0x01, 0x00}
	adu := BuildADU(25, obj)
	if adu[0] != 25 {
		t.Errorf("Type: want 25, got %d", adu[0])
	}
	if adu[1] != 1 {
		t.Errorf("Count: want 1, got %d", adu[1])
	}
	if adu[2] != 0x01 || adu[3] != 0x00 {
		t.Errorf("Objects wrong: % X", adu[2:])
	}
}

// 系统状态（4字节）解析/构建往返测试
func TestSystemStatusRoundtrip(t *testing.T) {
	s := &SystemStatus{
		SystemType: SysTypeFireAlarm,
		SystemAddress: 3,
		StatusData: 0x0101,
	}
	buf := BuildSystemStatus(s)
	if len(buf) != 4 {
		t.Fatalf("len: want 4, got %d", len(buf))
	}
	s2, err := ParseSystemStatus(buf)
	if err != nil {
		t.Fatal(err)
	}
	if s2.SystemType != s.SystemType {
		t.Errorf("SystemType mismatch")
	}
	if s2.SystemAddress != s.SystemAddress {
		t.Errorf("SystemAddress mismatch")
	}
	if s2.StatusData != s.StatusData {
		t.Errorf("StatusData mismatch")
	}
}

// 部件运行状态（40字节）解析/构建往返测试
func TestComponentStatusRoundtrip(t *testing.T) {
	c := &ComponentStatus{
		SystemType:    SysTypeFireAlarm,
		SystemAddress:    3,
		ComponentType: 0,
		ComponentAddr: [4]byte{0x50, 0x01, 0x01, 0x00},
		RunStatus:     0x0005,
	}
	// 设一个简单的描述
	copy(c.Description[:], []byte("Test"))

	buf := BuildComponentStatus(c)
	if len(buf) != 40 {
		t.Fatalf("len: want 40, got %d", len(buf))
	}

	c2, err := ParseComponentStatus(buf)
	if err != nil {
		t.Fatal(err)
	}
	if c2.SystemType != c.SystemType {
		t.Errorf("SystemType mismatch")
	}
	if c2.SystemAddress != c.SystemAddress {
		t.Errorf("SystemAddress mismatch")
	}
	if c2.ComponentType != c.ComponentType {
		t.Errorf("ComponentType mismatch")
	}
	if c2.ComponentAddr != c.ComponentAddr {
		t.Errorf("ComponentAddr mismatch: want % X, got % X", c.ComponentAddr, c2.ComponentAddr)
	}
	if c2.RunStatus != c.RunStatus {
		t.Errorf("RunStatus mismatch: want 0x%04X, got 0x%04X", c.RunStatus, c2.RunStatus)
	}
}

// 部件模拟量值（10字节）解析/构建往返测试
func TestComponentAnalogRoundtrip(t *testing.T) {
	c := &ComponentAnalog{
		SystemType:    SysTypeFireAlarm,
		SystemAddress:    1,
		ComponentType: CompTypeHeatDetector,
		ComponentAddr: [4]byte{0x01, 0x00, 0x01, 0x00},
		AnalogType:    AnalogTypeTemperature,
		AnalogValue:   356, // 35.6 ℃
	}
	buf := BuildComponentAnalog(c)
	if len(buf) != 10 {
		t.Fatalf("len: want 10, got %d", len(buf))
	}
	c2, err := ParseComponentAnalog(buf)
	if err != nil {
		t.Fatal(err)
	}
	if c2.AnalogType != c.AnalogType {
		t.Errorf("AnalogType mismatch")
	}
	if c2.AnalogValue != c.AnalogValue {
		t.Errorf("AnalogValue mismatch")
	}
	// 工程量 = 356 * 0.1 = 35.6
	scaled := c2.ScaledValue()
	if scaled < 35.5 || scaled > 35.7 {
		t.Errorf("ScaledValue: want ~35.6, got %f", scaled)
	}
}

// 负模拟量值（有符号）
func TestComponentAnalogNegative(t *testing.T) {
	c := &ComponentAnalog{
		SystemType:    SysTypeFireAlarm,
		SystemAddress:    1,
		ComponentType: CompTypeHeatDetector,
		AnalogType:    AnalogTypeTemperature,
		AnalogValue:   -100, // -10.0 ℃
	}
	buf := BuildComponentAnalog(c)
	c2, err := ParseComponentAnalog(buf)
	if err != nil {
		t.Fatal(err)
	}
	if c2.AnalogValue != -100 {
		t.Errorf("AnalogValue: want -100, got %d", c2.AnalogValue)
	}
	scaled := c2.ScaledValue()
	if scaled > -9.9 || scaled < -10.1 {
		t.Errorf("ScaledValue: want ~-10.0, got %f", scaled)
	}
}

// 操作信息（4字节）解析/构建
func TestOperationInfoRoundtrip(t *testing.T) {
	o := &OperationInfo{
		SystemType: SysTypeFireAlarm,
		SystemAddress: 1,
		OperatorID: 5,
		OpCode:     2,
	}
	buf := BuildOperationInfo(o)
	if len(buf) != 4 {
		t.Fatalf("len: want 4, got %d", len(buf))
	}
	o2, err := ParseOperationInfo(buf)
	if err != nil {
		t.Fatal(err)
	}
	if *o2 != *o {
		t.Errorf("mismatch: %+v vs %+v", o2, o)
	}
}

// 软件版本（4字节）解析/构建
func TestSoftwareVersionRoundtrip(t *testing.T) {
	s := &SoftwareVersion{
		SystemType:   SysTypeFireAlarm,
		SystemAddress:   1,
		MajorVersion: 2,
		MinorVersion: 5,
	}
	buf := BuildSoftwareVersion(s)
	if len(buf) != 4 {
		t.Fatalf("len: want 4, got %d", len(buf))
	}
	s2, err := ParseSoftwareVersion(buf)
	if err != nil {
		t.Fatal(err)
	}
	if *s2 != *s {
		t.Errorf("mismatch")
	}
}

// 时间同步
func TestTimeSyncRoundtrip(t *testing.T) {
	tl := FormatTimeLabel(21, 9, 10, 13, 17, 34)
	buf := BuildTimeSync(tl)
	if len(buf) != 6 {
		t.Fatalf("len: want 6, got %d", len(buf))
	}
	ts, err := ParseTimeSync(buf)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Time != tl {
		t.Errorf("Time mismatch: want % X, got % X", tl, ts.Time)
	}
}

// 部件地址编码格式1: 部件地址0+1=回路, 2+3=地址号
func TestParseComponentAddr_Format1(t *testing.T) {
	// 案例3: 部件地址 50 01 01 00
	// 电路号 = 0x50 + 0x01*256 = 80+256 = 336
	// 地址号 = 0x01 + 0x00*256 = 1
	addr := [4]byte{0x50, 0x01, 0x01, 0x00}
	info := ParseComponentAddr(addr, AddrFormatCircuitAddr)
	if info.Circuit != 336 {
		t.Errorf("Circuit: want 336, got %d", info.Circuit)
	}
	if info.Addr != 1 {
		t.Errorf("Addr: want 1, got %d", info.Addr)
	}
}

// 部件地址编码格式2: 4字节作为单一10进制地址号
func TestParseComponentAddr_Format2(t *testing.T) {
	addr := [4]byte{0x01, 0x00, 0x00, 0x00} // 1
	info := ParseComponentAddr(addr, AddrFormatSingleNumber)
	if info.Addr != 1 {
		t.Errorf("Addr: want 1, got %d", info.Addr)
	}
}

// 错误输入：各种短数据
func TestParseErrors(t *testing.T) {
	_, err := ParseSystemStatus([]byte{0x01, 0x02})
	if err == nil {
		t.Error("SystemStatus should fail")
	}
	_, err = ParseComponentStatus(make([]byte, 30))
	if err == nil {
		t.Error("ComponentStatus should fail")
	}
	_, err = ParseComponentAnalog(make([]byte, 5))
	if err == nil {
		t.Error("ComponentAnalog should fail")
	}
	_, err = ParseOperationInfo(make([]byte, 3))
	if err == nil {
		t.Error("OperationInfo should fail")
	}
	_, err = ParseSoftwareVersion(make([]byte, 3))
	if err == nil {
		t.Error("SoftwareVersion should fail")
	}
	_, err = ParseTimeSync(make([]byte, 5))
	if err == nil {
		t.Error("TimeSync should fail")
	}
}

// 模拟量缩放表
func TestAnalogScaleTable(t *testing.T) {
	tests := []struct {
		typ   uint8
		scale float64
		unit  string
	}{
		{AnalogTypeTemperature, 0.1, "℃"},
		{AnalogTypePressureMPa, 0.1, "MPa"},
		{AnalogTypeGasConc, 0.1, "%LEL"},
		{AnalogTypeVoltage, 0.1, "V"},
		{AnalogTypeCurrent, 0.1, "A"},
	}
	for _, tt := range tests {
		if AnalogScale[tt.typ] != tt.scale {
			t.Errorf("Type %d: scale want %v, got %v", tt.typ, tt.scale, AnalogScale[tt.typ])
		}
		if AnalogUnit[tt.typ] != tt.unit {
			t.Errorf("Type %d: unit want %s, got %s", tt.typ, tt.unit, AnalogUnit[tt.typ])
		}
	}
}

// 验证 ComponentStatus 字段长度（按案例3分析：40 字节）
func TestComponentStatusFieldOffset(t *testing.T) {
	// 案例3的信息对象体: 01 03 00 50 01 01 00 05 00 05 [31字节描述] 22 11 0D 0A 09 15
	//                       ^系统类型 ^系统地址 ^部件类型 ^部件地址(4) ^运行状态(2) ^描述(31)
	// 系统类型=01(火灾报警), 系统地址=03, 部件类型/保留=00
	// 部件地址=50 01 01 00, 运行状态=05 00
	// 描述从byte[9]开始
	// 时间标签追加在40字节之后

	// 构造40字节数据 + 6字节时间
	info := make([]byte, 46)
	info[0] = 0x01 // 系统类型
	info[1] = 0x03 // 系统地址
	info[2] = 0x00 // 部件类型/保留
	info[3] = 0x50 // 部件地址0
	info[4] = 0x01 // 部件地址1
	info[5] = 0x01 // 部件地址2
	info[6] = 0x00 // 部件地址3
	info[7] = 0x05 // 运行状态 low
	info[8] = 0x00 // 运行状态 high
	// 描述部分省略
	// 时间标签
	info[40] = 0x22 // 秒
	info[41] = 0x11 // 分
	info[42] = 0x0D // 时
	info[43] = 0x0A // 日
	info[44] = 0x09 // 月
	info[45] = 0x15 // 年

	c, err := ParseComponentStatus(info)
	if err != nil {
		t.Fatal(err)
	}
	if c.SystemType != 1 {
		t.Errorf("SystemType: want 1, got %d", c.SystemType)
	}
	if c.SystemAddress != 3 {
		t.Errorf("SystemAddress: want 3, got %d", c.SystemAddress)
	}
	if c.ComponentAddr != [4]byte{0x50, 0x01, 0x01, 0x00} {
		t.Errorf("ComponentAddr wrong: % X", c.ComponentAddr)
	}
	if c.RunStatus != 0x0005 {
		t.Errorf("RunStatus: want 0x0005, got 0x%04X", c.RunStatus)
	}
	// 时间标签应解析出来
	if c.Time[0] != 0x22 || c.Time[5] != 0x15 {
		t.Errorf("Time not parsed: % X", c.Time)
	}
}