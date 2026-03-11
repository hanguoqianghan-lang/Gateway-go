// internal/driver/modbus/slave.go - 单 Slave 采集协程
//
// 每个 Slave 独立运行一个 goroutine，包含：
//  1. 指数退避重连（1s → 2s → 4s … 最大 MaxRetryInterval）
//  2. 按 ReadBlock 批量读寄存器，将 raw 寄存器值解析为工程量
//  3. 通过 model.GetPoint() 从 Pool 获取对象，填充后发布到 Bus
//  4. 断线时对当前设备所有测点发布 QualityNotConnected 质量戳
package modbus

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sync/atomic"
	"time"

	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/model"
	"github.com/simonvetter/modbus"
	"go.uber.org/zap"
)

// slaveWorker 封装单 Slave 的全部采集状态
type slaveWorker struct {
	cfg    SlaveConfig
	blocks []ReadBlock // 合并后的批量读请求，Init 阶段生成
	logger *zap.Logger

	// 运行时统计（原子操作，ARM64 8 字节对齐安全）
	pollCount   uint64
	errCount    uint64
	reconnCount uint64
}

func newSlaveWorker(cfg SlaveConfig, logger *zap.Logger) *slaveWorker {
	cfg.fillDefaults()
	
	// 应用全局地址偏移
	for i := range cfg.Points {
		cfg.Points[i].Address += cfg.AddressOffset
	}
	
	// maxGap=4：允许 4 个寄存器空洞，减少请求次数
	blocks := MergePoints(cfg.Points, 4, cfg.MaxRegistersPerRequest)
	return &slaveWorker{
		cfg:    cfg,
		blocks: blocks,
		logger: logger.With(zap.String("slave", cfg.ID), zap.String("host", cfg.Host)),
	}
}

// run 是该 Slave 的主协程，ctx 取消时退出。
func (w *slaveWorker) run(ctx context.Context, bus *broker.Bus) {
	w.logger.Info("slave 采集协程启动",
		zap.Int("blocks", len(w.blocks)),
		zap.Duration("poll_interval", w.cfg.PollInterval),
	)

	backoff := newExponentialBackoff(time.Second, w.cfg.MaxRetryInterval)

	for {
		// 检查 ctx
		select {
		case <-ctx.Done():
			w.logger.Info("slave 采集协程退出（ctx 取消）")
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

		// 采集循环
		w.pollLoop(ctx, client, bus)

		// pollLoop 退出意味着连接断开，关闭连接
		_ = client.Close()

		// 对所有测点发布 QualityNotConnected 质量戳
		w.publishDisconnected(bus)

		// 等待退避时间后重连
		delay := backoff.Next()
		w.logger.Warn("连接断开，等待重连",
			zap.Duration("backoff", delay),
			zap.Uint64("err_count", atomic.LoadUint64(&w.errCount)),
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// connect 使用指数退避尝试建立 TCP 连接，直到成功或 ctx 取消。
// 此处的退避只针对初始连接阶段（首次连接）；
// 断线重连退避由外层 run() 的 backoff 管理。
func (w *slaveWorker) connect(ctx context.Context) (*modbus.ModbusClient, error) {
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

// pollLoop 在已连接的客户端上执行周期性采集，直到出错或 ctx 取消。
func (w *slaveWorker) pollLoop(ctx context.Context, client *modbus.ModbusClient, bus *broker.Bus) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.poll(client, bus); err != nil {
				atomic.AddUint64(&w.errCount, 1)
				w.logger.Error("采集出错，触发重连", zap.Error(err))
				return // 退出 pollLoop，由外层重连
			}
			atomic.AddUint64(&w.pollCount, 1)
		}
	}
}

// poll 执行一轮完整的批量读取。
func (w *slaveWorker) poll(client *modbus.ModbusClient, bus *broker.Bus) error {
	now := time.Now().UnixNano()

	for i := range w.blocks {
		blk := &w.blocks[i]
		if err := w.readBlock(client, blk, now, bus); err != nil {
			return fmt.Errorf("block[%d] addr=%d: %w", i, blk.StartAddr, err)
		}
	}
	return nil
}

// readBlock 读取一个 ReadBlock，解析寄存器值并发布到总线。
func (w *slaveWorker) readBlock(client *modbus.ModbusClient, blk *ReadBlock, ts int64, bus *broker.Bus) error {
	switch blk.RegType {
	case HoldingRegister, InputRegister:
		return w.readWordBlock(client, blk, ts, bus)
	case Coil, DiscreteInput:
		return w.readBitBlock(client, blk, ts, bus)
	default:
		return fmt.Errorf("未知寄存器类型 %d", blk.RegType)
	}
}

// readWordBlock 批量读取字寄存器（Holding/Input）并解析为各测点工程值。
func (w *slaveWorker) readWordBlock(client *modbus.ModbusClient, blk *ReadBlock, ts int64, bus *broker.Bus) error {
	var regs []uint16
	var err error

	if blk.RegType == HoldingRegister {
		regs, err = client.ReadRegisters(blk.StartAddr, blk.Count, modbus.HOLDING_REGISTER)
	} else {
		regs, err = client.ReadRegisters(blk.StartAddr, blk.Count, modbus.INPUT_REGISTER)
	}
	if err != nil {
		return err
	}

	for _, pt := range blk.Points {
		offset := pt.Address - blk.StartAddr
		width := registerWidth(pt.DataType)

		// 边界保护
		if int(offset)+int(width) > len(regs) {
			w.logger.Warn("寄存器偏移越界，跳过该点",
				zap.String("point", pt.Name),
				zap.Uint16("address", pt.Address),
			)
			continue
		}

		raw := regs[offset : offset+width]
		value, parseErr := parseRegisters(raw, pt.DataType, pt.ByteOrder, pt.BitPos)
		if parseErr != nil {
			w.logger.Warn("寄存器解析失败", zap.String("point", pt.Name), zap.Error(parseErr))
			continue
		}

		// 线性缩放
		scaled := value
		if pt.Scale != 0 && pt.Scale != 1.0 {
			scaled = value*pt.Scale + pt.Offset
		}

		// 整数类型且无缩放时，保留 int64 类型，JSON 序列化后显示为整数而非浮点
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
		p.ID = w.cfg.ID + "/modbus/" + pt.Name
		p.Value = publishVal
		p.Timestamp = ts
		p.Quality = model.QualityGood
		
		// 添加调试日志
		w.logger.Debug("Modbus数据采集成功",
			zap.String("point", pt.Name),
			zap.Float64("raw", value),
			zap.Any("publish", publishVal),
			zap.String("byte_order", pt.ByteOrder))
		
		bus.Publish(p)
	}
	return nil
}

// readBitBlock 批量读取线圈/离散输入并发布 bool 值。
func (w *slaveWorker) readBitBlock(client *modbus.ModbusClient, blk *ReadBlock, ts int64, bus *broker.Bus) error {
	var coils []bool
	var err error

	if blk.RegType == Coil {
		coils, err = client.ReadCoils(blk.StartAddr, blk.Count)
	} else {
		coils, err = client.ReadDiscreteInputs(blk.StartAddr, blk.Count)
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
		p.ID = w.cfg.ID + "/modbus/" + pt.Name
		p.Value = coils[offset]
		p.Timestamp = ts
		p.Quality = model.QualityGood
		bus.Publish(p)
	}
	return nil
}

// publishDisconnected 对本 Slave 所有测点发布 QualityNotConnected 质量戳，
// 供北向系统感知设备离线状态。
func (w *slaveWorker) publishDisconnected(bus *broker.Bus) {
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

// ─── 寄存器值解析 ──────────────────────────────────────────────────────────────

// parseRegisters 将原始 uint16 寄存器切片解析为 float64（统一工程量）。
// byteOrder: "big"(大端), "little"(小端), "ABCD", "CDAB", "BADC", "DCBA"
// bitPos: 位提取位置(0-15), -1表示不启用
func parseRegisters(regs []uint16, dt DataType, byteOrder string, bitPos int) (float64, error) {
	// 默认大端序
	if byteOrder == "" {
		byteOrder = "big"
	}

	// 先解析原始值
	var rawVal float64
	var err error

	switch dt {
	case Uint16:
		rawVal = float64(regs[0])

	case Int16:
		rawVal = float64(int16(regs[0]))

	case Uint32:
		if len(regs) < 2 {
			return 0, fmt.Errorf("Uint32 需要 2 个寄存器，实际 %d", len(regs))
		}
		var v uint32
		if byteOrder == "little" || byteOrder == "DCBA" {
			// 小端序: 低字在前 (regs[1] 是高字)
			v = uint32(regs[1])<<16 | uint32(regs[0])
		} else {
			// 大端序: 高字在前 (regs[0] 是高字)
			v = uint32(regs[0])<<16 | uint32(regs[1])
		}
		rawVal = float64(v)

	case Int32:
		if len(regs) < 2 {
			return 0, fmt.Errorf("Int32 需要 2 个寄存器，实际 %d", len(regs))
		}
		var v int32
		if byteOrder == "little" || byteOrder == "DCBA" {
			v = int32(uint32(regs[1])<<16 | uint32(regs[0]))
		} else {
			v = int32(uint32(regs[0])<<16 | uint32(regs[1]))
		}
		rawVal = float64(v)

	case Float32:
		if len(regs) < 2 {
			return 0, fmt.Errorf("Float32 需要 2 个寄存器，实际 %d", len(regs))
		}
		rawVal, err = parseFloat32(regs, byteOrder)
		if err != nil {
			return 0, err
		}

	case Float64:
		if len(regs) < 4 {
			return 0, fmt.Errorf("Float64 需要 4 个寄存器，实际 %d", len(regs))
		}
		b := make([]byte, 8)
		for i := 0; i < 4; i++ {
			binary.BigEndian.PutUint16(b[i*2:i*2+2], regs[i])
		}
		bits := binary.BigEndian.Uint64(b)
		rawVal = math.Float64frombits(bits)

	case Bool:
		rawVal = float64(regs[0] & 0x0001)

	default:
		return 0, fmt.Errorf("未知 DataType %d", dt)
	}

	// 位提取处理
	if bitPos >= 0 && bitPos <= 15 {
		// 从uint16中提取指定位
		uintVal := uint16(rawVal)
		bitVal := (uintVal >> bitPos) & 0x0001
		return float64(bitVal), nil
	}

	return rawVal, nil
}

// parseFloat32 解析float32,支持扩展字节序
func parseFloat32(regs []uint16, byteOrder string) (float64, error) {
	b := make([]byte, 4)
	
	switch byteOrder {
	case "big", "ABCD":
		// 标准大端: AB CD EF GH
		binary.BigEndian.PutUint16(b[0:2], regs[0])
		binary.BigEndian.PutUint16(b[2:4], regs[1])
		bits := binary.BigEndian.Uint32(b)
		return float64(math.Float32frombits(bits)), nil
		
	case "little", "DCBA":
		// 标准小端: DC BA HG FE (但在寄存器中是 BA DC FE HG)
		binary.LittleEndian.PutUint16(b[0:2], regs[0])
		binary.LittleEndian.PutUint16(b[2:4], regs[1])
		bits := binary.LittleEndian.Uint32(b)
		return float64(math.Float32frombits(bits)), nil
		
	case "CDAB":
		// 字交换: CD AB GH EF
		// regs[0] = CD AB, regs[1] = GH EF
		// 需要交换每个寄存器内的字节
		b[0] = byte(regs[0] >> 8)   // CD
		b[1] = byte(regs[0] & 0xFF) // AB
		b[2] = byte(regs[1] >> 8)   // GH
		b[3] = byte(regs[1] & 0xFF) // EF
		bits := binary.BigEndian.Uint32(b)
		return float64(math.Float32frombits(bits)), nil
		
	case "BADC":
		// 字节交换: BA DC FE HG
		// regs[0] = AB CD, regs[1] = EF GH
		// 需要交换每个寄存器内的字节
		b[0] = byte(regs[0] & 0xFF) // CD -> DC
		b[1] = byte(regs[0] >> 8)   // AB -> BA
		b[2] = byte(regs[1] & 0xFF) // GH -> HG
		b[3] = byte(regs[1] >> 8)   // EF -> FE
		bits := binary.BigEndian.Uint32(b)
		return float64(math.Float32frombits(bits)), nil
		
	default:
		// 默认大端
		binary.BigEndian.PutUint16(b[0:2], regs[0])
		binary.BigEndian.PutUint16(b[2:4], regs[1])
		bits := binary.BigEndian.Uint32(b)
		return float64(math.Float32frombits(bits)), nil
	}
}

// ─── 指数退避 ──────────────────────────────────────────────────────────────────

// exponentialBackoff 线程不安全的指数退避计时器（每个 slaveWorker 独享一个实例）。
type exponentialBackoff struct {
	current time.Duration
	base    time.Duration
	max     time.Duration
}

func newExponentialBackoff(base, max time.Duration) *exponentialBackoff {
	return &exponentialBackoff{current: base, base: base, max: max}
}

// Next 返回当前退避时间，并将下一次退避时间翻倍（直到 max）。
func (b *exponentialBackoff) Next() time.Duration {
	d := b.current
	b.current *= 2
	if b.current > b.max {
		b.current = b.max
	}
	return d
}

// Reset 将退避重置为初始值（连接成功后调用）。
func (b *exponentialBackoff) Reset() {
	b.current = b.base
}
