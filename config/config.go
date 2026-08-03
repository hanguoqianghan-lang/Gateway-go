// config/config.go - 全局配置结构体定义
package config

import "time"

// AppConfig 全局配置结构体
type AppConfig struct {
	// Gateway 网关基本信息
	Gateway GatewayConfig `yaml:"gateway" json:"gateway"`

	// Drivers 南向驱动配置列表
	Drivers []DriverConfig `yaml:"drivers" json:"drivers"`

	// Exporters 北向导出器配置
	Exporters ExporterConfig `yaml:"exporters" json:"exporters"`

	// Bus 内部总线配置
	Bus BusConfig `yaml:"bus" json:"bus"`

	// Storage 离线缓存配置
	Storage StorageConfig `yaml:"storage" json:"storage"`

	// NTP 时间同步配置
	NTP NTPConfig `yaml:"ntp" json:"ntp"`
}

// GatewayConfig 网关基本信息
type GatewayConfig struct {
	// Name 网关名称
	Name string `yaml:"name" json:"name" default:"Gateway"`
	// Version 网关版本
	Version string `yaml:"version" json:"version" default:"1.0.0"`
	// MetricsAddr HTTP metrics 服务地址（如 :8080），为空则使用默认 :8080
	MetricsAddr string `yaml:"metrics_addr" json:"metrics_addr" default:":8080"`
	// LogPath 日志文件路径（可选，未配置则输出到控制台）
	LogPath string `yaml:"log_path" json:"log_path"`
	// LogLevel 日志级别：debug, info, warn, error
	LogLevel string `yaml:"log_level" json:"log_level" default:"info"`
	// LogMaxSize 日志文件最大大小（MB）
	LogMaxSize int `yaml:"log_max_size" json:"log_max_size" default:"100"`
	// LogMaxBackups 日志文件最大备份数
	LogMaxBackups int `yaml:"log_max_backups" json:"log_max_backups" default:"3"`
	// LogMaxAge 日志文件最大保留天数
	LogMaxAge int `yaml:"log_max_age" json:"log_max_age" default:"28"`
	// LogCompress 是否压缩日志文件
	LogCompress bool `yaml:"log_compress" json:"log_compress" default:"true"`
}

// DriverConfig 南向驱动配置
type DriverConfig struct {
	// ID 驱动实例唯一标识
	ID string `yaml:"id" json:"id"`
	// Type 驱动类型：modbus_tcp, iec104, iec101, iec102, iec103
	Type string `yaml:"type" json:"type"`
	// Enabled 是否启用该驱动
	Enabled bool `yaml:"enabled" json:"enabled" default:"true"`
	// Name 驱动实例名称（用于日志和测点ID前缀）
	Name string `yaml:"name" json:"name"`

	// PointFile 点表文件路径（CSV格式）
	PointFile string `yaml:"point_file" json:"point_file"`

	// Modbus TCP 配置（仅当 Type=modbus_tcp 时有效）
	Modbus *ModbusDriverConfig `yaml:"modbus,omitempty" json:"modbus,omitempty"`

	// IEC104 配置（仅当 Type=iec104 时有效）
	IEC104 *IEC104DriverConfig `yaml:"iec104,omitempty" json:"iec104,omitempty"`

	// IEC101 配置（仅当 Type=iec101 时有效）
	IEC101 *IEC101DriverConfig `yaml:"iec101,omitempty" json:"iec101,omitempty"`

	// IEC102 配置（仅当 Type=iec102 时有效）
	IEC102 *IEC102DriverConfig `yaml:"iec102,omitempty" json:"iec102,omitempty"`

	// IEC103 配置（仅当 Type=iec103 时有效）
	IEC103 *IEC103DriverConfig `yaml:"iec103,omitempty" json:"iec103,omitempty"`

	// DL/T 645 配置（仅当 Type=dlt645 时有效）
	DLT645 *DLT645DriverConfig `yaml:"dlt645,omitempty" json:"dlt645,omitempty"`

	// GB/T 26875.3 配置（仅当 Type=gb26875 时有效）
	GB26875 *GB26875DriverConfig `yaml:"gb26875,omitempty" json:"gb26875,omitempty"`

	// 国网102风光一体配置（仅当 Type=guowang102 时有效）
	GuoWang102 *GuoWang102DriverConfig `yaml:"guowang102,omitempty" json:"guowang102,omitempty"`
}

// ModbusDriverConfig Modbus TCP 驱动配置
type ModbusDriverConfig struct {
	// Host Modbus Slave IP 地址
	Host string `yaml:"host" json:"host"`
	// Port Modbus TCP 端口
	Port int `yaml:"port" json:"port" default:"502"`
	// UnitID Modbus 单元 ID（从站地址）
	UnitID uint8 `yaml:"unit_id" json:"unit_id" default:"1"`
	// Timeout 单次请求超时
	Timeout time.Duration `yaml:"timeout" json:"timeout" default:"3s"`
	// MaxRetryInterval 指数退避最大间隔
	MaxRetryInterval time.Duration `yaml:"max_retry_interval" json:"max_retry_interval" default:"60s"`
	// PollInterval 默认采集轮询间隔（CSV中未指定Interval时使用）
	PollInterval time.Duration `yaml:"poll_interval" json:"poll_interval" default:"1s"`
}

// IEC104DriverConfig IEC104 驱动配置
type IEC104DriverConfig struct {
	// Host IEC104 主站 IP 地址
	Host string `yaml:"host" json:"host"`
	// Port IEC104 端口
	Port int `yaml:"port" json:"port" default:"2404"`
	// CommonAddress 公共地址
	CommonAddress uint8 `yaml:"common_address" json:"common_address" default:"1"`
	// Timeout ASDU 超时时间
	Timeout time.Duration `yaml:"timeout" json:"timeout" default:"10s"`
	// TestInterval 心跳测试间隔
	TestInterval time.Duration `yaml:"test_interval" json:"test_interval" default:"20s"`
	// ReconnectInterval 重连间隔
	ReconnectInterval time.Duration `yaml:"reconnect_interval" json:"reconnect_interval" default:"5s"`
	// GIInterval 总召唤间隔（0表示不主动召唤）
	GIInterval time.Duration `yaml:"gi_interval" json:"gi_interval" default:"0"`
	// ClockSyncInterval 时钟同步间隔（0表示不同步）
	ClockSyncInterval time.Duration `yaml:"clock_sync_interval" json:"clock_sync_interval" default:"0"`
	// GIStaggeredDelay GI 防风暴随机延迟上限
	GIStaggeredDelay time.Duration `yaml:"gi_staggered_delay" json:"gi_staggered_delay" default:"5s"`
	// EnableSystemMetrics 是否启用系统测点
	EnableSystemMetrics bool `yaml:"enable_system_metrics" json:"enable_system_metrics" default:"false"`
	// ASDUBufferSize ASDU 处理缓冲区大小（默认 50000），支持百万级测点
	ASDUBufferSize int `yaml:"asdu_buffer_size" json:"asdu_buffer_size" default:"50000"`
}

// IEC101DriverConfig IEC101 驱动配置
type IEC101DriverConfig struct {
	// Transport 接入方式："serial" 或 "tcp"
	//   - serial → 走串口 (goburrow/serial + com0com 真串口对)
	//   - tcp    → 调试用，net.Dial 到 TCPAddr（mocksvr-iec101 之类）
	Transport string `yaml:"transport" json:"transport" default:"serial"`
	// TCPAddr TCP 接入地址，如 "127.0.0.1:8881"（Transport=tcp 必填）
	TCPAddr string `yaml:"tcp_addr" json:"tcp_addr"`
	// SerialPort 串口设备路径（如 COM3/COM4，由 com0com 映射）
	SerialPort string `yaml:"serial_port" json:"serial_port"`
	// BaudRate 波特率
	BaudRate int `yaml:"baud_rate" json:"baud_rate" default:"9600"`
	// DataBits 数据位
	DataBits int `yaml:"data_bits" json:"data_bits" default:"8"`
	// StopBits 停止位
	StopBits int `yaml:"stop_bits" json:"stop_bits" default:"1"`
	// Parity 校验位：none, even, odd
	Parity string `yaml:"parity" json:"parity" default:"even"`
	// Timeout 响应超时
	Timeout time.Duration `yaml:"timeout" json:"timeout" default:"1s"`
	// CommonAddress 公共地址
	CommonAddress int `yaml:"common_address" json:"common_address" default:"1"`
	// LinkAddress 链路地址
	LinkAddress int `yaml:"link_address" json:"link_address" default:"1"`
	// BalancedMode 传输模式：true=平衡模式，false=非平衡模式
	BalancedMode bool `yaml:"balanced_mode" json:"balanced_mode" default:"false"`
	// GIInterval 总召唤间隔（非平衡模式）
	GIInterval time.Duration `yaml:"gi_interval" json:"gi_interval" default:"15m"`
	// PollInterval 轮询间隔
	PollInterval time.Duration `yaml:"poll_interval" json:"poll_interval" default:"1s"`
	// MaxRetry 最大重试次数
	MaxRetry int `yaml:"max_retry" json:"max_retry" default:"3"`
	// RetryInterval 重试间隔
	RetryInterval time.Duration `yaml:"retry_interval" json:"retry_interval" default:"1s"`
}

// IEC102DriverConfig IEC102 驱动配置
type IEC102DriverConfig struct {
	// SerialPort 串口设备路径
	SerialPort string `yaml:"serial_port" json:"serial_port"`
	// BaudRate 波特率
	BaudRate int `yaml:"baud_rate" json:"baud_rate" default:"9600"`
	// DataBits 数据位
	DataBits int `yaml:"data_bits" json:"data_bits" default:"8"`
	// StopBits 停止位
	StopBits int `yaml:"stop_bits" json:"stop_bits" default:"1"`
	// Parity 校验位：none, even, odd
	Parity string `yaml:"parity" json:"parity" default:"even"`
	// Timeout 响应超时
	Timeout time.Duration `yaml:"timeout" json:"timeout" default:"1s"`
	// CommonAddress 公共地址
	CommonAddress int `yaml:"common_address" json:"common_address" default:"1"`
	// LinkAddress 链路地址
	LinkAddress int `yaml:"link_address" json:"link_address" default:"1"`
	// BalancedMode 传输模式：true=平衡模式，false=非平衡模式
	BalancedMode bool `yaml:"balanced_mode" json:"balanced_mode" default:"false"`
	// BackgroundScanInterval 背景扫描间隔
	BackgroundScanInterval time.Duration `yaml:"background_scan_interval" json:"background_scan_interval" default:"15m"`
	// PeriodicReadInterval 周期读取间隔
	PeriodicReadInterval time.Duration `yaml:"periodic_read_interval" json:"periodic_read_interval" default:"5m"`
	// PollInterval 轮询间隔
	PollInterval time.Duration `yaml:"poll_interval" json:"poll_interval" default:"1s"`
	// MaxRetry 最大重试次数
	MaxRetry int `yaml:"max_retry" json:"max_retry" default:"3"`
	// RetryInterval 重试间隔
	RetryInterval time.Duration `yaml:"retry_interval" json:"retry_interval" default:"1s"`
}

// IEC103DriverConfig IEC103 驱动配置
type IEC103DriverConfig struct {
	// SerialPort 串口设备路径
	SerialPort string `yaml:"serial_port" json:"serial_port"`
	// BaudRate 波特率
	BaudRate int `yaml:"baud_rate" json:"baud_rate" default:"9600"`
	// DataBits 数据位
	DataBits int `yaml:"data_bits" json:"data_bits" default:"8"`
	// StopBits 停止位
	StopBits int `yaml:"stop_bits" json:"stop_bits" default:"1"`
	// Parity 校验位：none, even, odd（IEC103 标准使用 even）
	Parity string `yaml:"parity" json:"parity" default:"even"`
	// Timeout 响应超时
	Timeout time.Duration `yaml:"timeout" json:"timeout" default:"1s"`
	// CommonAddress 公共地址
	CommonAddress int `yaml:"common_address" json:"common_address" default:"1"`
	// LinkAddress 链路地址
	LinkAddress int `yaml:"link_address" json:"link_address" default:"1"`
	// BalancedMode 传输模式：true=平衡模式，false=非平衡模式
	BalancedMode bool `yaml:"balanced_mode" json:"balanced_mode" default:"false"`
	// GIInterval 总召唤间隔（非平衡模式）
	GIInterval time.Duration `yaml:"gi_interval" json:"gi_interval" default:"15m"`
	// PollInterval 轮询间隔
	PollInterval time.Duration `yaml:"poll_interval" json:"poll_interval" default:"1s"`
	// MaxRetry 最大重试次数
	MaxRetry int `yaml:"max_retry" json:"max_retry" default:"3"`
	// RetryInterval 重试间隔
	RetryInterval time.Duration `yaml:"retry_interval" json:"retry_interval" default:"1s"`
	// SOEQueueSize SOE 事件队列大小（应对故障爆发）
	SOEQueueSize int `yaml:"soe_queue_size" json:"soe_queue_size" default:"10000"`
	// SOEWorkerCount SOE 处理 Worker 数量
	SOEWorkerCount int `yaml:"soe_worker_count" json:"soe_worker_count" default:"10"`
}

// DLT645DriverConfig DL/T 645 驱动配置
type DLT645DriverConfig struct {
	// SerialPort 串口设备路径（如 COM3/COM4）
	SerialPort string `yaml:"serial_port" json:"serial_port"`
	// BaudRate 波特率
	BaudRate int `yaml:"baud_rate" json:"baud_rate" default:"9600"`
	// DataBits 数据位
	DataBits int `yaml:"data_bits" json:"data_bits" default:"8"`
	// StopBits 停止位
	StopBits int `yaml:"stop_bits" json:"stop_bits" default:"1"`
	// Parity 校验位：none, even, odd
	Parity string `yaml:"parity" json:"parity" default:"even"`
	// ProtocolVersion 协议版本：1997 或 2007
	ProtocolVersion string `yaml:"protocol_version" json:"protocol_version" default:"2007"`
	// UseLeadingByte 是否使用前导字节（唤醒沉睡电表）
	UseLeadingByte bool `yaml:"use_leading_byte" json:"use_leading_byte" default:"false"`
	// LeadingByteCount 前导字节数量
	LeadingByteCount int `yaml:"leading_byte_count" json:"leading_byte_count" default:"4"`
	// CharTimeout 字符间超时
	CharTimeout time.Duration `yaml:"char_timeout" json:"char_timeout" default:"50ms"`
	// FrameTimeout 帧超时
	FrameTimeout time.Duration `yaml:"frame_timeout" json:"frame_timeout" default:"200ms"`
	// ResponseTimeout 响应超时
	ResponseTimeout time.Duration `yaml:"response_timeout" json:"response_timeout" default:"1s"`
	// MaxRetry 最大重试次数
	MaxRetry int `yaml:"max_retry" json:"max_retry" default:"3"`
	// RetryInterval 重试间隔
	RetryInterval time.Duration `yaml:"retry_interval" json:"retry_interval" default:"1s"`
	// PollInterval 采集轮询间隔
	PollInterval time.Duration `yaml:"poll_interval" json:"poll_interval" default:"1s"`
	// QueryIntervalPerPoint 每测点查询间隔（加速采集）
	QueryIntervalPerPoint time.Duration `yaml:"query_interval_per_point" json:"query_interval_per_point" default:"50ms"`
}

// GB26875DriverConfig GB/T 26875.3 驱动配置
type GB26875DriverConfig struct {
	// Host 监听地址（如 0.0.0.0 或留空）
	Host string `yaml:"host" json:"host" default:"0.0.0.0"`
	// Port TCP 监听端口
	Port int `yaml:"port" json:"port" default:"5001"`
	// LocalAddress 本机地址（6字节HEX字符串，下行报文的源地址）
	LocalAddress string `yaml:"local_address" json:"local_address" default:"000000000000"`
	// MaxConnections 最大并发传输装置连接数
	MaxConnections int `yaml:"max_connections" json:"max_connections" default:"100"`
	// ReadTimeout 接收单帧超时
	ReadTimeout time.Duration `yaml:"read_timeout" json:"read_timeout" default:"10s"`
	// WriteTimeout 发送超时
	WriteTimeout time.Duration `yaml:"write_timeout" json:"write_timeout" default:"5s"`
	// FrameTimeout 相邻字节超时（用于切分帧）
	FrameTimeout time.Duration `yaml:"frame_timeout" json:"frame_timeout" default:"200ms"`
	// ClockSyncInterval 时钟同步周期（0 = 不主动同步；非0时按周期广播）
	ClockSyncInterval time.Duration `yaml:"clock_sync_interval" json:"clock_sync_interval" default:"0"`
	// Version 主版本号（固定1）
	Version uint8 `yaml:"version" json:"version" default:"1"`
	// UserVersion 用户版本号（自定义）
	UserVersion uint8 `yaml:"user_version" json:"user_version" default:"1"`
	// EnableSystemMetrics 是否启用系统测点（连接状态、计数等）
	EnableSystemMetrics bool `yaml:"enable_system_metrics" json:"enable_system_metrics" default:"false"`
	// ADUBufferSize 接收 ADU 缓冲区大小
	ADUBufferSize int `yaml:"adu_buffer_size" json:"adu_buffer_size" default:"5000"`
}

// GuoWang102DriverConfig 国网102风光一体驱动配置
type GuoWang102DriverConfig struct {
	// Host 子站 IP 地址
	Host string `yaml:"host" json:"host" default:"127.0.0.1"`
	// Port 子站端口，固定 6960
	Port int `yaml:"port" json:"port" default:"6960"`
	// LinkAddress 链路地址，固定 0xFFFF
	LinkAddress uint16 `yaml:"link_address" json:"link_address" default:"65535"`
	// CommonAddress 公共地址，固定 0xFFFF
	CommonAddress uint16 `yaml:"common_address" json:"common_address" default:"65535"`
	// ConnectTimeout 连接超时
	ConnectTimeout time.Duration `yaml:"connect_timeout" json:"connect_timeout" default:"10s"`
	// ReadTimeout 读超时
	ReadTimeout time.Duration `yaml:"read_timeout" json:"read_timeout" default:"30s"`
	// WriteTimeout 写超时
	WriteTimeout time.Duration `yaml:"write_timeout" json:"write_timeout" default:"10s"`
	// KeepAliveInterval TCP Keepalive 间隔
	KeepAliveInterval time.Duration `yaml:"keepalive_interval" json:"keepalive_interval" default:"10s"`
	// LinkStatusInterval FC=9 链路状态检查间隔
	LinkStatusInterval time.Duration `yaml:"link_status_interval" json:"link_status_interval" default:"60s"`
	// BackgroundScanInterval FC=11 召唤2级数据间隔
	BackgroundScanInterval time.Duration `yaml:"background_scan_interval" json:"background_scan_interval" default:"15m"`
	// PeriodicReadInterval FC=10 召唤1级数据间隔
	PeriodicReadInterval time.Duration `yaml:"periodic_read_interval" json:"periodic_read_interval" default:"5m"`
	// MaxRetry 最大重传次数
	MaxRetry int `yaml:"max_retry" json:"max_retry" default:"3"`
	// RetryInterval 重传间隔
	RetryInterval time.Duration `yaml:"retry_interval" json:"retry_interval" default:"5s"`
	// FrameTimeout 帧接收超时
	FrameTimeout time.Duration `yaml:"frame_timeout" json:"frame_timeout" default:"5s"`
	// StorageDir 本地文件存储目录
	StorageDir string `yaml:"storage_dir" json:"storage_dir" default:"./data/guowang102/files"`
	// MaxFileSize 最大文件大小，默认 20480 (512*40)
	MaxFileSize int `yaml:"max_file_size" json:"max_file_size" default:"20480"`
	// FileTimeout 单文件接收总超时
	FileTimeout time.Duration `yaml:"file_timeout" json:"file_timeout" default:"30s"`
	// LogLevel 日志级别
	LogLevel string `yaml:"log_level" json:"log_level" default:"info"`
}

// ExporterConfig 北向导出器配置
type ExporterConfig struct {
	// MQTT MQTT 导出器配置
	MQTT *MQTTExporterConfig `yaml:"mqtt,omitempty" json:"mqtt,omitempty"`

	// Kafka Kafka 导出器配置
	Kafka *KafkaExporterConfig `yaml:"kafka,omitempty" json:"kafka,omitempty"`

	// IEC104 IEC104 北向从站配置
	IEC104 *IEC104ExporterConfig `yaml:"iec104,omitempty" json:"iec104,omitempty"`

	// BatchConfig 批量发送配置
	Batch BatchConfig `yaml:"batch" json:"batch"`
}

// MQTTExporterConfig MQTT 导出器配置
type MQTTExporterConfig struct {
	// Enabled 是否启用 MQTT 导出
	Enabled bool `yaml:"enabled" json:"enabled" default:"false"`
	// Broker MQTT broker 地址
	Broker string `yaml:"broker" json:"broker" default:"tcp://127.0.0.1:1883"`
	// ClientID 客户端 ID
	ClientID string `yaml:"client_id" json:"client_id" default:"gateway"`
	// TopicPrefix 发布主题前缀
	TopicPrefix string `yaml:"topic_prefix" json:"topic_prefix" default:"gateway/data"`
	// QoS 服务质量等级：0, 1, 2
	QoS byte `yaml:"qos" json:"qos" default:"1"`
	// Username 用户名（可选）
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	// Password 密码（可选）
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	// ConnTimeout 连接超时
	ConnTimeout time.Duration `yaml:"conn_timeout" json:"conn_timeout" default:"5s"`
}

// KafkaExporterConfig Kafka 导出器配置
type KafkaExporterConfig struct {
	// Enabled 是否启用 Kafka 导出
	Enabled bool `yaml:"enabled" json:"enabled" default:"false"`
	// Brokers Kafka broker 列表
	Brokers []string `yaml:"brokers" json:"brokers"`
	// Topic 主题名称
	Topic string `yaml:"topic" json:"topic" default:"gateway-data"`
	// ClientID 客户端 ID
	ClientID string `yaml:"client_id" json:"client_id" default:"gateway-producer"`
	// Async 是否异步写入
	Async bool `yaml:"async" json:"async" default:"true"`
	// Timeout 写入超时
	Timeout time.Duration `yaml:"timeout" json:"timeout" default:"5s"`
	// BatchSize 批量大小
	BatchSize int `yaml:"batch_size" json:"batch_size" default:"100"`
	// BatchTimeout 批量超时
	BatchTimeout time.Duration `yaml:"batch_timeout" json:"batch_timeout" default:"10ms"`
	// Compression 压缩类型：none, gzip, snappy, lz4, zstd
	Compression string `yaml:"compression" json:"compression" default:"none"`
	// FlushMessages 批量发送消息数（已废弃，使用BatchSize）
	FlushMessages int `yaml:"flush_messages" json:"flush_messages" default:"100"`
	// FlushTimeout 批量发送超时（已废弃，使用BatchTimeout）
	FlushTimeout time.Duration `yaml:"flush_timeout" json:"flush_timeout" default:"1s"`
	// Acks 确认级别：0, 1, -1
	Acks int `yaml:"acks" json:"acks" default:"1"`

	// SASL 认证配置（可选）
	SASL *SASLConfig `yaml:"sasl,omitempty" json:"sasl,omitempty"`
	// TLS 加密配置（可选）
	TLS *TLSConfig `yaml:"tls,omitempty" json:"tls,omitempty"`
}

// SASLConfig Kafka SASL 认证配置
type SASLConfig struct {
	// Enabled 是否启用 SASL
	Enabled bool `yaml:"enabled" json:"enabled" default:"false"`
	// Mechanism 认证机制：PLAIN, SCRAM-SHA-256, SCRAM-SHA-512
	Mechanism string `yaml:"mechanism" json:"mechanism" default:"PLAIN"`
	// User 用户名
	User string `yaml:"user" json:"user"`
	// Password 密码
	Password string `yaml:"password" json:"password"`
}

// TLSConfig Kafka TLS 加密配置
type TLSConfig struct {
	// Enabled 是否启用 TLS
	Enabled bool `yaml:"enabled" json:"enabled" default:"false"`
	// SkipVerify 是否跳过证书校验（不建议在生产环境开启）
	SkipVerify bool `yaml:"skip_verify" json:"skip_verify" default:"false"`
	// CertFile 客户端证书文件路径
	CertFile string `yaml:"cert_file,omitempty" json:"cert_file,omitempty"`
	// KeyFile 客户端私钥文件路径
	KeyFile string `yaml:"key_file,omitempty" json:"key_file,omitempty"`
	// CAFile CA 证书文件路径
	CAFile string `yaml:"ca_file,omitempty" json:"ca_file,omitempty"`
}

// IEC104ExporterConfig IEC104 北向服务端配置
type IEC104ExporterConfig struct {
	// Enabled 是否启用 IEC104 导出
	Enabled bool `yaml:"enabled" json:"enabled" default:"false"`
	// BindAddr 监听地址（如 :2404）
	BindAddr string `yaml:"bind_addr" json:"bind_addr" default:":2404"`
	// CommonAddress 公共地址 (ASDU地址)
	CommonAddress uint16 `yaml:"common_address" json:"common_address" default:"1"`
	// MaxConnections 最大允许的客户端连接数
	MaxConnections int `yaml:"max_connections" json:"max_connections" default:"5"`
	// PointMapFile 点表映射文件（定义内部测点ID到IOA地址的映射）
	PointMapFile string `yaml:"point_map_file" json:"point_map_file"`
	// IdleTimeout 空闲连接超时
	IdleTimeout time.Duration `yaml:"idle_timeout" json:"idle_timeout" default:"60s"`
	// InterrogationAddr 总召唤地址 (通常为 0xFF)
	InterrogationAddr uint8 `yaml:"interrogation_addr" json:"interrogation_addr" default:"20"`
}

// BatchConfig 批量发送配置
type BatchConfig struct {
	// MaxSize 批量发送最大条数
	MaxSize int `yaml:"max_size" json:"max_size" default:"500"`
	// MaxLatency 批量发送最大延迟
	MaxLatency time.Duration `yaml:"max_latency" json:"max_latency" default:"200ms"`
}

// BusConfig 内部总线配置
type BusConfig struct {
	// BufferSize 主通道缓冲区大小
	BufferSize int `yaml:"buffer_size" json:"buffer_size" default:"8192"`
	// DeadbandThreshold 死区阈值（0=禁用）
	DeadbandThreshold float64 `yaml:"deadband_threshold" json:"deadband_threshold" default:"0"`
}

// StorageConfig 离线缓存配置
type StorageConfig struct {
	// Enabled 是否启用离线缓存
	Enabled bool `yaml:"enabled" json:"enabled" default:"false"`
	// Type 存储类型：memory, sqlite, leveldb
	Type string `yaml:"type" json:"type" default:"memory"`
	// Path 存储文件路径（sqlite/leveldb）
	Path string `yaml:"path" json:"path" default:"./data/gateway.db"`
	// MaxMemorySize 内存缓存最大大小（MB）
	MaxMemorySize int `yaml:"max_memory_size" json:"max_memory_size" default:"100"`
	// FlushInterval 刷盘间隔（仅memory类型有效）
	FlushInterval time.Duration `yaml:"flush_interval" json:"flush_interval" default:"30s"`
	// RetryInterval 重试间隔（网络恢复后）
	RetryInterval time.Duration `yaml:"retry_interval" json:"retry_interval" default:"10s"`
}

// NTPConfig NTP时间同步配置
type NTPConfig struct {
	// Enabled 是否启用NTP时间同步
	Enabled bool `yaml:"enabled" json:"enabled" default:"false"`
	// Server NTP服务器地址
	Server string `yaml:"server" json:"server" default:"pool.ntp.org"`
	// Port NTP服务器端口
	Port int `yaml:"port" json:"port" default:"123"`
	// Interval 同步间隔
	Interval time.Duration `yaml:"interval" json:"interval" default:"1h"`
	// Timeout 超时时间
	Timeout time.Duration `yaml:"timeout" json:"timeout" default:"5s"`
}
