// internal/driver/iec103/handler.go - IEC 60870-5-103 数据处理与 SOE 并发处理
package iec103

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gateway/gateway/internal/model"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// SOE 事件定义
// ─────────────────────────────────────────────────────────────────────────────

// SOEEvent SOE 事件
type SOEEvent struct {
	CA        uint8  // 公共地址
	FUN       uint8  // 功能类型
	INF       uint8  // 信息号
	TI        uint8  // 类型标识
	COT       uint8  // 传输原因
	Data      []byte // 原始数据
	Timestamp int64  // 事件时标
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler 数据处理器
// ─────────────────────────────────────────────────────────────────────────────

// Handler IEC103 数据处理器
type Handler struct {
	config Config
	logger *zap.Logger

	// 点表索引（基于 FUN/INF）- O(1) 查找
	pointMap   map[string]*PointConfig
	pointMapMu sync.RWMutex

	// SOE 并发处理
	soeQueue   chan *SOEEvent      // SOE 事件队列
	soeWorkers int                 // Worker 数量
	wg         sync.WaitGroup      // 等待组
	ctx        context.Context
	cancel     context.CancelFunc

	// 数据发布回调
	publishFunc func(point *model.PointData)

	// 统计
	stats HandlerStats
}

// HandlerStats 处理器统计信息
type HandlerStats struct {
	TotalEvents    uint64 // 总事件数
	SOEEvents      uint64 // SOE 事件数
	DroppedEvents  uint64 // 丢弃事件数
	PublishedCount uint64 // 发布计数
}

// NewHandler 创建处理器
func NewHandler(config Config, logger *zap.Logger) *Handler {
	ctx, cancel := context.WithCancel(context.Background())

	return &Handler{
		config:     config,
		logger:     logger,
		pointMap:   make(map[string]*PointConfig),
		soeQueue:   make(chan *SOEEvent, config.SOEQueueSize),
		soeWorkers: config.SOEWorkerCount,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 点表管理（基于 FUN/INF）
// ─────────────────────────────────────────────────────────────────────────────

// BuildPointMap 构建点表索引
// 在驱动初始化时调用，构建 map[string]*PointConfig
// Key 使用 fmt.Sprintf("%d-%d-%d", CA, FUN, INF)，确保 O(1) 查找
func (h *Handler) BuildPointMap(points []PointConfig) {
	h.pointMapMu.Lock()
	defer h.pointMapMu.Unlock()

	// 清空旧索引
	h.pointMap = make(map[string]*PointConfig, len(points))

	// 构建新索引
	for i := range points {
		key := BuildPointKey(points[i].CA, points[i].FUN, points[i].INF)
		h.pointMap[key] = &points[i]
	}

	h.logger.Info("point map built",
		zap.Int("total_points", len(points)),
		zap.Int("map_size", len(h.pointMap)),
	)
}

// FindPoint 查找点位（O(1) 查找）
// 基于 CA + FUN + INF 进行查找
func (h *Handler) FindPoint(ca uint8, fun uint8, inf uint8) (*PointConfig, bool) {
	h.pointMapMu.RLock()
	defer h.pointMapMu.RUnlock()

	key := BuildPointKey(ca, fun, inf)
	point, ok := h.pointMap[key]
	return point, ok
}

// GetPointCount 获取点位数量
func (h *Handler) GetPointCount() int {
	h.pointMapMu.RLock()
	defer h.pointMapMu.RUnlock()
	return len(h.pointMap)
}

// ─────────────────────────────────────────────────────────────────────────────
// SOE 并发处理
// ─────────────────────────────────────────────────────────────────────────────

// Start 启动 SOE 处理 Worker
func (h *Handler) Start() {
	// 启动多个 Worker 并发处理 SOE 事件
	for i := 0; i < h.soeWorkers; i++ {
		h.wg.Add(1)
		go h.soeWorker(i)
	}

	h.logger.Info("SOE handler started",
		zap.Int("workers", h.soeWorkers),
		zap.Int("queue_size", h.config.SOEQueueSize),
	)
}

// Stop 停止 SOE 处理
func (h *Handler) Stop() {
	h.cancel()
	h.wg.Wait()
	close(h.soeQueue)

	h.logger.Info("SOE handler stopped",
		zap.Uint64("total_events", atomic.LoadUint64(&h.stats.TotalEvents)),
		zap.Uint64("soe_events", atomic.LoadUint64(&h.stats.SOEEvents)),
		zap.Uint64("dropped_events", atomic.LoadUint64(&h.stats.DroppedEvents)),
	)
}

// soeWorker SOE 处理 Worker
func (h *Handler) soeWorker(id int) {
	defer h.wg.Done()

	for {
		select {
		case <-h.ctx.Done():
			return
		case event := <-h.soeQueue:
			if event == nil {
				return
			}
			h.processSOEEvent(event)
		}
	}
}

// EnqueueSOE 将 SOE 事件加入队列
func (h *Handler) EnqueueSOE(event *SOEEvent) {
	atomic.AddUint64(&h.stats.TotalEvents, 1)

	select {
	case h.soeQueue <- event:
		atomic.AddUint64(&h.stats.SOEEvents, 1)
	default:
		// 队列满，丢弃事件
		atomic.AddUint64(&h.stats.DroppedEvents, 1)
		h.logger.Warn("SOE queue full, event dropped",
			zap.Uint8("ca", event.CA),
			zap.Uint8("fun", event.FUN),
			zap.Uint8("inf", event.INF),
		)
	}
}

// processSOEEvent 处理 SOE 事件
func (h *Handler) processSOEEvent(event *SOEEvent) {
	// 查找点位（O(1) 查找）
	point, ok := h.FindPoint(event.CA, event.FUN, event.INF)
	if !ok {
		h.logger.Debug("point not found",
			zap.Uint8("ca", event.CA),
			zap.Uint8("fun", event.FUN),
			zap.Uint8("inf", event.INF),
		)
		return
	}

	// 解析数据值
	value, err := h.parseValue(event.TI, event.Data)
	if err != nil {
		h.logger.Warn("parse value failed",
			zap.String("point", point.Name),
			zap.Uint8("ti", event.TI),
			zap.Error(err),
		)
		return
	}

	// 应用缩放和偏移
	value = value*point.Scale + point.Offset

	// 死区过滤
	if h.shouldFilter(point, value) {
		return
	}

	// 发布数据
	h.publishPointData(point, value, event.Timestamp)
}

// ─────────────────────────────────────────────────────────────────────────────
// ASDU 处理
// ─────────────────────────────────────────────────────────────────────────────

// HandleASDU 处理 ASDU
func (h *Handler) HandleASDU(asdu *ASDU) error {
	switch asdu.TI {
	case TI_TIME_SYNC:
		// TI=1: 带时标的消息
		return h.handleTimeSyncMessage(asdu)

	case TI_TIME_SYNC_RELATIVE:
		// TI=2: 带相对时间的时标消息
		return h.handleTimeSyncRelativeMessage(asdu)

	case TI_MEASURED_VALUE_SHORT_TS:
		// TI=9: 带时标的测量值（短浮点数）
		return h.handleMeasuredValueShortTS(asdu)

	case TI_SINGLE_POINT_TS:
		// TI=13: 单点信息带时标
		return h.handleSinglePointTS(asdu)

	case TI_DOUBLE_POINT_TS:
		// TI=14: 双点信息带时标
		return h.handleDoublePointTS(asdu)

	case TI_GENERIC_CLASS_DATA:
		// TI=19: 通用分类数据
		return h.handleGenericClassData(asdu)

	default:
		h.logger.Debug("unsupported TI",
			zap.Uint8("ti", asdu.TI),
			zap.Uint8("fun", asdu.FUN),
			zap.Uint8("inf", asdu.INF),
		)
		return nil
	}
}

// handleTimeSyncMessage 处理 TI=1（带时标的消息）
func (h *Handler) handleTimeSyncMessage(asdu *ASDU) error {
	// 数据格式：SIQ(1) | CP24Time2a(3)
	if len(asdu.Data) < 4 {
		return fmt.Errorf("data too short: %d", len(asdu.Data))
	}

	siq := asdu.Data[0]
	value := float64(siq & 0x01) // SIQ 最低位为状态值
	timestamp := h.parseCP24Time2a(asdu.Data[1:4])

	// 创建 SOE 事件并加入队列
	event := &SOEEvent{
		CA:        asdu.CA,
		FUN:       asdu.FUN,
		INF:       asdu.INF,
		TI:        asdu.TI,
		COT:       asdu.COT,
		Data:      asdu.Data,
		Timestamp: timestamp,
	}
	h.EnqueueSOE(event)

	// 直接处理（非 SOE 模式）
	h.processData(asdu.CA, asdu.FUN, asdu.INF, value, timestamp)

	return nil
}

// handleTimeSyncRelativeMessage 处理 TI=2（带相对时间的时标消息）
func (h *Handler) handleTimeSyncRelativeMessage(asdu *ASDU) error {
	// 数据格式：SIQ(1) | CP16Time2a(2) | CP24Time2a(3)
	if len(asdu.Data) < 6 {
		return fmt.Errorf("data too short: %d", len(asdu.Data))
	}

	siq := asdu.Data[0]
	value := float64(siq & 0x01)
	relativeTime := binary.LittleEndian.Uint16(asdu.Data[1:3])
	timestamp := h.parseCP24Time2a(asdu.Data[3:6])

	// 计算实际时标（基准时标 + 相对时间）
	timestamp = timestamp + int64(relativeTime)

	h.processData(asdu.CA, asdu.FUN, asdu.INF, value, timestamp)

	return nil
}

// handleMeasuredValueShortTS 处理 TI=9（带时标的测量值）
func (h *Handler) handleMeasuredValueShortTS(asdu *ASDU) error {
	// 数据格式：IEEE-754 Float(4) | QDS(1) | CP24Time2a(3)
	if len(asdu.Data) < 8 {
		return fmt.Errorf("data too short: %d", len(asdu.Data))
	}

	// 解析 IEEE-754 短浮点数
	value := float64(binary.LittleEndian.Uint32(asdu.Data[0:4]))
	qds := asdu.Data[4]
	timestamp := h.parseCP24Time2a(asdu.Data[5:8])

	// 检查质量描述
	if (qds & 0x80) != 0 {
		// IV = 1，值无效
		h.logger.Debug("invalid value",
			zap.Uint8("ca", asdu.CA),
			zap.Uint8("fun", asdu.FUN),
			zap.Uint8("inf", asdu.INF),
		)
		return nil
	}

	h.processData(asdu.CA, asdu.FUN, asdu.INF, value, timestamp)

	return nil
}

// handleSinglePointTS 处理 TI=13（单点信息带时标）
func (h *Handler) handleSinglePointTS(asdu *ASDU) error {
	// 数据格式：SIQ(1) | CP24Time2a(3)
	if len(asdu.Data) < 4 {
		return fmt.Errorf("data too short: %d", len(asdu.Data))
	}

	siq := asdu.Data[0]
	value := float64(siq & 0x01)
	timestamp := h.parseCP24Time2a(asdu.Data[1:4])

	// SOE 事件
	if asdu.COT == COT_SPONTANEOUS {
		event := &SOEEvent{
			CA:        asdu.CA,
			FUN:       asdu.FUN,
			INF:       asdu.INF,
			TI:        asdu.TI,
			COT:       asdu.COT,
			Data:      asdu.Data,
			Timestamp: timestamp,
		}
		h.EnqueueSOE(event)
	}

	h.processData(asdu.CA, asdu.FUN, asdu.INF, value, timestamp)

	return nil
}

// handleDoublePointTS 处理 TI=14（双点信息带时标）
func (h *Handler) handleDoublePointTS(asdu *ASDU) error {
	// 数据格式：DIQ(1) | CP24Time2a(3)
	if len(asdu.Data) < 4 {
		return fmt.Errorf("data too short: %d", len(asdu.Data))
	}

	diq := asdu.Data[0]
	value := float64(diq & 0x03) // DIQ 低两位为状态值
	timestamp := h.parseCP24Time2a(asdu.Data[1:4])

	// SOE 事件
	if asdu.COT == COT_SPONTANEOUS {
		event := &SOEEvent{
			CA:        asdu.CA,
			FUN:       asdu.FUN,
			INF:       asdu.INF,
			TI:        asdu.TI,
			COT:       asdu.COT,
			Data:      asdu.Data,
			Timestamp: timestamp,
		}
		h.EnqueueSOE(event)
	}

	h.processData(asdu.CA, asdu.FUN, asdu.INF, value, timestamp)

	return nil
}

// handleGenericClassData 处理 TI=19（通用分类数据）
// 预留接口，适配不同厂家的保护定值读取
func (h *Handler) handleGenericClassData(asdu *ASDU) error {
	// 通用分类数据格式：
	// KOD(1) | GDD(3) | GID(可变) | Data(可变)
	//
	// KOD: 描述类别
	// GDD: 通用数据描述
	// GID: 通用数据标识
	// Data: 数据内容

	if len(asdu.Data) < 4 {
		return fmt.Errorf("data too short: %d", len(asdu.Data))
	}

	kod := asdu.Data[0] // 描述类别

	h.logger.Info("generic class data received",
		zap.Uint8("ca", asdu.CA),
		zap.Uint8("fun", asdu.FUN),
		zap.Uint8("inf", asdu.INF),
		zap.Uint8("kod", kod),
		zap.Int("data_len", len(asdu.Data)),
	)

	// TODO: 根据不同厂家的实现解析通用分类数据
	// 这里预留接口，由具体厂家适配实现

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 时标解析
// ─────────────────────────────────────────────────────────────────────────────

// parseCP24Time2a 解析 CP24Time2a 时标（3 字节）
func (h *Handler) parseCP24Time2a(data []byte) int64 {
	if len(data) < 3 {
		return time.Now().UnixMilli()
	}

	msec := int(binary.LittleEndian.Uint16(data[0:2]))
	minutes := int(data[2] & 0x3F)

	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(),
		now.Hour(), minutes, 0, msec*1000000, time.Local).UnixMilli()
}

// parseCP56Time2a 解析 CP56Time2a 时标（7 字节）
func (h *Handler) parseCP56Time2a(data []byte) int64 {
	if len(data) < 7 {
		return time.Now().UnixMilli()
	}

	msec := int(binary.LittleEndian.Uint16(data[0:2]))
	minutes := int(data[2] & 0x3F)
	hours := int(data[3] & 0x1F)
	day := int(data[4] & 0x1F)
	month := int(data[5] & 0x0F)
	year := int(data[6] & 0x7F) + 2000

	return time.Date(year, time.Month(month), day,
		hours, minutes, 0, msec*1000000, time.Local).UnixMilli()
}

// ─────────────────────────────────────────────────────────────────────────────
// 数据处理辅助方法
// ─────────────────────────────────────────────────────────────────────────────

// parseValue 解析数据值
func (h *Handler) parseValue(ti uint8, data []byte) (float64, error) {
	switch ti {
	case TI_TIME_SYNC, TI_TIME_SYNC_RELATIVE, TI_SINGLE_POINT_TS:
		if len(data) < 1 {
			return 0, fmt.Errorf("data too short")
		}
		return float64(data[0] & 0x01), nil

	case TI_DOUBLE_POINT_TS:
		if len(data) < 1 {
			return 0, fmt.Errorf("data too short")
		}
		return float64(data[0] & 0x03), nil

	case TI_MEASURED_VALUE_SHORT_TS:
		if len(data) < 4 {
			return 0, fmt.Errorf("data too short")
		}
		return float64(binary.LittleEndian.Uint32(data[0:4])), nil

	default:
		return 0, fmt.Errorf("unsupported TI: %d", ti)
	}
}

// processData 处理数据
func (h *Handler) processData(ca uint8, fun uint8, inf uint8, value float64, timestamp int64) {
	// 查找点位（O(1) 查找）
	point, ok := h.FindPoint(ca, fun, inf)
	if !ok {
		return
	}

	// 应用缩放和偏移
	value = value*point.Scale + point.Offset

	// 死区过滤
	if h.shouldFilter(point, value) {
		return
	}

	// 发布数据
	h.publishPointData(point, value, timestamp)
}

// shouldFilter 死区过滤
func (h *Handler) shouldFilter(point *PointConfig, value float64) bool {
	if point.DeadbandValue == 0 {
		return false
	}

	var diff float64
	if point.DeadbandType == DeadbandPercent {
		// 百分比死区
		if point.lastValue != 0 {
			diff = (value - point.lastValue) / point.lastValue * 100
		}
	} else {
		// 绝对值死区
		diff = value - point.lastValue
	}

	if diff < 0 {
		diff = -diff
	}

	return diff < point.DeadbandValue
}

// publishPointData 发布点位数据
func (h *Handler) publishPointData(point *PointConfig, value float64, timestamp int64) {
	// 更新缓存
	point.lastValue = value
	point.lastTimestamp = timestamp

	// 使用对象池获取 PointData
	data := model.GetPoint()
	data.ID = point.Name
	data.Value = value
	data.Timestamp = timestamp
	data.Quality = model.QualityGood

	// 发布
	if h.publishFunc != nil {
		h.publishFunc(data)
	}

	atomic.AddUint64(&h.stats.PublishedCount, 1)
}

// SetPublishFunc 设置发布回调
func (h *Handler) SetPublishFunc(fn func(point *model.PointData)) {
	h.publishFunc = fn
}

// GetStats 获取统计信息
func (h *Handler) GetStats() HandlerStats {
	return HandlerStats{
		TotalEvents:    atomic.LoadUint64(&h.stats.TotalEvents),
		SOEEvents:      atomic.LoadUint64(&h.stats.SOEEvents),
		DroppedEvents:  atomic.LoadUint64(&h.stats.DroppedEvents),
		PublishedCount: atomic.LoadUint64(&h.stats.PublishedCount),
	}
}
