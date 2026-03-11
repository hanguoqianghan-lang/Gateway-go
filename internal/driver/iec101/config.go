// internal/driver/iec101/config.go - IEC 60870-5-101 配置定义
package iec101

import (
	"time"
)

// Config IEC101 驱动配置
type Config struct {
	// 驱动标识
	ID   string // 驱动 ID
	Name string // 驱动名称

	// 串口配置
	SerialPort     string        // 串口设备路径，如 "/dev/ttyUSB0" 或 "COM1"
	BaudRate       int           // 波特率，如 9600, 19200, 38400
	DataBits       int           // 数据位，通常为 8
	StopBits       int           // 停止位，1 或 2
	Parity         string        // 校验位："none", "even", "odd"（IEC101 通常使用 even）
	CharTimeout    time.Duration // 字符间超时（防止报文断裂）
	FrameTimeout   time.Duration // 帧超时
	ResponseTimeout time.Duration // 响应超时

	// 协议参数
	CommonAddress   uint8  // 公共地址（ASDU.CommonAddr）
	LinkAddress     uint8  // 链路地址
	BalancedMode    bool   // 传输模式：true=平衡模式，false=非平衡模式
	MaxRetry        int    // 最大重试次数
	RetryInterval   time.Duration // 重试间隔

	// 采集参数
	GIInterval      time.Duration // 总召唤间隔（非平衡模式）
	PollInterval    time.Duration // 轮询间隔
	MaxGap          int           // 最大地址空洞（用于合并召唤请求）

	// 点表配置
	Points []PointConfig // 点表配置列表
}

// PointConfig 单个测点配置
type PointConfig struct {
	// 点标识
	Name string // 测点名称

	// IEC101 地址（CA + IOA）
	CA  uint8  // 公共地址（Common Address）
	IOA uint16 // 信息对象地址（Information Object Address）

	// 数据类型
	TypeID uint8 // ASDU 类型标识

	// 转换参数
	Scale   float64 // 缩放因子
	Offset  float64 // 偏移量

	// 采集参数
	Interval int // 采集间隔（毫秒），0 表示使用默认间隔

	// 死区过滤
	DeadbandValue float64 // 死区阈值
	DeadbandType  DeadbandType // 死区类型

	// 运行时缓存
	lastValue     float64
	lastTimestamp int64
}

// DeadbandType 死区类型
type DeadbandType int

const (
	DeadbandAbsolute DeadbandType = iota // 绝对值死区
	DeadbandPercent                     // 百分比死区
)

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		BaudRate:        9600,
		DataBits:        8,
		StopBits:        1,
		Parity:          "even", // IEC101 标准使用偶校验
		CharTimeout:     50 * time.Millisecond,
		FrameTimeout:    200 * time.Millisecond,
		ResponseTimeout: 1 * time.Second,
		CommonAddress:   1,
		LinkAddress:     1,
		BalancedMode:    false, // 默认非平衡模式
		MaxRetry:        3,
		RetryInterval:   1 * time.Second,
		GIInterval:      15 * time.Minute,
		PollInterval:    1 * time.Second,
		MaxGap:          10,
	}
}

// Validate 校验配置
func (c *Config) Validate() error {
	if c.SerialPort == "" {
		return ErrInvalidConfig("serial port cannot be empty")
	}
	if c.BaudRate <= 0 {
		return ErrInvalidConfig("baud rate must be positive")
	}
	if c.DataBits < 5 || c.DataBits > 8 {
		return ErrInvalidConfig("data bits must be in range [5, 8]")
	}
	if c.StopBits < 1 || c.StopBits > 2 {
		return ErrInvalidConfig("stop bits must be 1 or 2")
	}
	if c.Parity != "none" && c.Parity != "even" && c.Parity != "odd" {
		return ErrInvalidConfig("parity must be 'none', 'even', or 'odd'")
	}
	if c.CommonAddress == 0 {
		return ErrInvalidConfig("common address cannot be 0")
	}
	return nil
}

// ConfigError 配置错误
type ConfigError string

func (e ConfigError) Error() string {
	return string(e)
}

func ErrInvalidConfig(msg string) error {
	return ConfigError("iec101: invalid config: " + msg)
}
