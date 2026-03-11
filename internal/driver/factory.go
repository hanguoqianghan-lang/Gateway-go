// internal/driver/factory.go - 驱动工厂接口
//
// 提供统一的驱动注册和创建机制，支持根据配置自动创建对应的驱动实例。
// 新增驱动时只需实现 Driver 接口并注册到工厂即可，无需修改 main.go。
package driver

import (
	"context"
	"fmt"
	"sync"

	"github.com/gateway/gateway/config"
	"github.com/gateway/gateway/internal/broker"
	"go.uber.org/zap"
)

// DriverCreator 驱动创建函数类型
// 每个驱动包在 init() 中注册自己的创建函数
type DriverCreator func(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (Driver, error)

// factory 全局驱动工厂实例
var factory = &DriverFactory{
	creators: make(map[string]DriverCreator),
}

// DriverFactory 驱动工厂
type DriverFactory struct {
	mu       sync.RWMutex
	creators map[string]DriverCreator
}

// RegisterDriver 注册驱动创建函数
// 驱动包应在 init() 函数中调用此方法注册自己
//
// 示例：
//
//	func init() {
//	    driver.RegisterDriver("modbus_tcp", NewModbusDriverFromConfig)
//	}
func RegisterDriver(driverType string, creator DriverCreator) {
	factory.mu.Lock()
	defer factory.mu.Unlock()

	if _, exists := factory.creators[driverType]; exists {
		panic(fmt.Sprintf("驱动类型已注册: %s", driverType))
	}

	factory.creators[driverType] = creator
}

// CreateDriver 根据配置创建驱动实例
func CreateDriver(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (Driver, error) {
	factory.mu.RLock()
	creator, exists := factory.creators[drvCfg.Type]
	factory.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("不支持的驱动类型: %s", drvCfg.Type)
	}

	return creator(ctx, drvCfg, logger)
}

// GetRegisteredTypes 获取所有已注册的驱动类型
func GetRegisteredTypes() []string {
	factory.mu.RLock()
	defer factory.mu.RUnlock()

	types := make([]string, 0, len(factory.creators))
	for t := range factory.creators {
		types = append(types, t)
	}
	return types
}

// IsDriverTypeRegistered 检查驱动类型是否已注册
func IsDriverTypeRegistered(driverType string) bool {
	factory.mu.RLock()
	defer factory.mu.RUnlock()

	_, exists := factory.creators[driverType]
	return exists
}

// DriverManager 驱动管理器（增强版）
// 封装了驱动工厂和驱动管理器的功能
type DriverManager struct {
	manager *Manager
	factory *DriverFactory
	logger  *zap.Logger
	drivers map[string]Driver // 驱动ID -> 驱动实例
}

// NewDriverManager 创建驱动管理器
func NewDriverManager(logger *zap.Logger) *DriverManager {
	return &DriverManager{
		manager: NewManager(logger),
		factory: factory,
		logger:  logger,
		drivers: make(map[string]Driver),
	}
}

// InitializeDrivers 根据配置初始化所有驱动
func (dm *DriverManager) InitializeDrivers(ctx context.Context, driversCfg []config.DriverConfig) error {
	for _, drvCfg := range driversCfg {
		if !drvCfg.Enabled {
			dm.logger.Info("跳过已禁用的驱动",
				zap.String("driver_id", drvCfg.ID),
				zap.String("driver_type", drvCfg.Type),
			)
			continue
		}

		drv, err := dm.CreateAndRegisterDriver(ctx, drvCfg)
		if err != nil {
			dm.logger.Error("初始化驱动失败",
				zap.String("driver_id", drvCfg.ID),
				zap.String("driver_type", drvCfg.Type),
				zap.Error(err),
			)
			// 继续初始化其他驱动
			continue
		}

		dm.logger.Info("驱动初始化成功",
			zap.String("driver_id", drvCfg.ID),
			zap.String("driver_name", drv.Name()),
		)
	}

	return nil
}

// CreateAndRegisterDriver 创建并注册单个驱动
func (dm *DriverManager) CreateAndRegisterDriver(ctx context.Context, drvCfg config.DriverConfig) (Driver, error) {
	dm.logger.Info("创建驱动",
		zap.String("id", drvCfg.ID),
		zap.String("type", drvCfg.Type),
		zap.String("name", drvCfg.Name),
		zap.String("point_file", drvCfg.PointFile),
	)

	// 使用工厂创建驱动
	drv, err := CreateDriver(ctx, drvCfg, dm.logger)
	if err != nil {
		return nil, err
	}

	// 注册到管理器
	dm.manager.Register(drv)
	dm.drivers[drvCfg.ID] = drv

	return drv, nil
}

// StartAll 启动所有驱动
// 返回启动结果，调用方应根据结果决定是否继续运行
func (dm *DriverManager) StartAll(ctx context.Context, bus *broker.Bus) *StartAllResult {
	return dm.manager.StartAll(ctx, bus)
}

// StopAll 停止所有驱动
func (dm *DriverManager) StopAll(ctx context.Context) error {
	return dm.manager.StopAll(ctx)
}

// GetDriver 获取指定ID的驱动实例
func (dm *DriverManager) GetDriver(id string) (Driver, bool) {
	drv, ok := dm.drivers[id]
	return drv, ok
}

// GetAllDrivers 获取所有驱动实例
func (dm *DriverManager) GetAllDrivers() map[string]Driver {
	result := make(map[string]Driver, len(dm.drivers))
	for k, v := range dm.drivers {
		result[k] = v
	}
	return result
}

// GetDriverCount 获取驱动数量
func (dm *DriverManager) GetDriverCount() int {
	return len(dm.drivers)
}

// GetStartedCount 获取已成功启动的驱动数量
func (dm *DriverManager) GetStartedCount() int {
	return dm.manager.GetStartedCount()
}
