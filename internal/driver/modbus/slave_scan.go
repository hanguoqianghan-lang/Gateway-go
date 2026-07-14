// internal/driver/modbus/slave_scan.go - 支持分频采集的Slave采集协程
//
// 该版本支持按不同的采集间隔（Interval）将测点分组到不同的ScanGroup中，
// 每个ScanGroup拥有独立的time.Ticker，实现分频采集功能。
package modbus

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/model"
	"github.com/simonvetter/modbus"
	"go.uber.org/zap"
)

// SlaveScanWorker 支持分频采集的Slave采集协程
type SlaveScanWorker struct {
	cfg              SlaveConfig
	pointIntervals   map[string]time.Duration // 测点名称 -> 采集间隔
	defaultInterval  time.Duration            // 默认采集间隔
	scanGroupManager *ScanGroupManager
	logger           *zap.Logger

	// 运行时统计
	reconnCount uint64
	pollCount   uint64
	errCount    uint64

	// 连接状态（用于快速检测断开）
	connErr atomic.Value // 存储 error，实现了 error 接口的 struct
}

// connErrorFlag 连接错误标志
type connErrorFlag struct {
	err error
}

func (e *connErrorFlag) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return ""
}

func (w *SlaveScanWorker) setConnError(err error) {
	w.connErr.Store(&connErrorFlag{err: err})
}

func (w *SlaveScanWorker) getConnError() error {
	if v := w.connErr.Load(); v != nil {
		if cf, ok := v.(*connErrorFlag); ok {
			return cf.err
		}
	}
	return nil
}

func (w *SlaveScanWorker) clearConnError() {
	w.connErr.Store(&connErrorFlag{err: nil})
}

// NewSlaveScanWorker 创建支持分频采集的Slave采集协程
func NewSlaveScanWorker(cfg SlaveConfig, logger *zap.Logger) *SlaveScanWorker {
	cfg.fillDefaults()
	return &SlaveScanWorker{
		cfg:             cfg,
		pointIntervals:  make(map[string]time.Duration),
		defaultInterval: cfg.PollInterval,
		logger:          logger.With(zap.String("slave", cfg.ID), zap.String("host", cfg.Host)),
	}
}

// SetPointInterval 设置测点的采集间隔
func (w *SlaveScanWorker) SetPointInterval(pointName string, interval time.Duration) {
	w.pointIntervals[pointName] = interval
}

// BuildScanGroups 构建采集组
func (w *SlaveScanWorker) BuildScanGroups() {
	w.scanGroupManager = NewScanGroupManager(w.logger, w.cfg.MaxRegistersPerRequest)

	for _, pt := range w.cfg.Points {
		// 获取测点的采集间隔
		interval := w.pointIntervals[pt.Name]
		if interval == 0 {
			interval = w.defaultInterval
		}

		// 添加到采集组
		w.scanGroupManager.AddPoint(pt, interval, w.cfg.ID)
	}

	// 构建采集组
	w.scanGroupManager.Build()

	w.logger.Info("采集组构建完成",
		zap.Int("total_points", len(w.cfg.Points)),
		zap.Int("scan_groups", len(w.scanGroupManager.groups)),
	)

	// 打印每个采集组的信息
	for _, g := range w.scanGroupManager.groups {
		w.logger.Info("采集组信息",
			zap.Duration("interval", g.interval),
			zap.Int("points", len(g.points)),
		)
	}
}

// run 是该 Slave 的主协程，ctx 取消时退出
func (w *SlaveScanWorker) run(ctx context.Context, bus *broker.Bus) {
	w.logger.Info("Slave采集协程启动（支持分频采集）",
		zap.Int("scan_groups", len(w.scanGroupManager.groups)),
	)

	backoff := newExponentialBackoff(time.Second, w.cfg.MaxRetryInterval)

	for {
		// 检查 ctx
		select {
		case <-ctx.Done():
			w.logger.Info("Slave采集协程退出（ctx 取消）")
			return
		default:
		}

		// 清空连接错误状态
		w.clearConnError()

		// 建立连接
		client, err := w.connect(ctx)
		if err != nil {
			// ctx 已取消
			return
		}

		w.logger.Info("连接成功，开始采集", zap.Uint64("reconnect_count", atomic.LoadUint64(&w.reconnCount)))
		atomic.AddUint64(&w.reconnCount, 1)
		backoff.Reset() // 连接成功后重置退避

		// 注册连接错误回调（采集出错时记录）
		w.scanGroupManager.SetConnErrorHandler(func(err error) {
			w.setConnError(err)
		})

		// 启动所有采集组
		w.scanGroupManager.Start(ctx, client, bus)

		// 等待连接断开或 ctx 取消
		w.waitForDisconnect(client, ctx)

		// 连接断开/停止请求，停止所有采集组
		w.scanGroupManager.Stop()

		// 检查是否由连接错误触发重连
		connErr := w.getConnError()

		// 对所有测点发布 QualityNotConnected 质量戳
		w.publishDisconnected(bus)

		// 检查是否由 ctx 取消触发的退出
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 等待退避时间后重连
		delay := backoff.Next()
		totalErrCount := atomic.LoadUint64(&w.errCount)
		if connErr != nil {
			w.logger.Warn("连接断开（采集错误），等待重连",
				zap.Duration("backoff", delay),
				zap.Uint64("err_count", totalErrCount),
				zap.Error(connErr),
			)
		} else {
			w.logger.Warn("连接断开，等待重连",
				zap.Duration("backoff", delay),
				zap.Uint64("err_count", totalErrCount),
			)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// connect 使用指数退避尝试建立 TCP 连接
func (w *SlaveScanWorker) connect(ctx context.Context) (*modbus.ModbusClient, error) {
	url := fmt.Sprintf("tcp://%s", net.JoinHostPort(w.cfg.Host, fmt.Sprintf("%d", w.cfg.Port)))
	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     url,
		Timeout: w.cfg.Timeout,
		Speed:   0,
	})
	if err != nil {
		// 配置错误，不重试
		w.logger.Error("创建 modbus client 失败", zap.Error(err))
		return nil, err
	}

	b := newExponentialBackoff(500*time.Millisecond, 30*time.Second)
	for {
		if connectErr := client.Open(); connectErr != nil {
			delay := b.Next()
			w.logger.Warn("连接失败，重试",
				zap.Error(connectErr),
				zap.Duration("retry_after", delay),
			)
			select {
			case <-ctx.Done():
				_ = client.Close()
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		// 设置单元 ID
		client.SetUnitId(w.cfg.UnitID)
		return client, nil
	}
}

// waitForDisconnect 等待连接断开或 ctx 取消
// 优先检查连接错误（采集出错时立即触发），辅以定期心跳检测
func (w *SlaveScanWorker) waitForDisconnect(client *modbus.ModbusClient, ctx context.Context) {
	// 心跳检测间隔
	heartbeatTicker := time.NewTicker(500 * time.Millisecond)
	defer heartbeatTicker.Stop()

	for {
		// 快速检查连接错误标志（采集出错时立即触发重连）
		if err := w.getConnError(); err != nil {
			w.logger.Warn("检测到连接错误，触发重连", zap.Error(err))
			return
		}

		// 同时监听 ctx 和心跳
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			_, err := client.ReadRegisters(0, 1, modbus.HOLDING_REGISTER)
			if err != nil {
				w.logger.Warn("心跳检测失败，触发重连", zap.Error(err))
				w.setConnError(err)
				return
			}
		}
	}
}

// publishDisconnected 对本 Slave 所有测点发布 QualityNotConnected 质量戳
func (w *SlaveScanWorker) publishDisconnected(bus *broker.Bus) {
	ts := time.Now().UnixNano()
	for _, pt := range w.cfg.Points {
		p := model.GetPoint()
		p.ID = w.cfg.ID + "/modbus/" + pt.Name
		p.Value = nil
		p.Timestamp = ts
		p.Quality = model.QualityNotConnected
		bus.Publish(p)
	}
}

// Stats 返回统计信息
func (w *SlaveScanWorker) Stats() SlaveScanStats {
	scanStats := SlaveScanStats{
		SlaveID:     w.cfg.ID,
		PollCount:   atomic.LoadUint64(&w.pollCount),
		ErrCount:    atomic.LoadUint64(&w.errCount),
		ReconnCount: atomic.LoadUint64(&w.reconnCount),
	}

	if w.scanGroupManager != nil {
		scanStats.GroupStats = w.scanGroupManager.Stats()
		// 累加采集组的统计
		for _, gs := range scanStats.GroupStats {
			scanStats.PollCount += gs.PollCount
			scanStats.ErrCount += gs.ErrCount
		}
	}

	return scanStats
}

// SlaveScanStats Slave统计信息
type SlaveScanStats struct {
	SlaveID     string
	PollCount   uint64
	ErrCount    uint64
	ReconnCount uint64
	GroupStats  []ScanGroupStats
}