// internal/driver/iec103/config.go - IEC 60870-5-103 配置定义
package iec103

import (
	"time"
)

// Config IEC103 驱动配置
type Config struct {
	// 驱动标识
	ID   string // 驱动 ID
	Name string // 驱动名称

	// 串口配置
	SerialPort     string        // 串口设备路径，如 "/dev/ttyUSB0" 或 "COM1"
	BaudRate       int           // 波特率，如 9600, 19200, 38400
	DataBits       int           // 数据位，通常为 8
	StopBits       int           // 停止位，1 或 2
	Parity         string        // 校验位："none", "even", "odd"（IEC103 通常使用 even）
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

	// SOE 并发处理参数
	SOEQueueSize    int // SOE 事件队列大小
	SOEWorkerCount  int // SOE 处理 Worker 数量

	// 点表配置
	Points []PointConfig // 点表配置列表
}

// PointConfig 单个测点配置
type PointConfig struct {
	// 点标识
	Name string // 测点名称

	// IEC103 地址（CA + FUN + INF）- 关键差异！
	CA  uint8  // 公共地址（Common Address）
	FUN uint8  // 功能类型（Function Type）
	INF uint8  // 信息号（Information Number）

	// 数据类型
	TypeID uint8 // ASDU 类型标识（TI）

	// 转换参数
	Scale   float64 // 缩放因子
	Offset  float64 // 偏移量

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
		Parity:          "even", // IEC103 标准使用偶校验
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
		SOEQueueSize:    10000, // SOE 事件队列大小（应对故障爆发）
		SOEWorkerCount:  10,    // SOE 处理 Worker 数量
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
	if c.SOEQueueSize <= 0 {
		return ErrInvalidConfig("soe_queue_size must be positive")
	}
	if c.SOEWorkerCount <= 0 {
		return ErrInvalidConfig("soe_worker_count must be positive")
	}
	return nil
}

// ConfigError 配置错误
type ConfigError string

func (e ConfigError) Error() string {
	return string(e)
}

func ErrInvalidConfig(msg string) error {
	return ConfigError("iec103: invalid config: " + msg)
}

// ─────────────────────────────────────────────────────────────────────────────
// IEC103 特有常量定义
// ─────────────────────────────────────────────────────────────────────────────

// 功能类型（FUN）- IEC103 特有
const (
	FUN_TIME_SYNC          = 1  // 时间同步
	FUN_GENERAL_INTERROG   = 2  // 总召唤
	FUN_READ               = 3  // 读命令
	FUN_TEST               = 4  // 测试命令
	FUN_RESET_PROCESS      = 5  // 复位进程
	FUN_DELAY_ACQUISITION  = 6  // 延迟获取
	FUN_SPONTANEOUS        = 7  // 突发传输
	FUN_GENERIC_CLASS_DATA = 8  // 通用分类数据
	FUN_CONTROL            = 9  // 控制命令
	FUN_PARAMETER          = 10 // 参数设置
)

// 类型标识（TI）- IEC103 特有
const (
	TI_TIME_SYNC                 = 1  // 带时标的消息
	TI_TIME_SYNC_RELATIVE        = 2  // 带相对时间的时标消息
	TI_MEASURED_VALUE_NORMAL     = 3  // 测量值归一化值
	TI_MEASURED_VALUE_SCALED     = 4  // 测量值标度化值
	TI_MEASURED_VALUE_SHORT      = 5  // 测量值短浮点数
	TI_BIT_STRING                = 6  // 比特串
	TI_MEASURED_VALUE_NORMAL_TS  = 7  // 测量值归一化值带时标
	TI_MEASURED_VALUE_SCALED_TS  = 8  // 测量值标度化值带时标
	TI_MEASURED_VALUE_SHORT_TS   = 9  // 测量值短浮点数带时标
	TI_BIT_STRING_TS             = 10 // 比特串带时标
	TI_SINGLE_POINT              = 11 // 单点信息
	TI_DOUBLE_POINT              = 12 // 双点信息
	TI_SINGLE_POINT_TS           = 13 // 单点信息带时标
	TI_DOUBLE_POINT_TS           = 14 // 双点信息带时标
	TI_STEP_POSITION             = 15 // 步位置信息
	TI_STEP_POSITION_TS          = 16 // 步位置信息带时标
	TI_BINARY_STATE              = 17 // 二进制状态信息
	TI_BINARY_STATE_TS           = 18 // 二进制状态信息带时标
	TI_GENERIC_CLASS_DATA        = 19 // 通用分类数据
	TI_GENERIC_CLASS_IDENT       = 20 // 通用分类标识
	TI_GENERIC_CLASS_DEF         = 21 // 通用分类定义
	TI_GENERIC_CLASS_DESCR       = 22 // 通用分类描述
	TI_GENERIC_CLASS_RANGE       = 23 // 通用分类范围
	TI_GENERIC_CLASS_SCALE       = 24 // 通用分类标度
	TI_GENERIC_CLASS_VALUE       = 25 // 通用分类值
)
