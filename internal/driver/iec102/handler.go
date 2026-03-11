// internal/driver/iec102/handler.go - 电能量 ASDU 解析与数据分发
package iec102

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/gateway/gateway/internal/model"
	"go.uber.org/zap"
)

// Handler ASDU 处理器
type Handler struct {
	driver *Driver
	logger *zap.Logger
}

// NewHandler 创建处理器
func NewHandler(driver *Driver, logger *zap.Logger) *Handler {
	return &Handler{
		driver: driver,
		logger: logger,
	}
}

// HandleASDU 处理 ASDU
func (h *Handler) HandleASDU(asdu *ASDU) error {
	// 根据类型标识分发处理
	switch asdu.TypeID {
	// 累计值（Integrated Totals）- IEC102 核心数据类型
	case M_IT_NA_1:
		// 累计值（不带时标）
		return h.handleIntegratedTotals(asdu)

	case M_IT_TA_1:
		// 累计值带时标 CP24Time2a
		return h.handleIntegratedTotalsWithTime24(asdu)

	case M_IT_NB_1:
		// 累计值带时标 CP24Time2a（扩展）
		return h.handleIntegratedTotalsWithTime24(asdu)

	case M_IT_TB_1:
		// 累计值带时标 CP56Time2a
		return h.handleIntegratedTotalsWithTime56(asdu)

	case M_IT_NC_1, M_IT_TC_1:
		// 累计值带时标 CP56Time2a（扩展）
		return h.handleIntegratedTotalsWithTime56(asdu)

	// 计数量召唤命令响应
	case C_CI_NA_1:
		return h.handleCounterInterrogationResponse(asdu)

	default:
		h.logger.Debug("unsupported ASDU type",
			zap.Uint8("type_id", asdu.TypeID),
			zap.Uint8("cot", asdu.COT),
		)
		return nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 电能量累计值处理（IEC102 核心）
// ─────────────────────────────────────────────────────────────────────────────

// handleIntegratedTotals 处理累计值（不带时标）
// 信息对象格式：IOA(2) | CounterReading(4) | SequenceNumber(2) | QDS(1)
func (h *Handler) handleIntegratedTotals(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var counterReading uint32
		var sequenceNumber uint16
		var qds uint8

		if isSequence && i > 0 {
			// 序列模式：后续信息对象只有数据部分
			ioa = baseIOA + uint16(i)
			if len(asdu.InfoObj) < offset+7 {
				break
			}
			counterReading = binary.LittleEndian.Uint32(asdu.InfoObj[offset : offset+4])
			sequenceNumber = binary.LittleEndian.Uint16(asdu.InfoObj[offset+4 : offset+6])
			qds = asdu.InfoObj[offset+6]
			offset += 7
		} else {
			// 非序列模式或第一个信息对象
			if len(asdu.InfoObj) < offset+9 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			counterReading = binary.LittleEndian.Uint32(asdu.InfoObj[offset+2 : offset+6])
			sequenceNumber = binary.LittleEndian.Uint16(asdu.InfoObj[offset+6 : offset+8])
			qds = asdu.InfoObj[offset+8]
			offset += 9

			if i == 0 {
				baseIOA = ioa
			}
		}

		// 发布电能量数据（使用 float64 保证精度）
		h.publishEnergyData(asdu.CA, ioa, func() float64 {
			return float64(counterReading)
		}, sequenceNumber, qds, time.Now())
	}

	return nil
}

// handleIntegratedTotalsWithTime24 处理累计值带时标 CP24Time2a
// 信息对象格式：IOA(2) | CounterReading(4) | SequenceNumber(2) | QDS(1) | CP24Time2a(3)
func (h *Handler) handleIntegratedTotalsWithTime24(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var counterReading uint32
		var sequenceNumber uint16
		var qds uint8
		var timestamp time.Time

		if isSequence && i > 0 {
			ioa = baseIOA + uint16(i)
			if len(asdu.InfoObj) < offset+10 {
				break
			}
			counterReading = binary.LittleEndian.Uint32(asdu.InfoObj[offset : offset+4])
			sequenceNumber = binary.LittleEndian.Uint16(asdu.InfoObj[offset+4 : offset+6])
			qds = asdu.InfoObj[offset+6]
			timestamp = h.parseCP24Time2a(asdu.InfoObj[offset+7 : offset+10])
			offset += 10
		} else {
			if len(asdu.InfoObj) < offset+12 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			counterReading = binary.LittleEndian.Uint32(asdu.InfoObj[offset+2 : offset+6])
			sequenceNumber = binary.LittleEndian.Uint16(asdu.InfoObj[offset+6 : offset+8])
			qds = asdu.InfoObj[offset+8]
			timestamp = h.parseCP24Time2a(asdu.InfoObj[offset+9 : offset+12])
			offset += 12

			if i == 0 {
				baseIOA = ioa
			}
		}

		h.publishEnergyData(asdu.CA, ioa, func() float64 {
			return float64(counterReading)
		}, sequenceNumber, qds, timestamp)
	}

	return nil
}

// handleIntegratedTotalsWithTime56 处理累计值带时标 CP56Time2a
// 信息对象格式：IOA(2) | CounterReading(4) | SequenceNumber(2) | QDS(1) | CP56Time2a(7)
func (h *Handler) handleIntegratedTotalsWithTime56(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var counterReading uint32
		var sequenceNumber uint16
		var qds uint8
		var timestamp time.Time

		if isSequence && i > 0 {
			ioa = baseIOA + uint16(i)
			if len(asdu.InfoObj) < offset+14 {
				break
			}
			counterReading = binary.LittleEndian.Uint32(asdu.InfoObj[offset : offset+4])
			sequenceNumber = binary.LittleEndian.Uint16(asdu.InfoObj[offset+4 : offset+6])
			qds = asdu.InfoObj[offset+6]
			timestamp = h.parseCP56Time2a(asdu.InfoObj[offset+7 : offset+14])
			offset += 14
		} else {
			if len(asdu.InfoObj) < offset+16 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			counterReading = binary.LittleEndian.Uint32(asdu.InfoObj[offset+2 : offset+6])
			sequenceNumber = binary.LittleEndian.Uint16(asdu.InfoObj[offset+6 : offset+8])
			qds = asdu.InfoObj[offset+8]
			timestamp = h.parseCP56Time2a(asdu.InfoObj[offset+9 : offset+16])
			offset += 16

			if i == 0 {
				baseIOA = ioa
			}
		}

		h.publishEnergyData(asdu.CA, ioa, func() float64 {
			return float64(counterReading)
		}, sequenceNumber, qds, timestamp)
	}

	return nil
}

// handleCounterInterrogationResponse 处理计数量召唤响应
func (h *Handler) handleCounterInterrogationResponse(asdu *ASDU) error {
	h.logger.Info("counter interrogation response received",
		zap.Uint8("cot", asdu.COT),
		zap.Uint8("ca", asdu.CA),
	)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 时标解析
// ─────────────────────────────────────────────────────────────────────────────

// parseCP24Time2a 解析 CP24Time2a 时标（3 字节）
// 格式：毫秒(2) | 分钟(1)
func (h *Handler) parseCP24Time2a(data []byte) time.Time {
	if len(data) < 3 {
		return time.Now()
	}

	msec := binary.LittleEndian.Uint16(data[0:2])
	minutes := int(data[2] & 0x3F)

	now := time.Now()
	return time.Date(
		now.Year(), now.Month(), now.Day(),
		now.Hour(), minutes, 0, int(msec)*1000000,
		time.Local,
	)
}

// parseCP56Time2a 解析 CP56Time2a 时标（7 字节）
// 格式：毫秒(2) | 分钟(1) | 小时(1) | 日(1) | 月(1) | 年(1)
func (h *Handler) parseCP56Time2a(data []byte) time.Time {
	if len(data) < 7 {
		return time.Now()
	}

	msec := binary.LittleEndian.Uint16(data[0:2])
	minutes := int(data[2] & 0x3F)
	hours := int(data[3] & 0x1F)
	day := int(data[4] & 0x1F)
	month := int(data[5] & 0x0F)
	year := int(data[6] & 0x7F) + 2000 // IEC102 年份从 2000 年开始

	return time.Date(
		year, time.Month(month), day,
		hours, minutes, 0, int(msec)*1000000,
		time.Local,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// 数据发布
// ─────────────────────────────────────────────────────────────────────────────

// publishEnergyData 发布电能量数据
func (h *Handler) publishEnergyData(ca uint8, ioa uint16, getValue func() float64, seqNum uint16, qds uint8, timestamp time.Time) {
	// 构建点表 key：CA + IOA
	key := fmt.Sprintf("%d/%d", ca, ioa)

	// O(1) 查找点表配置
	h.driver.pointMu.RLock()
	point, ok := h.driver.pointMap[key]
	h.driver.pointMu.RUnlock()

	if !ok {
		h.logger.Debug("point not found in mapping",
			zap.String("key", key),
		)
		return
	}

	// 获取原始值（float64 保证精度）
	value := getValue()

	// 应用 Scale 和 Offset
	scaledValue := value*point.Scale + point.Offset

	// 死区过滤
	if point.DeadbandValue > 0 {
		if h.shouldFilter(point, scaledValue) {
			return
		}
	}

	// 映射质量码
	quality := h.mapQuality(qds)

	// 使用 sync.Pool 获取 PointData
	p := model.GetPoint()
	p.ID = fmt.Sprintf("%s/iec102/%s", h.driver.config.Name, point.Name)
	p.Value = scaledValue // float64 保证电量精度
	p.Timestamp = timestamp.UnixNano()
	p.Quality = quality

	// 发布数据
	h.driver.bus.Publish(p)

	h.logger.Debug("energy data published",
		zap.String("id", p.ID),
		zap.Float64("value", scaledValue),
		zap.Uint16("seq_num", seqNum),
	)
}

// shouldFilter 判断是否应该被死区过滤
func (h *Handler) shouldFilter(point *PointConfig, value float64) bool {
	// 第一次采集，不过滤
	if point.lastTimestamp == 0 {
		point.lastValue = value
		point.lastTimestamp = time.Now().UnixNano()
		return false
	}

	threshold := point.DeadbandValue
	if point.DeadbandType == DeadbandPercent {
		// 百分比死区
		threshold = math.Abs(point.lastValue) * point.DeadbandValue / 100.0
	}

	// 计算变化量
	delta := math.Abs(value - point.lastValue)
	if delta < threshold {
		return true // 变化量小于阈值，过滤
	}

	// 更新上一次的值
	point.lastValue = value
	point.lastTimestamp = time.Now().UnixNano()
	return false
}

// mapQuality 映射质量码
func (h *Handler) mapQuality(qds uint8) uint8 {
	// QDS 格式：IV NT SB BL 0 0 0 0
	// IV=1: 无效
	// NT=1: 非当前
	// SB=1: 被取代
	// BL=1: 被封锁

	if qds&0x80 != 0 { // IV=1
		return model.QualityBad
	}
	if qds&0x40 != 0 { // NT=1
		return model.QualityUncertain
	}
	return model.QualityGood
}
