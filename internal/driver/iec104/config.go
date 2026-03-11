// internal/driver/iec104/config.go - IEC104 驱动配置定义
package iec104

import (
	"time"

	"github.com/gateway/gateway/config"
)

// TypeID IEC104 类型标识符
type TypeID uint8

const (
	// 遥信类型
	TypeM_SP_NA_1 TypeID = 1  // 单点遥信
	TypeM_DP_NA_1 TypeID = 3  // 双点遥信
	TypeM_ST_NA_1 TypeID = 5  // 步位置信息
	TypeM_BO_NA_1 TypeID = 7  // 32比特串

	// 遥测类型
	TypeM_ME_NA_1 TypeID = 9  // 归一化值
	TypeM_ME_NB_1 TypeID = 11 // 标度化值
	TypeM_ME_NC_1 TypeID = 13 // 短浮点数
	TypeM_ME_ND_1 TypeID = 21 // 不带品质归一化值

	// 累计量类型
	TypeM_IT_NA_1 TypeID = 15 // 累计量
	TypeM_IT_TA_1 TypeID = 16 // 带时标累计量

	// 32位比特串
	TypeM_PS_NA_1 TypeID = 20 // 32位比特串
)

// String 返回类型标识符的字符串表示
func (t TypeID) String() string {
	switch t {
	case TypeM_SP_NA_1:
		return "M_SP_NA_1"
	case TypeM_DP_NA_1:
		return "M_DP_NA_1"
	case TypeM_ST_NA_1:
		return "M_ST_NA_1"
	case TypeM_BO_NA_1:
		return "M_BO_NA_1"
	case TypeM_ME_NA_1:
		return "M_ME_NA_1"
	case TypeM_ME_NB_1:
		return "M_ME_NB_1"
	case TypeM_ME_NC_1:
		return "M_ME_NC_1"
	case TypeM_ME_ND_1:
		return "M_ME_ND_1"
	case TypeM_IT_NA_1:
		return "M_IT_NA_1"
	case TypeM_IT_TA_1:
		return "M_IT_TA_1"
	case TypeM_PS_NA_1:
		return "M_PS_NA_1"
	default:
		return "UNKNOWN"
	}
}

// ParseTypeID 从字符串解析类型标识符
func ParseTypeID(s string) TypeID {
	switch s {
	case "M_SP_NA_1":
		return TypeM_SP_NA_1
	case "M_DP_NA_1":
		return TypeM_DP_NA_1
	case "M_ST_NA_1":
		return TypeM_ST_NA_1
	case "M_BO_NA_1":
		return TypeM_BO_NA_1
	case "M_ME_NA_1":
		return TypeM_ME_NA_1
	case "M_ME_NB_1":
		return TypeM_ME_NB_1
	case "M_ME_NC_1":
		return TypeM_ME_NC_1
	case "M_ME_ND_1":
		return TypeM_ME_ND_1
	case "M_IT_NA_1":
		return TypeM_IT_NA_1
	case "M_IT_TA_1":
		return TypeM_IT_TA_1
	case "M_PS_NA_1":
		return TypeM_PS_NA_1
	default:
		return 0
	}
}

// IsTelemetry 判断是否为遥测类型
func (t TypeID) IsTelemetry() bool {
	switch t {
	case TypeM_ME_NA_1, TypeM_ME_NB_1, TypeM_ME_NC_1, TypeM_ME_ND_1:
		return true
	default:
		return false
	}
}

// IsSignal 判断是否为遥信类型
func (t TypeID) IsSignal() bool {
	switch t {
	case TypeM_SP_NA_1, TypeM_DP_NA_1, TypeM_ST_NA_1, TypeM_BO_NA_1:
		return true
	default:
		return false
	}
}

// IsCounter 判断是否为累计量类型
func (t TypeID) IsCounter() bool {
	switch t {
	case TypeM_IT_NA_1, TypeM_IT_TA_1:
		return true
	default:
		return false
	}
}

// 死区类型常量
const (
	DeadbandAbsolute = "absolute" // 绝对值死区
	DeadbandPercent  = "percent"  // 百分比死区
)

// PointConfig 单个 IEC104 测点配置
type PointConfig struct {
	// Name 测点名称，对应 PointData.ID 的最后一段
	Name string
	// IOA 信息对象地址（Information Object Address）
	IOA uint32
	// CA 公共地址（ASDU Common Address），0 表示使用驱动默认值
	CA uint8
	// TypeID 类型标识符
	TypeID TypeID
	// Scale 线性缩放系数（raw * Scale + Offset）
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

// Config IEC104 驱动配置（与 driver.go 中的 Driver 结构体配合使用）
type Config struct {
	// Name 设备唯一标识，用于构造 PointData.ID
	Name string
	// Host 主站 IP 地址
	Host string
	// Port IEC104 端口，默认 2404
	Port int
	// CommonAddress 公共地址（ASDU Common Address），默认 1
	CommonAddress uint8
	// OriginatorAddress 源发地址，默认 0
	OriginatorAddress uint8
	// Timeout ASDU 超时时间，默认 10s
	Timeout time.Duration
	// TestInterval 心跳测试间隔，默认 20s
	TestInterval time.Duration
	// ReconnectInterval 重连间隔，默认 5s
	ReconnectInterval time.Duration
	// MaxRetryInterval 指数退避最大间隔，默认 60s
	MaxRetryInterval time.Duration
	// GIInterval 总召唤间隔，默认 0（不主动召唤）
	GIInterval time.Duration
	// GIStaggeredDelay GI 防风暴随机延迟上限
	GIStaggeredDelay time.Duration
	// ClockSyncInterval 时钟同步间隔，默认 0（不同步）
	ClockSyncInterval time.Duration
	// EnableSystemMetrics 是否启用系统测点
	EnableSystemMetrics bool
	// Points 该设备上需要采集的测点列表
	Points []PointConfig
}

// fillDefaults 为未设置的字段填充合理默认值
func (c *Config) fillDefaults() {
	if c.Port == 0 {
		c.Port = 2404
	}
	if c.CommonAddress == 0 {
		c.CommonAddress = 1
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	if c.TestInterval == 0 {
		c.TestInterval = 20 * time.Second
	}
	if c.ReconnectInterval == 0 {
		c.ReconnectInterval = 5 * time.Second
	}
	if c.MaxRetryInterval == 0 {
		c.MaxRetryInterval = 60 * time.Second
	}
	// GIInterval 默认 15 分钟（工业标准）
	// 设为 -1 可禁用定时总召唤
	if c.GIInterval == 0 {
		c.GIInterval = 15 * time.Minute
	}
	if c.GIStaggeredDelay == 0 {
		c.GIStaggeredDelay = 5 * time.Second
	}
	// ClockSyncInterval 默认 30 分钟
	if c.ClockSyncInterval == 0 {
		c.ClockSyncInterval = 30 * time.Minute
	}

	// 为每个测点填充默认值
	for i := range c.Points {
		if c.Points[i].Scale == 0 {
			c.Points[i].Scale = 1.0
		}
		if c.Points[i].DeadbandType == "" {
			c.Points[i].DeadbandType = DeadbandAbsolute
		}
		// 如果测点未指定公共地址，使用 Config 的默认值
		if c.Points[i].CA == 0 {
			c.Points[i].CA = c.CommonAddress
		}
	}
}

// NewConfig 从全局配置创建 IEC104 驱动配置
func NewConfig(driverCfg *config.IEC104DriverConfig, driverID string, points []PointConfig) Config {
	cfg := Config{
		Name:            driverID,
		Host:            driverCfg.Host,
		Port:            driverCfg.Port,
		CommonAddress:   driverCfg.CommonAddress,
		Timeout:         driverCfg.Timeout,
		TestInterval:    driverCfg.TestInterval,
		Points:          points,
	}
	cfg.fillDefaults()

	return cfg
}

// PointMap 返回以 IOA 为键的测点映射表
func (c *Config) PointMap() map[uint32]*PointConfig {
	m := make(map[uint32]*PointConfig, len(c.Points))
	for i := range c.Points {
		m[c.Points[i].IOA] = &c.Points[i]
	}
	return m
}
