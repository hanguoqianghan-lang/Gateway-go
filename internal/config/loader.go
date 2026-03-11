// internal/config/loader.go - 配置文件加载器
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cgn/gateway/config"
	"gopkg.in/yaml.v3"
	"go.uber.org/zap"
)

// Loader 配置加载器
type Loader struct {
	configPath string
	logger     *zap.Logger
}

// NewLoader 创建配置加载器
func NewLoader(configPath string, logger *zap.Logger) *Loader {
	return &Loader{
		configPath: configPath,
		logger:     logger,
	}
}

// Load 加载配置文件
func (l *Loader) Load() (*config.AppConfig, error) {
	// 获取配置文件的绝对路径
	absPath, err := filepath.Abs(l.configPath)
	if err != nil {
		return nil, fmt.Errorf("获取配置文件绝对路径失败: %w", err)
	}

	// 检查配置文件是否存在
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("配置文件不存在: %s", absPath)
	}

	l.logger.Info("开始加载配置文件", zap.String("path", absPath))

	// 读取配置文件内容
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	l.logger.Info("配置文件读取成功", zap.String("file", absPath))

	// 解析到配置结构体
	cfg := &config.AppConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 调试：打印解析后的配置
	l.logger.Debug("解析后的配置",
		zap.String("gateway_name", cfg.Gateway.Name),
		zap.Int("drivers_count", len(cfg.Drivers)),
	)
	if len(cfg.Drivers) > 0 {
		l.logger.Debug("第一个驱动",
			zap.String("id", cfg.Drivers[0].ID),
			zap.String("type", cfg.Drivers[0].Type),
			zap.String("point_file", cfg.Drivers[0].PointFile),
		)
	}

	// 验证配置
	if err := l.validate(cfg); err != nil {
		return nil, fmt.Errorf("配置验证失败: %w", err)
	}

	// 填充默认值
	l.fillDefaults(cfg)

	l.logger.Info("配置加载完成",
		zap.String("gateway_name", cfg.Gateway.Name),
		zap.Int("drivers", len(cfg.Drivers)),
		zap.Bool("mqtt_enabled", cfg.Exporters.MQTT != nil && cfg.Exporters.MQTT.Enabled),
		zap.Bool("kafka_enabled", cfg.Exporters.Kafka != nil && cfg.Exporters.Kafka.Enabled),
	)

	return cfg, nil
}

// validate 验证配置的有效性
func (l *Loader) validate(cfg *config.AppConfig) error {
	// 验证网关配置
	if cfg.Gateway.Name == "" {
		return fmt.Errorf("gateway.name 不能为空")
	}

	// 验证驱动配置
	if len(cfg.Drivers) == 0 {
		return fmt.Errorf("至少需要配置一个驱动")
	}

	driverIDs := make(map[string]bool)
	for i, drv := range cfg.Drivers {
		// 验证驱动ID
		if drv.ID == "" {
			return fmt.Errorf("drivers[%d].id 不能为空", i)
		}
		if driverIDs[drv.ID] {
			return fmt.Errorf("drivers[%d].id 重复: %s", i, drv.ID)
		}
		driverIDs[drv.ID] = true

		// 验证驱动类型
		if drv.Type == "" {
			return fmt.Errorf("drivers[%d].type 不能为空", i)
		}

		// 验证驱动名称
		if drv.Name == "" {
			return fmt.Errorf("drivers[%d].name 不能为空", i)
		}

		// 验证点表文件
		if drv.PointFile == "" {
			return fmt.Errorf("drivers[%d].point_file 不能为空", i)
		}

		// 验证点表文件是否存在
		absPath, err := filepath.Abs(drv.PointFile)
		if err != nil {
			return fmt.Errorf("drivers[%d].point_file 路径无效: %w", i, err)
		}
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			return fmt.Errorf("drivers[%d].point_file 不存在: %s", i, absPath)
		}

		// 根据驱动类型验证特定配置
		switch drv.Type {
		case "modbus_tcp":
			if drv.Modbus == nil {
				return fmt.Errorf("drivers[%d].modbus 配置不能为空", i)
			}
			if drv.Modbus.Host == "" {
				return fmt.Errorf("drivers[%d].modbus.host 不能为空", i)
			}
			if drv.Modbus.Port <= 0 || drv.Modbus.Port > 65535 {
				return fmt.Errorf("drivers[%d].modbus.port 无效: %d", i, drv.Modbus.Port)
			}
		case "iec104":
			if drv.IEC104 == nil {
				return fmt.Errorf("drivers[%d].iec104 配置不能为空", i)
			}
			if drv.IEC104.Host == "" {
				return fmt.Errorf("drivers[%d].iec104.host 不能为空", i)
			}
			if drv.IEC104.Port <= 0 || drv.IEC104.Port > 65535 {
				return fmt.Errorf("drivers[%d].iec104.port 无效: %d", i, drv.IEC104.Port)
			}
		default:
			return fmt.Errorf("drivers[%d].type 不支持的驱动类型: %s", i, drv.Type)
		}
	}

	// 验证导出器配置
	if cfg.Exporters.MQTT == nil && cfg.Exporters.Kafka == nil {
		return fmt.Errorf("至少需要启用一个导出器（MQTT 或 Kafka）")
	}

	// 验证MQTT配置
	if cfg.Exporters.MQTT != nil && cfg.Exporters.MQTT.Enabled {
		if cfg.Exporters.MQTT.Broker == "" {
			return fmt.Errorf("exporters.mqtt.broker 不能为空")
		}
		if cfg.Exporters.MQTT.QoS > 2 {
			return fmt.Errorf("exporters.mqtt.qos 无效: %d (应为 0, 1, 或 2)", cfg.Exporters.MQTT.QoS)
		}
	}

	// 验证Kafka配置
	if cfg.Exporters.Kafka != nil && cfg.Exporters.Kafka.Enabled {
		if len(cfg.Exporters.Kafka.Brokers) == 0 {
			return fmt.Errorf("exporters.kafka.brokers 不能为空")
		}
	}

	// 验证批量配置
	if cfg.Exporters.Batch.MaxSize <= 0 {
		return fmt.Errorf("exporters.batch.max_size 必须大于 0")
	}
	if cfg.Exporters.Batch.MaxLatency <= 0 {
		return fmt.Errorf("exporters.batch.max_latency 必须大于 0")
	}

	// 验证总线配置
	if cfg.Bus.BufferSize <= 0 {
		return fmt.Errorf("bus.buffer_size 必须大于 0")
	}

	return nil
}

// fillDefaults 填充默认值
func (l *Loader) fillDefaults(cfg *config.AppConfig) {
	// 网关默认值
	if cfg.Gateway.Name == "" {
		cfg.Gateway.Name = "CGN-Gateway"
	}
	if cfg.Gateway.Version == "" {
		cfg.Gateway.Version = "1.0.0"
	}
	if cfg.Gateway.LogLevel == "" {
		cfg.Gateway.LogLevel = "info"
	}

	// 批量配置默认值
	if cfg.Exporters.Batch.MaxSize == 0 {
		cfg.Exporters.Batch.MaxSize = 500
	}
	if cfg.Exporters.Batch.MaxLatency == 0 {
		cfg.Exporters.Batch.MaxLatency = 200 * 1000000 // 200ms (纳秒)
	}

	// 总线配置默认值
	if cfg.Bus.BufferSize == 0 {
		cfg.Bus.BufferSize = 8192
	}

	// 驱动默认值
	for i := range cfg.Drivers {
		drv := &cfg.Drivers[i]

		// Modbus 默认值
		if drv.Type == "modbus_tcp" && drv.Modbus != nil {
			if drv.Modbus.Port == 0 {
				drv.Modbus.Port = 502
			}
			if drv.Modbus.UnitID == 0 {
				drv.Modbus.UnitID = 1
			}
			if drv.Modbus.Timeout == 0 {
				drv.Modbus.Timeout = 3 * 1000000000 // 3s
			}
			if drv.Modbus.MaxRetryInterval == 0 {
				drv.Modbus.MaxRetryInterval = 60 * 1000000000 // 60s
			}
			if drv.Modbus.PollInterval == 0 {
				drv.Modbus.PollInterval = 1 * 1000000000 // 1s
			}
		}

		// IEC104 默认值
		if drv.Type == "iec104" && drv.IEC104 != nil {
			if drv.IEC104.Port == 0 {
				drv.IEC104.Port = 2404
			}
			if drv.IEC104.CommonAddress == 0 {
				drv.IEC104.CommonAddress = 1
			}
			if drv.IEC104.Timeout == 0 {
				drv.IEC104.Timeout = 10 * 1000000000 // 10s
			}
			if drv.IEC104.TestInterval == 0 {
				drv.IEC104.TestInterval = 20 * 1000000000 // 20s
			}
		}
	}

	// MQTT 默认值
	if cfg.Exporters.MQTT != nil {
		if cfg.Exporters.MQTT.ClientID == "" {
			cfg.Exporters.MQTT.ClientID = "cgn-gateway"
		}
		if cfg.Exporters.MQTT.TopicPrefix == "" {
			cfg.Exporters.MQTT.TopicPrefix = "gateway/data"
		}
		if cfg.Exporters.MQTT.QoS == 0 {
			cfg.Exporters.MQTT.QoS = 1
		}
		if cfg.Exporters.MQTT.ConnTimeout == 0 {
			cfg.Exporters.MQTT.ConnTimeout = 5 * 1000000000 // 5s
		}
	}

	// Kafka 默认值
	if cfg.Exporters.Kafka != nil {
		if cfg.Exporters.Kafka.ClientID == "" {
			cfg.Exporters.Kafka.ClientID = "cng-gateway-producer"
		}
		if cfg.Exporters.Kafka.Topic == "" {
			cfg.Exporters.Kafka.Topic = "gateway-data"
		}
		if cfg.Exporters.Kafka.Compression == "" {
			cfg.Exporters.Kafka.Compression = "none"
		}
		if cfg.Exporters.Kafka.FlushMessages == 0 {
			cfg.Exporters.Kafka.FlushMessages = 100
		}
		if cfg.Exporters.Kafka.FlushTimeout == 0 {
			cfg.Exporters.Kafka.FlushTimeout = 1 * 1000000000 // 1s
		}
		if cfg.Exporters.Kafka.Acks == 0 {
			cfg.Exporters.Kafka.Acks = 1
		}
	}
}
