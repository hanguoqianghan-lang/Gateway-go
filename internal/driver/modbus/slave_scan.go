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
	w.logger.Info("Slave采集协程启动（支持分频采集）")

	backoff := newExponentialBackoff(time.Second, w.cfg.MaxRetryInterval)

	for {
		// 检查 ctx
		select {
		case <-ctx.Done():
			w.logger.Info("Slave采集协程退出（ctx 取消）")
			return
		default:
		}

		// 建立连接
		client, err := w.connect(ctx)
		if err != nil {
			// ctx 已取消
			return
		}

		w.logger.Info("连接成功，开始采集", zap.Uint64("reconnect_count", atomic.LoadUint64(&w.reconnCount)))
		atomic.AddUint64(&w.reconnCount, 1)
		backoff.Reset() // 连接成功后重置退避

		// 启动所有采集组
		w.scanGroupManager.Start(ctx, client, bus)

		// 等待连接断开（这里简化处理，实际应该监听连接状态）
		// 由于scanGroupManager不会主动停止，我们需要通过其他方式检测断线
		// 这里使用一个简单的轮询机制来检测连接状态
		w.waitForDisconnect(client, ctx)

		// 连接断开，停止所有采集组
		w.scanGroupManager.Stop()

		// 对所有测点发布 QualityNotConnected 质量戳
		w.publishDisconnected(bus)

		// 等待退避时间后重连
		delay := backoff.Next()
		w.logger.Warn("连接断开，等待重连",
			zap.Duration("backoff", delay),
			zap.Uint64("err_count", w.scanGroupManager.groups[0].errCount),
		)
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

// waitForDisconnect 等待连接断开
func (w *SlaveScanWorker) waitForDisconnect(client *modbus.ModbusClient, ctx context.Context) {
	// 简化实现：定期检测连接状态
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 尝试读取一个寄存器来检测连接状态
			_, err := client.ReadRegisters(0, 1, modbus.HOLDING_REGISTER)
			if err != nil {
				w.logger.Warn("检测到连接断开", zap.Error(err))
				_ = client.Close()
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
	totalPollCount := uint64(0)
	totalErrCount := uint64(0)

	if w.scanGroupManager != nil {
		for _, stats := range w.scanGroupManager.Stats() {
			totalPollCount += stats.PollCount
			totalErrCount += stats.ErrCount
		}
	}

	return SlaveScanStats{
		SlaveID:     w.cfg.ID,
		PollCount:   totalPollCount,
		ErrCount:    totalErrCount,
		ReconnCount: atomic.LoadUint64(&w.reconnCount),
		GroupStats:  w.scanGroupManager.Stats(),
	}
}

// SlaveScanStats Slave统计信息
type SlaveScanStats struct {
	SlaveID     string
	PollCount   uint64
	ErrCount    uint64
	ReconnCount uint64
	GroupStats  []ScanGroupStats
}
