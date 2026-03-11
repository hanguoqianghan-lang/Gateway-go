// internal/driver/modbus/scan_group.go - 分频采集组
//
// 支持按不同的采集间隔（Interval）将测点分组到不同的ScanGroup中，
// 每个ScanGroup拥有独立的time.Ticker，实现分频采集功能。
package modbus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cgn/gateway/internal/broker"
	"github.com/cgn/gateway/internal/model"
	"github.com/simonvetter/modbus"
	"go.uber.org/zap"
)

// ScanGroup 采集组，包含相同采集间隔的测点
type ScanGroup struct {
	interval   time.Duration // 采集间隔
	points     []PointConfig // 该组内的测点
	blocks     []ReadBlock   // 合并后的批量读请求
	logger     *zap.Logger
	slaveID    string        // Slave ID

	// 运行时统计
	pollCount uint64
	errCount  uint64
}

// scanGroupWorker 采集组工作协程
type scanGroupWorker struct {
	group  *ScanGroup
	client *modbus.ModbusClient
	bus    *broker.Bus
	ticker *time.Ticker
}

// newScanGroup 创建采集组
func newScanGroup(interval time.Duration, points []PointConfig, slaveID string, logger *zap.Logger, maxRegs uint16) *ScanGroup {
	// maxGap=4：允许 4 个寄存器空洞，减少请求次数
	blocks := MergePoints(points, 4, maxRegs)

	return &ScanGroup{
		interval: interval,
		points:   points,
		blocks:   blocks,
		logger:   logger.With(zap.Duration("interval", interval)),
		slaveID:  slaveID,
	}
}

// start 启动采集组工作协程
func (g *ScanGroup) start(ctx context.Context, client *modbus.ModbusClient, bus *broker.Bus) {
	worker := &scanGroupWorker{
		group:  g,
		client: client,
		bus:    bus,
		ticker: time.NewTicker(g.interval),
	}

	g.logger.Info("采集组启动",
		zap.Int("points", len(g.points)),
		zap.Int("blocks", len(g.blocks)),
	)

	go worker.run(ctx)
}

// run 采集组主循环
func (w *scanGroupWorker) run(ctx context.Context) {
	defer w.ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.group.logger.Info("采集组协程退出（ctx 取消）")
			return
		case <-w.ticker.C:
			if err := w.poll(); err != nil {
				atomic.AddUint64(&w.group.errCount, 1)
				w.group.logger.Error("采集出错", zap.Error(err))
				// 采集组出错不会触发重连，重连由外层控制
			} else {
				atomic.AddUint64(&w.group.pollCount, 1)
			}
		}
	}
}

// poll 执行一轮批量读取
func (w *scanGroupWorker) poll() error {
	now := time.Now().UnixNano()

	for i := range w.group.blocks {
		blk := &w.group.blocks[i]
		if err := w.readBlock(blk, now); err != nil {
			return fmt.Errorf("block[%d] addr=%d: %w", i, blk.StartAddr, err)
		}
	}
	return nil
}

// readBlock 读取一个ReadBlock
func (w *scanGroupWorker) readBlock(blk *ReadBlock, ts int64) error {
	switch blk.RegType {
	case HoldingRegister, InputRegister:
		return w.readWordBlock(blk, ts)
	case Coil, DiscreteInput:
		return w.readBitBlock(blk, ts)
	default:
		return fmt.Errorf("未知寄存器类型 %d", blk.RegType)
	}
}

// readWordBlock 批量读取字寄存器
func (w *scanGroupWorker) readWordBlock(blk *ReadBlock, ts int64) error {
	var regs []uint16
	var err error

	if blk.RegType == HoldingRegister {
		regs, err = w.client.ReadRegisters(blk.StartAddr, blk.Count, modbus.HOLDING_REGISTER)
	} else {
		regs, err = w.client.ReadRegisters(blk.StartAddr, blk.Count, modbus.INPUT_REGISTER)
	}
	if err != nil {
		return err
	}

	for _, pt := range blk.Points {
		offset := pt.Address - blk.StartAddr
		width := registerWidth(pt.DataType)

		// 边界保护
		if int(offset)+int(width) > len(regs) {
			w.group.logger.Warn("寄存器偏移越界，跳过该点",
				zap.String("point", pt.Name),
				zap.Uint16("address", pt.Address),
			)
			continue
		}

		raw := regs[offset : offset+width]
		value, parseErr := parseRegisters(raw, pt.DataType, pt.ByteOrder, pt.BitPos)
		if parseErr != nil {
			w.group.logger.Warn("寄存器解析失败", zap.String("point", pt.Name), zap.Error(parseErr))
			continue
		}

		// 线性缩放
		scaled := value
		if pt.Scale != 0 && pt.Scale != 1.0 {
			scaled = value*pt.Scale + pt.Offset
		}

		// 整数类型且无缩放时，保留 int64 类型
		var publishVal interface{}
		noScale := pt.Scale == 0 || pt.Scale == 1.0
		if noScale && pt.Offset == 0 {
			switch pt.DataType {
			case Int16, Uint16, Int32, Uint32:
				publishVal = int64(value)
			default:
				publishVal = scaled
			}
		} else {
			publishVal = scaled
		}

		p := model.GetPoint()
		p.ID = w.group.slaveID + "/modbus/" + pt.Name
		p.Value = publishVal
		p.Timestamp = ts
		p.Quality = model.QualityGood
		w.bus.Publish(p)
	}
	return nil
}

// readBitBlock 批量读取线圈/离散输入
func (w *scanGroupWorker) readBitBlock(blk *ReadBlock, ts int64) error {
	var coils []bool
	var err error

	if blk.RegType == Coil {
		coils, err = w.client.ReadCoils(blk.StartAddr, blk.Count)
	} else {
		coils, err = w.client.ReadDiscreteInputs(blk.StartAddr, blk.Count)
	}
	if err != nil {
		return err
	}

	for _, pt := range blk.Points {
		offset := int(pt.Address - blk.StartAddr)
		if offset >= len(coils) {
			continue
		}
		p := model.GetPoint()
		p.ID = w.group.slaveID + "/modbus/" + pt.Name
		p.Value = coils[offset]
		p.Timestamp = ts
		p.Quality = model.QualityGood
		w.bus.Publish(p)
	}
	return nil
}

// ScanGroupManager 采集组管理器，管理多个不同采集间隔的采集组
type ScanGroupManager struct {
	groups      []*ScanGroup
	groupWorkers []*scanGroupWorker
	logger      *zap.Logger
	mu          sync.RWMutex
	maxRegs     uint16 // 单次请求最大读取寄存器数
}

// NewScanGroupManager 创建采集组管理器
func NewScanGroupManager(logger *zap.Logger, maxRegs uint16) *ScanGroupManager {
	return &ScanGroupManager{
		groups:  make([]*ScanGroup, 0),
		logger:  logger,
		maxRegs: maxRegs,
	}
}

// AddPoint 添加测点到采集组
func (m *ScanGroupManager) AddPoint(point PointConfig, interval time.Duration, slaveID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 查找是否已存在相同间隔的采集组
	for _, g := range m.groups {
		if g.interval == interval {
			g.points = append(g.points, point)
			return
		}
	}

	// 创建新的采集组
	group := newScanGroup(interval, []PointConfig{point}, slaveID, m.logger, m.maxRegs)
	m.groups = append(m.groups, group)
}

// Build 构建采集组（在添加完所有测点后调用）
func (m *ScanGroupManager) Build() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, g := range m.groups {
		// 重新合并测点
		g.blocks = MergePoints(g.points, 4, m.maxRegs)
	}
}

// Start 启动所有采集组
func (m *ScanGroupManager) Start(ctx context.Context, client *modbus.ModbusClient, bus *broker.Bus) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.groupWorkers = make([]*scanGroupWorker, len(m.groups))
	for i, g := range m.groups {
		m.groupWorkers[i] = &scanGroupWorker{
			group:  g,
			client: client,
			bus:    bus,
			ticker: time.NewTicker(g.interval),
		}
		g.start(ctx, client, bus)
	}

	m.logger.Info("所有采集组已启动", zap.Int("groups", len(m.groups)))
}

// Stop 停止所有采集组
func (m *ScanGroupManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, worker := range m.groupWorkers {
		if worker.ticker != nil {
			worker.ticker.Stop()
		}
	}

	m.logger.Info("所有采集组已停止")
}

// Stats 返回统计信息
func (m *ScanGroupManager) Stats() []ScanGroupStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]ScanGroupStats, len(m.groups))
	for i, g := range m.groups {
		stats[i] = ScanGroupStats{
			Interval:  g.interval,
			PointCount: len(g.points),
			PollCount:  atomic.LoadUint64(&g.pollCount),
			ErrCount:   atomic.LoadUint64(&g.errCount),
		}
	}
	return stats
}

// ScanGroupStats 采集组统计信息
type ScanGroupStats struct {
	Interval   time.Duration
	PointCount int
	PollCount  uint64
	ErrCount   uint64
}
