// internal/driver/modbus/driver.go - Modbus TCP 驱动，实现 driver.Driver 接口
//
// 使用方式（在 main.go 中）：
//
//	cfg := modbus.ModbusConfig{
//	    Slaves: []modbus.SlaveConfig{
//	        {
//	            ID:   "plc-01",
//	            Host: "192.168.1.10",
//	            Port: 502,
//	            Points: []modbus.PointConfig{
//	                {Name: "temp",    Address: 100, DataType: modbus.Float32, Scale: 0.1},
//	                {Name: "pressure",Address: 102, DataType: modbus.Float32},
//	                {Name: "rpm",     Address: 110, DataType: modbus.Uint16},
//	            },
//	        },
//	    },
//	}
//	drv := modbus.NewDriver(cfg, logger)
//	manager.Register(drv)
package modbus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/cgn/gateway/internal/broker"
	"go.uber.org/zap"
)

// Driver 实现 driver.Driver 接口，管理所有 Modbus TCP Slave。
// 每个 Slave 对应一个独立 goroutine，互不影响。
type Driver struct {
	cfg     ModbusConfig
	logger  *zap.Logger
	workers []*slaveWorker

	// 用于 Stop() 优雅停止所有采集协程
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// 运行状态（0=未启动 1=运行中 2=已停止）
	state uint32
}

// NewDriver 创建 Modbus 驱动实例。
// logger 建议携带父级 field，如 zap.String("component", "modbus")。
func NewDriver(cfg ModbusConfig, logger *zap.Logger) *Driver {
	return &Driver{
		cfg:    cfg,
		logger: logger.With(zap.String("driver", "modbus-tcp")),
	}
}

// Name 实现 driver.Driver 接口，返回驱动唯一名称。
func (d *Driver) Name() string {
	return "modbus-tcp"
}

// Init 校验配置，预构建所有 slaveWorker 及其 ReadBlock。
// 此阶段不建立 TCP 连接，连接在 Start 后由各协程异步完成。
func (d *Driver) Init(_ context.Context) error {
	if len(d.cfg.Slaves) == 0 {
		return fmt.Errorf("modbus: 配置中没有任何 Slave 节点")
	}

	d.workers = make([]*slaveWorker, 0, len(d.cfg.Slaves))
	for i, s := range d.cfg.Slaves {
		if s.ID == "" {
			return fmt.Errorf("modbus: slave[%d] 缺少 ID 字段", i)
		}
		if s.Host == "" {
			return fmt.Errorf("modbus: slave %q 缺少 Host 字段", s.ID)
		}
		if len(s.Points) == 0 {
			d.logger.Warn("slave 没有配置任何测点，跳过", zap.String("slave", s.ID))
			continue
		}
		w := newSlaveWorker(s, d.logger)
		if len(w.blocks) == 0 {
			d.logger.Warn("slave 测点合并后无有效 ReadBlock，跳过", zap.String("slave", s.ID))
			continue
		}
		d.workers = append(d.workers, w)
		d.logger.Info("slave 初始化完成",
			zap.String("slave", s.ID),
			zap.Int("points", len(s.Points)),
			zap.Int("blocks", len(w.blocks)),
		)
	}

	if len(d.workers) == 0 {
		return fmt.Errorf("modbus: 所有 Slave 均无有效配置")
	}

	d.logger.Info("驱动初始化完成",
		zap.Int("slaves", len(d.workers)),
	)
	return nil
}

// Start 为每个 Slave 启动独立采集 goroutine。
// ctx 取消时（或调用 Stop），所有协程将优雅退出。
func (d *Driver) Start(ctx context.Context, bus *broker.Bus) error {
	if !atomic.CompareAndSwapUint32(&d.state, 0, 1) {
		return fmt.Errorf("modbus: 驱动已在运行中")
	}

	// 创建可取消的子 context，Stop() 通过 cancel 触发退出
	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel

	for _, w := range d.workers {
		w := w // 捕获循环变量
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			w.run(runCtx, bus)
		}()
	}

	d.logger.Info("所有 Slave 采集协程已启动", zap.Int("count", len(d.workers)))
	return nil
}

// Stop 取消所有采集协程并等待它们退出（阻塞直到超时或全部退出）。
func (d *Driver) Stop(_ context.Context) error {
	if !atomic.CompareAndSwapUint32(&d.state, 1, 2) {
		return nil // 未启动或已停止，直接返回
	}

	d.logger.Info("正在停止 Modbus 驱动...")
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	d.logger.Info("Modbus 驱动已完全停止")
	return nil
}

// Stats 返回所有 Slave 的运行统计（仅供监控/调试使用）。
func (d *Driver) Stats() []SlaveStats {
	stats := make([]SlaveStats, len(d.workers))
	for i, w := range d.workers {
		stats[i] = SlaveStats{
			SlaveID:     w.cfg.ID,
			PollCount:   atomic.LoadUint64(&w.pollCount),
			ErrCount:    atomic.LoadUint64(&w.errCount),
			ReconnCount: atomic.LoadUint64(&w.reconnCount),
		}
	}
	return stats
}

// SlaveStats 单 Slave 的统计快照
type SlaveStats struct {
	SlaveID     string
	PollCount   uint64
	ErrCount    uint64
	ReconnCount uint64
}
