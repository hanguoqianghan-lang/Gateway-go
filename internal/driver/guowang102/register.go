// internal/driver/guowang102/register.go - 国网102驱动注册
package guowang102

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"github.com/gateway/gateway/config"
	"github.com/gateway/gateway/internal/driver"
)

// RegisterDriver 注册国网102驱动到全局注册表
// 此函数在 init() 中自动调用，无需手动调用
func RegisterDriver() {
	driver.RegisterDriver("guowang102", NewDriverFromConfig)
}

func init() {
	RegisterDriver()
}

// NewDriverFromConfig 从配置创建驱动实例
// 符合 driver.DriverCreator 接口签名
func NewDriverFromConfig(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (driver.Driver, error) {
	// 驱动配置在 drvCfg.GuoWang102 中
	if drvCfg.GuoWang102 == nil {
		return nil, errors.New("guowang102 config is nil")
	}
	cfg := drvCfg.GuoWang102

	d := &Driver{
		cfg: &DriverConfig{
			Host:                   cfg.Host,
			Port:                   cfg.Port,
			LinkAddress:            cfg.LinkAddress,
			CommonAddress:          cfg.CommonAddress,
			ConnectTimeout:         cfg.ConnectTimeout.String(),
			ReadTimeout:            cfg.ReadTimeout.String(),
			WriteTimeout:           cfg.WriteTimeout.String(),
			KeepAliveInterval:      cfg.KeepAliveInterval.String(),
			LinkStatusInterval:     cfg.LinkStatusInterval.String(),
			BackgroundScanInterval: cfg.BackgroundScanInterval.String(),
			PeriodicReadInterval:   cfg.PeriodicReadInterval.String(),
			MaxRetry:               cfg.MaxRetry,
			RetryInterval:          cfg.RetryInterval.String(),
			FrameTimeout:           cfg.FrameTimeout.String(),
			StorageDir:             cfg.StorageDir,
			MaxFileSize:            cfg.MaxFileSize,
			FileTimeout:            cfg.FileTimeout.String(),
			LogLevel:               cfg.LogLevel,
		},
		logger: logger.Named("guowang102"),
	}

	if err := d.Init(ctx); err != nil {
		return nil, err
	}
	return d, nil
}