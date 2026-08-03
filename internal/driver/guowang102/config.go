// internal/driver/guowang102/config.go - 国网102规约 配置定义
package guowang102

import (
	"errors"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 国网102驱动配置
type Config struct {
	// 网络连接
	Host            string        `yaml:"host"`              // 子站 IP
	Port            int           `yaml:"port"`              // 端口，固定 6960
	LinkAddress     uint16        `yaml:"link_address"`      // 链路地址，固定 0xFFFF
	CommonAddress   uint16        `yaml:"common_address"`    // 公共地址，固定 0xFFFF
	ConnectTimeout  time.Duration `yaml:"connect_timeout"`   // 连接超时
	ReadTimeout     time.Duration `yaml:"read_timeout"`      // 读超时
	WriteTimeout    time.Duration `yaml:"write_timeout"`     // 写超时
	KeepAliveInterval time.Duration `yaml:"keepalive_interval"` // TCP Keepalive 间隔

	// 协议流程
	LinkStatusInterval     time.Duration `yaml:"link_status_interval"`      // FC=9 链路状态检查间隔
	BackgroundScanInterval time.Duration `yaml:"background_scan_interval"`  // FC=11 召唤2级间隔
	PeriodicReadInterval   time.Duration `yaml:"periodic_read_interval"`    // FC=10 召唤1级间隔
	MaxRetry               int           `yaml:"max_retry"`                 // 最大重传次数
	RetryInterval          time.Duration `yaml:"retry_interval"`            // 重传间隔
	FrameTimeout           time.Duration `yaml:"frame_timeout"`             // 帧接收超时

	// 文件存储
	StorageDir  string        `yaml:"storage_dir"`   // 本地存储根目录
	MaxFileSize int           `yaml:"max_file_size"` // 最大文件大小，默认 20480 (512*40)
	FileTimeout time.Duration `yaml:"file_timeout"`  // 单文件接收总超时

	// 日志
	LogLevel string `yaml:"log_level"` // debug/info/warn/error
}

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		Host:                   "127.0.0.1",
		Port:                   6960,
		LinkAddress:            DefaultLinkAddress,
		CommonAddress:          DefaultCommonAddress,
		ConnectTimeout:         10 * time.Second,
		ReadTimeout:            30 * time.Second,
		WriteTimeout:           10 * time.Second,
		KeepAliveInterval:      10 * time.Second,
		LinkStatusInterval:     60 * time.Second,
		BackgroundScanInterval: 15 * time.Minute,
		PeriodicReadInterval:   5 * time.Minute,
		MaxRetry:               3,
		RetryInterval:          5 * time.Second,
		FrameTimeout:           5 * time.Second,
		StorageDir:             "./data/guowang102/files",
		MaxFileSize:            20480, // 512 * 40
		FileTimeout:            30 * time.Second,
		LogLevel:               "info",
	}
}

// Validate 校验配置
func (c *Config) Validate() error {
	if c.Host == "" {
		return errors.New("host is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("invalid port")
	}
	if c.LinkAddress == 0 {
		return errors.New("link_address is required")
	}
	if c.CommonAddress == 0 {
		return errors.New("common_address is required")
	}
	if c.StorageDir == "" {
		return errors.New("storage_dir is required")
	}
	if c.MaxFileSize <= 0 {
		return errors.New("max_file_size must be > 0")
	}
	return nil
}

// FillDefaults 填充默认值
func (c *Config) FillDefaults() {
	def := DefaultConfig()
	if c.Host == "" {
		c.Host = def.Host
	}
	if c.Port == 0 {
		c.Port = def.Port
	}
	if c.LinkAddress == 0 {
		c.LinkAddress = def.LinkAddress
	}
	if c.CommonAddress == 0 {
		c.CommonAddress = def.CommonAddress
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = def.ConnectTimeout
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = def.ReadTimeout
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = def.WriteTimeout
	}
	if c.KeepAliveInterval == 0 {
		c.KeepAliveInterval = def.KeepAliveInterval
	}
	if c.LinkStatusInterval == 0 {
		c.LinkStatusInterval = def.LinkStatusInterval
	}
	if c.BackgroundScanInterval == 0 {
		c.BackgroundScanInterval = def.BackgroundScanInterval
	}
	if c.PeriodicReadInterval == 0 {
		c.PeriodicReadInterval = def.PeriodicReadInterval
	}
	if c.MaxRetry == 0 {
		c.MaxRetry = def.MaxRetry
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = def.RetryInterval
	}
	if c.FrameTimeout == 0 {
		c.FrameTimeout = def.FrameTimeout
	}
	if c.StorageDir == "" {
		c.StorageDir = def.StorageDir
	}
	if c.MaxFileSize == 0 {
		c.MaxFileSize = def.MaxFileSize
	}
	if c.FileTimeout == 0 {
		c.FileTimeout = def.FileTimeout
	}
	if c.LogLevel == "" {
		c.LogLevel = def.LogLevel
	}
}

// ToClientConfig 转换为 TCP 客户端配置
func (c *Config) ToClientConfig() ClientConfig {
	return ClientConfig{
		Host:              c.Host,
		Port:              c.Port,
		LinkAddress:       c.LinkAddress,
		CommonAddress:     c.CommonAddress,
		ConnectTimeout:    c.ConnectTimeout,
		ReadTimeout:       c.ReadTimeout,
		WriteTimeout:      c.WriteTimeout,
		KeepAliveInterval: c.KeepAliveInterval,
		ReconnectInterval: c.RetryInterval,
		MaxReconnectInterval: 60 * time.Second,
	}
}

// ToFileTransferConfig 转换为文件传输配置
func (c *Config) ToFileTransferConfig() FileTransferConfig {
	return FileTransferConfig{
		StorageDir:      c.StorageDir,
		MaxFileSize:     c.MaxFileSize,
		FileTimeout:     c.FileTimeout,
		CleanupInterval: 60 * time.Second,
		MaxConcurrent:   100,
	}
}

// ParseConfig 从 YAML 字节解析配置
func ParseConfig(data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg.FillDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}