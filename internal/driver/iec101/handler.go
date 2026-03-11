// internal/driver/iec101/handler.go - ASDU 解析与数据分发
package iec101

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
	// 单点信息
	case M_SP_NA_1, M_SP_TA_1:
		return h.handleSinglePoint(asdu)

	// 双点信息
	case M_DP_NA_1, M_DP_TA_1:
		return h.handleDoublePoint(asdu)

	// 步位置信息
	case M_ST_NA_1, M_ST_TA_1:
		return h.handleStepPosition(asdu)

	// 32位比特串
	case M_BO_NA_1, M_BO_TA_1:
		return h.handleBitString32(asdu)

	// 测量值归一化值
	case M_ME_NA_1, M_ME_TA_1:
		return h.handleMeasuredValueNormal(asdu)

	// 测量值标度化值
	case M_ME_NB_1, M_ME_TB_1:
		return h.handleMeasuredValueScaled(asdu)

	// 测量值短浮点数
	case M_ME_NC_1, M_ME_TC_1:
		return h.handleMeasuredValueFloat(asdu)

	// 累积量
	case M_IT_NA_1, M_IT_TA_1:
		return h.handleIntegratedTotals(asdu)

	// 总召唤命令响应
	case C_IC_NA_1:
		return h.handleInterrogationResponse(asdu)

	default:
		h.logger.Debug("unsupported ASDU type",
			zap.Uint8("type_id", asdu.TypeID),
			zap.Uint8("cot", asdu.COT),
		)
		return nil
	}
}

// handleSinglePoint 处理单点信息
func (h *Handler) handleSinglePoint(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var value bool
		var qds uint8

		if isSequence && i > 0 {
			// 序列模式：后续信息对象只有 SIQ
			ioa = baseIOA + uint16(i)
			siq := asdu.InfoObj[offset]
			value = (siq & 0x01) != 0
			qds = siq & 0xFC
			offset++
		} else {
			// 非序列模式或第一个信息对象
			if len(asdu.InfoObj) < offset+4 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			siq := asdu.InfoObj[offset+2]
			value = (siq & 0x01) != 0
			qds = siq & 0xFC
			offset += 3

			// 带时标
			if asdu.TypeID == M_SP_TA_1 {
				// 跳过 CP24Time2a (3 字节)
				offset += 3
			}

			if i == 0 {
				baseIOA = ioa
			}
		}

		// 发布数据
		h.publishPointData(asdu.CA, ioa, func() float64 {
			if value {
				return 1.0
			}
			return 0.0
		}, qds)
	}

	return nil
}

// handleDoublePoint 处理双点信息
func (h *Handler) handleDoublePoint(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var value uint8
		var qds uint8

		if isSequence && i > 0 {
			ioa = baseIOA + uint16(i)
			diq := asdu.InfoObj[offset]
			value = diq & 0x03
			qds = diq & 0xFC
			offset++
		} else {
			if len(asdu.InfoObj) < offset+4 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			diq := asdu.InfoObj[offset+2]
			value = diq & 0x03
			qds = diq & 0xFC
			offset += 3

			if asdu.TypeID == M_DP_TA_1 {
				offset += 3
			}

			if i == 0 {
				baseIOA = ioa
			}
		}

		h.publishPointData(asdu.CA, ioa, func() float64 {
			return float64(value)
		}, qds)
	}

	return nil
}

// handleStepPosition 处理步位置信息
func (h *Handler) handleStepPosition(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var value int8
		var qds uint8

		if isSequence && i > 0 {
			ioa = baseIOA + uint16(i)
			vti := asdu.InfoObj[offset]
			value = int8(vti & 0x7F)
			if vti&0x80 != 0 {
				value = -value
			}
			qds = asdu.InfoObj[offset+1]
			offset += 2
		} else {
			if len(asdu.InfoObj) < offset+5 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			vti := asdu.InfoObj[offset+2]
			value = int8(vti & 0x7F)
			if vti&0x80 != 0 {
				value = -value
			}
			qds = asdu.InfoObj[offset+3]
			offset += 4

			if asdu.TypeID == M_ST_TA_1 {
				offset += 3
			}

			if i == 0 {
				baseIOA = ioa
			}
		}

		h.publishPointData(asdu.CA, ioa, func() float64 {
			return float64(value)
		}, qds)
	}

	return nil
}

// handleBitString32 处理 32 位比特串
func (h *Handler) handleBitString32(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var value uint32
		var qds uint8

		if isSequence && i > 0 {
			ioa = baseIOA + uint16(i)
			value = binary.LittleEndian.Uint32(asdu.InfoObj[offset : offset+4])
			qds = asdu.InfoObj[offset+4]
			offset += 5
		} else {
			if len(asdu.InfoObj) < offset+7 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			value = binary.LittleEndian.Uint32(asdu.InfoObj[offset+2 : offset+6])
			qds = asdu.InfoObj[offset+6]
			offset += 7

			if asdu.TypeID == M_BO_TA_1 {
				offset += 3
			}

			if i == 0 {
				baseIOA = ioa
			}
		}

		h.publishPointData(asdu.CA, ioa, func() float64 {
			return float64(value)
		}, qds)
	}

	return nil
}

// handleMeasuredValueNormal 处理测量值归一化值
func (h *Handler) handleMeasuredValueNormal(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var value int16
		var qds uint8

		if isSequence && i > 0 {
			ioa = baseIOA + uint16(i)
			value = int16(binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2]))
			qds = asdu.InfoObj[offset+2]
			offset += 3
		} else {
			if len(asdu.InfoObj) < offset+5 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			value = int16(binary.LittleEndian.Uint16(asdu.InfoObj[offset+2 : offset+4]))
			qds = asdu.InfoObj[offset+4]
			offset += 5

			if asdu.TypeID == M_ME_TA_1 {
				offset += 3
			}

			if i == 0 {
				baseIOA = ioa
			}
		}

		h.publishPointData(asdu.CA, ioa, func() float64 {
			// 归一化值转换为百分比
			return float64(value) / 32768.0 * 100.0
		}, qds)
	}

	return nil
}

// handleMeasuredValueScaled 处理测量值标度化值
func (h *Handler) handleMeasuredValueScaled(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var value int16
		var qds uint8

		if isSequence && i > 0 {
			ioa = baseIOA + uint16(i)
			value = int16(binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2]))
			qds = asdu.InfoObj[offset+2]
			offset += 3
		} else {
			if len(asdu.InfoObj) < offset+5 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			value = int16(binary.LittleEndian.Uint16(asdu.InfoObj[offset+2 : offset+4]))
			qds = asdu.InfoObj[offset+4]
			offset += 5

			if asdu.TypeID == M_ME_TB_1 {
				offset += 3
			}

			if i == 0 {
				baseIOA = ioa
			}
		}

		h.publishPointData(asdu.CA, ioa, func() float64 {
			return float64(value)
		}, qds)
	}

	return nil
}

// handleMeasuredValueFloat 处理测量值短浮点数
func (h *Handler) handleMeasuredValueFloat(asdu *ASDU) error {
	infoObjCount := asdu.GetInfoObjCount()
	isSequence := asdu.IsSequence()

	offset := 0
	var baseIOA uint16

	for i := 0; i < infoObjCount; i++ {
		var ioa uint16
		var value float32
		var qds uint8

		if isSequence && i > 0 {
			ioa = baseIOA + uint16(i)
			value = math.Float32frombits(binary.LittleEndian.Uint32(asdu.InfoObj[offset : offset+4]))
			qds = asdu.InfoObj[offset+4]
			offset += 5
		} else {
			if len(asdu.InfoObj) < offset+7 {
				break
			}
			ioa = binary.LittleEndian.Uint16(asdu.InfoObj[offset : offset+2])
			value = math.Float32frombits(binary.LittleEndian.Uint32(asdu.InfoObj[offset+2 : offset+6]))
			qds = asdu.InfoObj[offset+6]
			offset += 7

			if asdu.TypeID == M_ME_TC_1 {
				offset += 3
			}

			if i == 0 {
				baseIOA = ioa
			}
		}

		h.publishPointData(asdu.CA, ioa, func() float64 {
			return float64(value)
		}, qds)
	}

	return nil
}

// handleIntegratedTotals 处理累积量
func (h *Handler) handleIntegratedTotals(asdu *ASDU) error {
	// 累积量处理逻辑类似，此处省略
	return nil
}

// handleInterrogationResponse 处理总召唤响应
func (h *Handler) handleInterrogationResponse(asdu *ASDU) error {
	h.logger.Info("interrogation response received",
		zap.Uint8("cot", asdu.COT),
		zap.Uint8("ca", asdu.CA),
	)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 数据发布
// ─────────────────────────────────────────────────────────────────────────────

// publishPointData 发布点数据
func (h *Handler) publishPointData(ca uint8, ioa uint16, getValue func() float64, qds uint8) {
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

	// 获取原始值
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
	p.ID = fmt.Sprintf("%s/iec101/%s", h.driver.config.Name, point.Name)
	p.Value = scaledValue
	p.Timestamp = time.Now().UnixNano()
	p.Quality = quality

	// 发布数据
	h.driver.bus.Publish(p)

	h.logger.Debug("point data published",
		zap.String("id", p.ID),
		zap.Float64("value", scaledValue),
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
