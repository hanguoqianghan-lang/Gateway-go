// internal/driver/modbus/config.go - Modbus TCP 驱动配置定义
package modbus

import "time"

// RegisterType 寄存器功能码类型
type RegisterType uint8

const (
	// Coil 线圈（功能码 0x01 读）
	Coil RegisterType = iota
	// DiscreteInput 离散输入（功能码 0x02 读）
	DiscreteInput
	// HoldingRegister 保持寄存器（功能码 0x03 读），最常用
	HoldingRegister
	// InputRegister 输入寄存器（功能码 0x04 读）
	InputRegister
)

// DataType 寄存器数据解析类型
type DataType uint8

const (
	// Uint16 单个 16 位无符号整数（1 个寄存器）
	Uint16 DataType = iota
	// Int16 单个 16 位有符号整数（1 个寄存器）
	Int16
	// Uint32 32 位无符号整数（2 个寄存器，高字在前）
	Uint32
	// Int32 32 位有符号整数（2 个寄存器，高字在前）
	Int32
	// Float32 IEEE 754 单精度浮点（2 个寄存器，高字在前）
	Float32
	// Float64 IEEE 754 双精度浮点（4 个寄存器，高字在前）
	Float64
	// Bool 布尔值（从 Coil/DiscreteInput 读取，或单寄存器最低位）
	Bool
)

// registerWidth 返回对应 DataType 占用的寄存器个数（16bit 为单位）。
func registerWidth(dt DataType) uint16 {
	switch dt {
	case Float64:
		return 4
	case Uint32, Int32, Float32:
		return 2
	default:
		return 1
	}
}

// ─── 配置结构体 ────────────────────────────────────────────────────────────────

// PointConfig 单个采集点配置
type PointConfig struct {
	// Name 测点名称，对应 PointData.ID 的最后一段，格式：<设备ID>/modbus/<Name>
	Name string `json:"name" yaml:"name"`
	// Address Modbus 寄存器起始地址（0-based）
	Address uint16 `json:"address" yaml:"address"`
	// Type 寄存器类型，默认 HoldingRegister
	Type RegisterType `json:"type" yaml:"type"`
	// DataType 数据解析方式，默认 Float32
	DataType DataType `json:"data_type" yaml:"data_type"`
	// ByteOrder 字节序: "big"(大端,AB CD) 或 "little"(小端,CD AB)，默认 "big"
	// 扩展支持: ABCD, CDAB, BADC, DCBA 四种浮点数字节序
	ByteOrder string `json:"byte_order" yaml:"byte_order"`
	// BitPos 位提取位置(0-15)，如果设置则从uint16中提取指定位，输出bool值
	BitPos int `json:"bit_pos" yaml:"bit_pos"`
	// Scale 线性缩放系数（raw * Scale + Offset），为 0 时不缩放
	Scale float64 `json:"scale" yaml:"scale"`
	// Offset 线性偏移
	Offset float64 `json:"offset" yaml:"offset"`
}

// SlaveConfig 单个 Modbus TCP Slave（设备）配置
type SlaveConfig struct {
	// ID 设备唯一标识，用于构造 PointData.ID
	ID string `json:"id" yaml:"id"`
	// Host Slave IP 地址或主机名
	Host string `json:"host" yaml:"host"`
	// Port Modbus TCP 端口，默认 502
	Port int `json:"port" yaml:"port"`
	// UnitID Modbus 单元 ID（从站地址），默认 1
	UnitID uint8 `json:"unit_id" yaml:"unit_id"`
	// PollInterval 轮询间隔，默认 1s
	PollInterval time.Duration `json:"poll_interval" yaml:"poll_interval"`
	// Timeout 单次请求超时，默认 3s
	Timeout time.Duration `json:"timeout" yaml:"timeout"`
	// MaxRetryInterval 指数退避最大间隔，默认 60s
	MaxRetryInterval time.Duration `json:"max_retry_interval" yaml:"max_retry_interval"`
	// AddressOffset 全局地址偏移量，实际地址 = CSV地址 + 此值，默认 0
	AddressOffset uint16 `json:"address_offset" yaml:"address_offset"`
	// MaxRegistersPerRequest 单次请求最大读取寄存器数，默认 100
	MaxRegistersPerRequest uint16 `json:"max_registers_per_request" yaml:"max_registers_per_request"`
	// Points 该设备上需要采集的测点列表
	Points []PointConfig `json:"points" yaml:"points"`
}

// ModbusConfig 整个 Modbus 驱动的顶层配置
type ModbusConfig struct {
	// Slaves 所有 Slave 设备列表
	Slaves []SlaveConfig `json:"slaves" yaml:"slaves"`
}

// fillDefaults 为未设置的字段填充合理默认值
func (s *SlaveConfig) fillDefaults() {
	if s.Port == 0 {
		s.Port = 502
	}
	if s.UnitID == 0 {
		s.UnitID = 1
	}
	if s.PollInterval == 0 {
		s.PollInterval = time.Second
	}
	if s.Timeout == 0 {
		s.Timeout = 3 * time.Second
	}
	if s.MaxRetryInterval == 0 {
		s.MaxRetryInterval = 60 * time.Second
	}
	if s.MaxRegistersPerRequest == 0 {
		s.MaxRegistersPerRequest = 100
	}
	for i := range s.Points {
		if s.Points[i].Scale == 0 {
			s.Points[i].Scale = 1.0
		}
		if s.Points[i].ByteOrder == "" {
			s.Points[i].ByteOrder = "big"
		}
		// BitPos 默认 -1 表示不启用位提取
		if s.Points[i].BitPos == 0 {
			s.Points[i].BitPos = -1
		}
	}
}
