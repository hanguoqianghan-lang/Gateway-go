// internal/driver/gb26875/config.go - GB/T 26875.3 驱动配置
package gb26875

import (
	"time"

	"github.com/gateway/gateway/config"
)

// PointConfig 单个 GB/T 26875.3 测点配置
type PointConfig struct {
	// Name 测点名称（用于构造 PointData.ID 的最后一段）
	Name string
	// DeviceAddress 传输装置地址（6字节HEX字符串，低字节在前，例如 "800D00000000"）
	// 留空表示匹配所有装置
	DeviceAddress string
	// MessageType 类型标志（1=系统状态, 2=部件运行状态, 3=部件模拟量, 21=传输装置运行状态等）
	MessageType uint8
	// SystemType 系统类型（0=通用, 1=火灾报警, 10=消防联动, 11=消火栓等）
	SystemType uint8
	// SystemAddress 系统地址（1字节）
	SystemAddress uint8
	// ComponentType 部件类型（仅 MessageType=2/3 时有意义）
	ComponentType uint8
	// ComponentAddr 部件地址（4字节HEX字符串，低字节在前，例如 "50010100"）
	ComponentAddr string
	// AnalogType 模拟量类型（仅 MessageType=3 时有意义）
	AnalogType uint8
	// AddrFormat 部件地址编码格式（1~6，默认1）
	AddrFormat uint8
	// Scale 线性缩放系数
	Scale float64
	// Offset 线性偏移
	Offset float64
	// DeadbandValue 死区阈值（变化超过此值才上报）
	DeadbandValue float64
	// DeadbandType 死区类型：absolute（绝对值）或 percent（百分比）
	DeadbandType string
	// Description 测点描述
	Description string
}

// Config GB/T 26875.3 驱动配置
type Config struct {
	// Name 设备唯一标识
	Name string
	// Host 监听地址（如 0.0.0.0 或 留空）
	Host string
	// Port TCP 监听端口（默认 5001）
	Port int
	// LocalAddress 本机地址（6字节HEX字符串，下行报文的源地址）
	// 默认全0表示由系统自动填入
	LocalAddress string
	// MaxConnections 最大并发传输装置连接数（默认 100）
	MaxConnections int
	// ReadTimeout 接收单帧超时（默认 10s）
	ReadTimeout time.Duration
	// WriteTimeout 发送超时（默认 5s）
	WriteTimeout time.Duration
	// FrameTimeout 相邻字节超时（用于切分帧，默认 200ms）
	FrameTimeout time.Duration
	// ClockSyncInterval 时钟同步周期（默认 0 = 不同步；非 0 时按周期主动同步）
	ClockSyncInterval time.Duration
	// Version 主版本号（固定 1）
	Version uint8
	// UserVersion 用户版本号（自定义）
	UserVersion uint8
	// EnableSystemMetrics 是否启用系统测点（连接状态、计数等）
	EnableSystemMetrics bool
	// ADUBufferSize 接收 ADU 缓冲区大小（默认 5000）
	ADUBufferSize int
	// Points 测点列表
	Points []PointConfig
}

// fillDefaults 为未设置字段填充默认值
func (c *Config) fillDefaults() {
	if c.Host == "" {
		c.Host = "0.0.0.0"
	}
	if c.Port == 0 {
		c.Port = 5001
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = 100
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 10 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 5 * time.Second
	}
	if c.FrameTimeout == 0 {
		c.FrameTimeout = 200 * time.Millisecond
	}
	if c.ADUBufferSize <= 0 {
		c.ADUBufferSize = 5000
	}
	if c.Version == 0 {
		c.Version = 1
	}
	if c.UserVersion == 0 {
		c.UserVersion = 1
	}
	if c.ClockSyncInterval == 0 {
		// 默认不主动时钟同步
		c.ClockSyncInterval = 0
	}

	for i := range c.Points {
		if c.Points[i].Scale == 0 {
			c.Points[i].Scale = 1.0
		}
		if c.Points[i].DeadbandType == "" {
			c.Points[i].DeadbandType = "absolute"
		}
		if c.Points[i].AddrFormat == 0 {
			c.Points[i].AddrFormat = 1
		}
	}
}

// NewConfig 从全局驱动配置构建 GB26875 Config
// 注意：完整实现见 S5 阶段（需要在 config.DriverConfig 添加 GB26875 字段）
func NewConfig(drvCfg *config.DriverConfig, points []PointConfig) Config {
	cfg := Config{
		Name:  drvCfg.Name,
		Points: points,
	}
	cfg.fillDefaults()
	return cfg
}
