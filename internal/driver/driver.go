// internal/driver/driver.go - 南向驱动接口定义
package driver

import (
	"context"
	"fmt"
	"sync"

	"github.com/cgn/gateway/internal/broker"
	"go.uber.org/zap"
)

// Driver 南向驱动接口。
// 每种协议（Modbus TCP、IEC104、IEC61850）实现此接口后注册到 Manager。
type Driver interface {
	// Init 初始化驱动，加载配置、校验参数等。
	// 返回 error 时网关将记录日志并跳过该驱动。
	Init(ctx context.Context) error

	// Start 启动数据采集循环。
	// 重要：实现方应在内部启动后台 goroutine 进行连接和采集，
	// 并立即返回 nil，不应阻塞等待连接成功。
	// 连接失败、断线重连等逻辑都在后台协程中处理。
	// ctx 取消时必须退出采集循环。
	Start(ctx context.Context, bus *broker.Bus) error

	// Stop 优雅停止驱动，释放连接、清理资源。
	Stop(ctx context.Context) error

	// Name 驱动唯一标识，用于日志和监控。
	Name() string
}

// ─── Manager ──────────────────────────────────────────────────────────────────

// Manager 统一管理所有已注册的南向驱动。
type Manager struct {
	mu      sync.RWMutex
	drivers map[string]Driver
	logger  *zap.Logger

	// 已成功启动的驱动计数
	startedCount int
}

// NewManager 创建驱动管理器。
func NewManager(logger *zap.Logger) *Manager {
	return &Manager{
		drivers: make(map[string]Driver),
		logger:  logger,
	}
}

// Register 注册驱动，name 应与 Driver.Name() 保持一致。
// 重复注册同名驱动会覆盖旧实例。
func (m *Manager) Register(d Driver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drivers[d.Name()] = d
}

// StartAllResult 启动结果
type StartAllResult struct {
	// Total 总驱动数
	Total int
	// Started 成功启动的驱动数
	Started int
	// Failed 启动失败的驱动名列表
	Failed []string
	// Errors 错误详情
	Errors []error
}

// StartAll 依次 Init 并 Start 所有已注册驱动。
// 实现故障隔离：单个驱动失败不影响其他驱动启动。
// 返回启动结果，调用方应根据结果决定是否继续运行。
func (m *Manager) StartAll(ctx context.Context, bus *broker.Bus) *StartAllResult {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := &StartAllResult{
		Total:   len(m.drivers),
		Started: 0,
		Failed:  make([]string, 0),
		Errors:  make([]error, 0),
	}

	for name, d := range m.drivers {
		// Init 阶段：配置校验失败是致命的，跳过该驱动
		if err := d.Init(ctx); err != nil {
			m.logger.Error("驱动初始化失败，跳过该驱动",
				zap.String("driver", name),
				zap.Error(err),
			)
			result.Failed = append(result.Failed, name)
			result.Errors = append(result.Errors, fmt.Errorf("driver %s init: %w", name, err))
			continue
		}

		// Start 阶段：启动后台协程，应立即返回 nil
		// 如果返回错误，说明驱动实现有问题
		if err := d.Start(ctx, bus); err != nil {
			m.logger.Error("驱动启动失败",
				zap.String("driver", name),
				zap.Error(err),
			)
			result.Failed = append(result.Failed, name)
			result.Errors = append(result.Errors, fmt.Errorf("driver %s start: %w", name, err))
			continue
		}

		// 启动成功
		result.Started++
		m.logger.Info("驱动已启动", zap.String("driver", name))
	}

	m.startedCount = result.Started

	// 汇总日志
	if result.Started > 0 {
		m.logger.Info("驱动启动汇总",
			zap.Int("total", result.Total),
			zap.Int("started", result.Started),
			zap.Int("failed", len(result.Failed)),
		)
	}

	return result
}

// GetStartedCount 返回已成功启动的驱动数量
func (m *Manager) GetStartedCount() int {
	return m.startedCount
}

// StopAll 依次停止所有驱动，错误聚合后返回。
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var errs []error
	for name, d := range m.drivers {
		if err := d.Stop(ctx); err != nil {
			m.logger.Warn("驱动停止失败", zap.String("driver", name), zap.Error(err))
			errs = append(errs, fmt.Errorf("driver %s stop: %w", name, err))
		} else {
			m.logger.Info("驱动已停止", zap.String("driver", name))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分驱动停止失败: %v", errs)
	}
	return nil
}
