// internal/driver/dlt645/handler.go - DL/T 645 数据处理
package dlt645

import (
	"fmt"
	"math"
	"time"

	"github.com/gateway/gateway/internal/model"
	"go.uber.org/zap"
)

// Handler 数据处理器
type Handler struct {
	driver *Driver
	logger *zap.Logger
	config *Config
}

// NewHandler 创建处理器
func NewHandler(driver *Driver, config *Config, logger *zap.Logger) *Handler {
	return &Handler{
		driver: driver,
		config: config,
		logger: logger,
	}
}

// ProcessFrame 处理接收到的帧，返回 PointData（不直接发布，由调用方批量发布）
// 如果返回 nil, nil 表示无需处理
func (h *Handler) ProcessFrame(frame *Frame) (*model.PointData, error) {
	if frame.Type != FrameTypeResponse {
		h.logger.Debug("ignoring non-response frame",
			zap.Uint8("control", frame.C),
		)
		return nil, nil
	}

	// 解析数据
	dataID, values, err := ParseResponse(frame, h.config.ProtocolVersion)
	if err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	h.logger.Debug("收到响应帧",
		zap.String("data_id_decoded", fmt.Sprintf("% X", dataID)),
		zap.String("values_decoded", fmt.Sprintf("% X", values)),
		zap.String("data_raw", fmt.Sprintf("% X", frame.Data)),
	)

	// 构建点表查找键
	key := h.buildKey(frame.Address, dataID)

	h.logger.Debug("查找点表",
		zap.String("key", key),
		zap.String("address_frame", StringAddress(frame.Address)),
	)

	// 调试：打印所有注册的 key
	h.driver.pointMu.RLock()
	if h.logger.Core().Enabled(zap.DebugLevel) {
		var allKeys []string
		for k := range h.driver.pointMap {
			allKeys = append(allKeys, k)
		}
		h.logger.Debug("所有注册的点表key",
			zap.Strings("keys", allKeys),
		)
	}
	point, ok := h.driver.pointMap[key]
	h.driver.pointMu.RUnlock()

	if !ok {
		// 尝试使用广播地址
		broadcast := [6]byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
		key2 := h.buildKey(broadcast, dataID)

		h.driver.pointMu.RLock()
		point, ok = h.driver.pointMap[key2]
		h.driver.pointMu.RUnlock()

		if !ok {
			h.logger.Debug("no point config found",
				zap.String("address", StringAddress(frame.Address)),
				zap.String("data_id", fmt.Sprintf("% X", dataID)),
			)
			return nil, nil
		}
	}

	// 解析数值（统一使用 BCD 格式）
	value := BCD2Float64(values)

	// 应用缩放和偏移
	scaledValue := value*point.Scale + point.Offset

	// 死区过滤（返回 nil 表示被过滤）
	if point.DeadbandValue > 0 {
		if h.shouldFilter(point, scaledValue) {
			return nil, nil
		}
	}

	// 构建 PointData（不发布，由调用方批量发布）
	p := model.GetPoint()
	p.ID = fmt.Sprintf("%s/dlt645/%s", h.driver.config.Name, point.Name)
	p.Value = scaledValue
	p.Timestamp = time.Now().UnixNano()
	p.Quality = model.QualityGood

	h.logger.Debug("data processed",
		zap.String("id", p.ID),
		zap.Float64("value", scaledValue),
		zap.String("unit", point.Unit),
	)

	return p, nil
}

// HandleFrame 兼容旧接口，处理帧并直接发布（不推荐使用）
// 推荐使用 ProcessFrame 替代
func (h *Handler) HandleFrame(frame *Frame) error {
	p, err := h.ProcessFrame(frame)
	if err != nil {
		return err
	}
	if p == nil {
		return nil
	}

	// 直接发布
	h.driver.bus.Publish(p)
	return nil
}

// buildKey 构建点表查找键: 地址_数据标识
// 帧中地址是低字节在前，需要反转回标准格式与点表匹配
// 帧中 DataID 是低字节在前，与点表格式一致，直接使用
func (h *Handler) buildKey(addr [6]byte, dataID []byte) string {
	// 反转地址字节（低字节在前 -> 高字节在前）
	// frame.Address = [00, 18, 01, 05, 20, 01] -> "001801052001"
	addrStr := fmt.Sprintf("%02X%02X%02X%02X%02X%02X",
		addr[5], addr[4], addr[3],
		addr[2], addr[1], addr[0])
	// DataID 字节顺序与 CSV 中 BCD 格式一致，直接使用（低字节在前）
	// CSV "00030200" -> BCD [00,03,02,00]
	dataIDStr := fmt.Sprintf("%02X%02X%02X%02X",
		dataID[0], dataID[1], dataID[2], dataID[3])
	return fmt.Sprintf("%s_%s", addrStr, dataIDStr)
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