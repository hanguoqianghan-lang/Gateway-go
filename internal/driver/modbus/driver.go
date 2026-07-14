// internal/driver/modbus/driver.go - Modbus TCP 驱动，实现 driver.Driver 接口
//
// 支持两种采集模式：
//   1. 固定间隔模式：所有测点使用相同的 PollInterval
//   2. 分频采集模式：每个测点可配置独立的 Interval
//
// 分频采集由 SlaveScanWorker 实现，通过 ScanGroupManager 管理多个采集组。
package modbus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gateway/gateway/internal/broker"
	"go.uber.org/zap"
)

// Driver 实现 driver.Driver 接口，管理所有 Modbus TCP Slave。
// 每个 Slave 对应一个独立 goroutine，互不影响。
type Driver struct {
	cfg     ModbusConfig
	logger  *zap.Logger

	// 采集模式：
	//   - fixedWorkers: 固定间隔模式（所有测点同一采集间隔）
	//   - scanWorkers: 分频采集模式（每测点可独立配置采集间隔）
	fixedWorkers []*slaveWorker
	scanWorkers  []*SlaveScanWorker

	// 用于 Stop() 优雅停止所有采集协程
	cancel context.CancelFunc
	wg     sync.WaitGroup

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

// Init 校验配置，预构建所有 worker。
// 此阶段不建立 TCP 连接，连接在 Start 后由各协程异步完成。
func (d *Driver) Init(_ context.Context) error {
	if len(d.cfg.Slaves) == 0 {
		return fmt.Errorf("modbus: 配置中没有任何 Slave 节点")
	}

	// 检查是否需要分频采集（任何测点配置了 Interval）
	needScanMode := d.checkNeedScanMode()

	if needScanMode {
		return d.initScanMode()
	}
	return d.initFixedMode()
}

// checkNeedScanMode 检查是否需要分频采集模式
func (d *Driver) checkNeedScanMode() bool {
	for _, s := range d.cfg.Slaves {
		for _, p := range s.Points {
			if p.Interval > 0 {
				return true
			}
		}
	}
	return false
}

// initFixedMode 初始化固定间隔模式
func (d *Driver) initFixedMode() error {
	d.fixedWorkers = make([]*slaveWorker, 0, len(d.cfg.Slaves))

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
		d.fixedWorkers = append(d.fixedWorkers, w)
		d.logger.Info("slave 初始化完成（固定间隔模式）",
			zap.String("slave", s.ID),
			zap.Int("points", len(s.Points)),
			zap.Int("blocks", len(w.blocks)),
			zap.Duration("poll_interval", s.PollInterval),
		)
	}

	if len(d.fixedWorkers) == 0 {
		return fmt.Errorf("modbus: 所有 Slave 均无有效配置")
	}

	d.logger.Info("驱动初始化完成（固定间隔模式）",
		zap.Int("slaves", len(d.fixedWorkers)),
	)
	return nil
}

// initScanMode 初始化分频采集模式
func (d *Driver) initScanMode() error {
	d.scanWorkers = make([]*SlaveScanWorker, 0, len(d.cfg.Slaves))

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

		w := NewSlaveScanWorker(s, d.logger)

		// 设置每个测点的采集间隔
		for _, pt := range s.Points {
			if pt.Interval > 0 {
				w.SetPointInterval(pt.Name, pt.Interval)
			}
		}

		// 构建采集组
		w.BuildScanGroups()
		d.scanWorkers = append(d.scanWorkers, w)

		d.logger.Info("slave 初始化完成（分频采集模式）",
			zap.String("slave", s.ID),
			zap.Int("points", len(s.Points)),
			zap.Int("scan_groups", len(w.scanGroupManager.groups)),
		)
	}

	if len(d.scanWorkers) == 0 {
		return fmt.Errorf("modbus: 所有 Slave 均无有效配置")
	}

	d.logger.Info("驱动初始化完成（分频采集模式）",
		zap.Int("slaves", len(d.scanWorkers)),
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

	// 启动固定间隔模式 worker
	for _, w := range d.fixedWorkers {
		w := w // 捕获循环变量
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			w.run(runCtx, bus)
		}()
	}

	// 启动分频采集模式 worker
	for _, w := range d.scanWorkers {
		w := w // 捕获循环变量
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			w.run(runCtx, bus)
		}()
	}

	workerCount := len(d.fixedWorkers) + len(d.scanWorkers)
	d.logger.Info("所有 Slave 采集协程已启动", zap.Int("count", workerCount))
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
// 兼容固定间隔和分频采集两种模式。
func (d *Driver) Stats() []SlaveStats {
	stats := make([]SlaveStats, 0, len(d.fixedWorkers)+len(d.scanWorkers))

	// 固定间隔模式统计
	for _, w := range d.fixedWorkers {
		stats = append(stats, SlaveStats{
			SlaveID:     w.cfg.ID,
			PollCount:   atomic.LoadUint64(&w.pollCount),
			ErrCount:    atomic.LoadUint64(&w.errCount),
			ReconnCount: atomic.LoadUint64(&w.reconnCount),
			Mode:        "fixed",
		})
	}

	// 分频采集模式统计
	for _, w := range d.scanWorkers {
		scanStats := w.Stats()
		stats = append(stats, SlaveStats{
			SlaveID:     scanStats.SlaveID,
			PollCount:   scanStats.PollCount,
			ErrCount:    scanStats.ErrCount,
			ReconnCount: scanStats.ReconnCount,
			Mode:        "scan",
			GroupStats:  scanStats.GroupStats,
		})
	}

	return stats
}

// SlaveStats 单 Slave 的统计快照
type SlaveStats struct {
	SlaveID     string
	PollCount   uint64
	ErrCount    uint64
	ReconnCount uint64
	Mode        string            // "fixed" 或 "scan"
	GroupStats  []ScanGroupStats  // 分频采集模式的采集组统计
}