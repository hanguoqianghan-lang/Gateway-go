package iec104server

import (
	"errors"
	"fmt"
	"time"
)

// Config IEC104 Server 导出器配置
type Config struct {
	// 网络配置
	ListenAddr     string        `yaml:"listen_addr"`     // 监听地址，如 ":2404"
	MaxConnections int           `yaml:"max_connections"` // 最大连接数，默认 5

	// APDU 配置
	MaxAPDULength uint8 `yaml:"max_apdu_length"` // APDU 最大长度，范围 [1, 253]

	// 协议参数
	CommonAddress uint16 `yaml:"common_address"` // 公共地址（ASDU.CommonAddr）
	COTLocal      uint8  `yaml:"cot_local"`      // 本地 COT，默认 2
	COTRemote     uint8  `yaml:"cot_remote"`     // 远端 COT，默认 1

	// 超时配置
	ConnectTimeout time.Duration `yaml:"connect_timeout"` // 连接超时
	IdleTimeout    time.Duration `yaml:"idle_timeout"`    // 空闲超时
	TestInterval   time.Duration `yaml:"test_interval"`   // 测试帧发送间隔

	// 点表配置
	PointFile string `yaml:"point_file"` // 点表文件路径

	// 缓冲配置
	SendBufferSize int `yaml:"send_buffer_size"` // 发送缓冲区大小
	QueueSize      int `yaml:"queue_size"`       // 数据队列大小
}

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		ListenAddr:     ":2404",
		MaxConnections: 5,
		MaxAPDULength:  253, // IEC104 规范最大值
		CommonAddress:  1,
		COTLocal:       2,
		COTRemote:      1,
		ConnectTimeout: 10 * time.Second,
		IdleTimeout:    30 * time.Second,
		TestInterval:   15 * time.Second,
		SendBufferSize: 1024,
		QueueSize:      8192,
	}
}

// Validate 校验配置
func (c *Config) Validate() error {
	if c.MaxAPDULength < 1 || c.MaxAPDULength > 253 {
		return fmt.Errorf("max_apdu_length must be in range [1, 253], got %d", c.MaxAPDULength)
	}
	if c.CommonAddress == 0 {
		return errors.New("common_address cannot be 0")
	}
	if c.ListenAddr == "" {
		return errors.New("listen_addr cannot be empty")
	}
	if c.MaxConnections <= 0 {
		return fmt.Errorf("max_connections must be positive, got %d", c.MaxConnections)
	}
	return nil
}
