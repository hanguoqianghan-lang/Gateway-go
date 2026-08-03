// internal/driver/dlt645/config.go - DL/T 645 驱动配置
package dlt645

import (
	"fmt"
	"time"
)

// ProtocolVersion DL/T 645 协议版本
type ProtocolVersion int

const (
	Version1997 ProtocolVersion = 1997
	Version2007 ProtocolVersion = 2007
)

// TransportMode 传输模式
type TransportMode string

const (
	TransportSerial TransportMode = "serial" // 串口模式
	TransportTCP    TransportMode = "tcp"    // TCP 网口模式（通过串口服务器或设备自带网口）
)

// Config DL/T 645 驱动配置
type Config struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// 传输模式：serial 或 tcp
	Transport TransportMode `json:"transport"` // 默认 serial

	// 串口配置（transport=serial 时必填）
	SerialPort string `json:"serial_port"`
	BaudRate    int    `json:"baud_rate"`
	DataBits    int    `json:"data_bits"`
	StopBits    int    `json:"stop_bits"`
	Parity      string `json:"parity"`

	// TCP 配置（transport=tcp 时必填）
	TCPAddr string `json:"tcp_addr"` // 格式: host:port，如 "192.168.1.100:4001"

	// 协议配置
	ProtocolVersion ProtocolVersion `json:"protocol_version"` // 1997 或 2007

	// 前导字节（激活设备）
	UseLeadingByte bool `json:"use_leading_byte"`
	LeadingByteCount int `json:"leading_byte_count"` // 前导字节数量，默认4

	// 采集配置
	PollInterval time.Duration `json:"poll_interval"` // 采集间隔
	QueryIntervalPerPoint time.Duration `json:"query_interval_per_point"` // 每测点间隔（异步模式）

	// 超时配置
	CharTimeout   time.Duration `json:"char_timeout"`    // 字符间超时
	FrameTimeout  time.Duration `json:"frame_timeout"`   // 帧超时
	ResponseTimeout time.Duration `json:"response_timeout"` // 响应超时

	// 重试配置
	MaxRetry      int           `json:"max_retry"`
	RetryInterval time.Duration `json:"retry_interval"`

	// 点表
	Points []PointConfig `json:"points"`

	// 广播地址（用于未配置表号的情况）
	BroadcastAddress string `json:"broadcast_address"` // 默认 "AAAAAAAAAAAA"
}

// PointConfig 点表配置
type PointConfig struct {
	Name           string        `json:"name"`            // 点名
	Address        string        `json:"address"`         // 表号 (12位BCD码，如 "123456789012")
	DataID         string        `json:"data_id"`         // 数据标识 (1997: 4位如 "9010", 2007: 8位如 "00010000")
	Scale          float64       `json:"scale"`           // 缩放系数
	Offset         float64       `json:"offset"`          // 偏移量
	Unit           string        `json:"unit"`            // 单位
	Precision      int           `json:"precision"`       // 精度（小数位数）
	Interval       int           `json:"interval"`        // 采集间隔（秒）
	DeadbandValue  float64       `json:"deadband_value"`  // 死区阈值
	DeadbandType   DeadbandType  `json:"deadband_type"`   // 死区类型

	// 内部使用
	lastValue     float64
	lastTimestamp int64
}

// DeadbandType 死区类型
type DeadbandType int

const (
	DeadbandAbsolute DeadbandType = iota
	DeadbandPercent
)

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Transport:            TransportSerial,
		ProtocolVersion:      Version2007,
		BaudRate:             9600,
		DataBits:             8,
		StopBits:             1,
		Parity:               "even",
		UseLeadingByte:       false,
		LeadingByteCount:     4,
		PollInterval:         1 * time.Second,
		QueryIntervalPerPoint: 100 * time.Millisecond,
		CharTimeout:          50 * time.Millisecond,
		FrameTimeout:         200 * time.Millisecond,
		ResponseTimeout:      1 * time.Second,
		MaxRetry:             3,
		RetryInterval:        1 * time.Second,
		BroadcastAddress:     "AAAAAAAAAAAA",
	}
}

// Validate 验证配置
func (c *Config) Validate() error {
	// 验证传输模式
	if c.Transport != TransportSerial && c.Transport != TransportTCP {
		c.Transport = TransportSerial // 默认串口
	}

	if c.Transport == TransportSerial {
		if c.SerialPort == "" {
			return fmt.Errorf("serial_port is required for serial transport")
		}
	} else if c.Transport == TransportTCP {
		if c.TCPAddr == "" {
			return fmt.Errorf("tcp_addr is required for tcp transport")
		}
	}

	if c.BaudRate <= 0 {
		return fmt.Errorf("invalid baud_rate: %d", c.BaudRate)
	}

	if c.ProtocolVersion != Version1997 && c.ProtocolVersion != Version2007 {
		return fmt.Errorf("invalid protocol_version: %d, must be 1997 or 2007", c.ProtocolVersion)
	}

	if c.ResponseTimeout <= 0 {
		c.ResponseTimeout = 1 * time.Second
	}

	return nil
}

// DataIDLen 返回数据标识字节长度
func (c *Config) DataIDLen() int {
	if c.ProtocolVersion == Version1997 {
		return 2
	}
	return 4
}