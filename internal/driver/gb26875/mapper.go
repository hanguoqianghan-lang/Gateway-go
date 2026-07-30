// internal/driver/gb26875/mapper.go - GB/T 26875.3 上行 ADU → PointData 映射
package gb26875

import (
	"fmt"
	"time"

	"github.com/gateway/gateway/internal/model"
	"go.uber.org/zap"
)

// processUploadADU 处理上行 ADU 并发布匹配测点
// 返回：已发布的测点数
func (d *Driver) processUploadADU(f *Frame, adu *ADU) int {
	srcAddrStr := StringAddr(f.SrcAddr)
	logger := d.logger.With(
		zap.String("src", srcAddrStr),
		zap.Uint8("type", adu.Type),
		zap.Uint8("count", adu.ObjectCount),
	)

	switch adu.Type {
	case TypeUploadSystemStatus:
		return d.mapSystemStatus(f, adu, logger)
	case TypeUploadComponentStatus:
		return d.mapComponentStatus(f, adu, logger)
	case TypeUploadComponentAnalog:
		return d.mapComponentAnalog(f, adu, logger)
	case TypeUploadOperationInfo:
		return d.mapOperationInfo(f, adu, logger)
	case TypeUploadSWVersion, TypeUploadTransmissionSWVer:
		return d.mapSoftwareVersion(f, adu, logger)
	case TypeUploadTransmissionDeviceStatus:
		return d.mapTransmissionDeviceStatus(f, adu, logger)
	default:
		logger.Debug("未实现的上行类型",
			zap.Uint8("type", adu.Type),
		)
		return 0
	}
}

// mapSystemStatus 映射系统状态（类型1）
func (d *Driver) mapSystemStatus(f *Frame, adu *ADU, logger *zap.Logger) int {
	srcAddrStr := StringAddr(f.SrcAddr)
	published := 0

	// ADU 头部之后连续 N 个系统状态对象
	offset := 0
	for i := 0; i < int(adu.ObjectCount); i++ {
		if offset+4 > len(adu.Objects) {
			break
		}
		obj, err := ParseSystemStatus(adu.Objects[offset:])
		if err != nil {
			logger.Warn("系统状态解析失败", zap.Error(err))
			break
		}
		// 每个对象 4 字节（不含时间），含时间则 10 字节
		consumed := 4
		if offset+10 <= len(adu.Objects) {
			consumed = 10
		}

		// 点表查找：key = (type<<40)|(sysaddr<<32)|(0<<24)|0
		key := uint64(adu.Type)<<40 | uint64(obj.SystemAddress)<<32
		d.pointMu.RLock()
		pt, ok := d.pointMap[key]
		d.pointMu.RUnlock()
		if !ok {
			// 尝试通配点（无 ComponentAddr）
			d.pointMu.RLock()
			for _, wpt := range d.wildPoints {
				if wpt.MessageType != adu.Type || wpt.SystemAddress != 0 {
					continue
				}
				if wpt.DeviceAddress != "" && wpt.DeviceAddress != srcAddrStr {
					continue
				}
				value := float64(obj.StatusData)*wpt.Scale + wpt.Offset
				if d.publishPoint(f, wpt, value, !obj.Time.IsZero(), obj.Time, logger) {
					published++
				}
			}
			d.pointMu.RUnlock()
		} else {
			if pt.DeviceAddress == "" || pt.DeviceAddress == srcAddrStr {
				// 仅系统状态等整型原始值在此应用线性缩放（模拟量不走这里）
				value := float64(obj.StatusData)*pt.Scale + pt.Offset
				if d.publishPoint(f, pt, value, !obj.Time.IsZero(), obj.Time, logger) {
					published++
				}
			}
		}

		offset += consumed
	}

	return published
}

// mapComponentStatus 映射部件运行状态（类型2）
func (d *Driver) mapComponentStatus(f *Frame, adu *ADU, logger *zap.Logger) int {
	srcAddrStr := StringAddr(f.SrcAddr)
	published := 0

	offset := 0
	for i := 0; i < int(adu.ObjectCount); i++ {
		if offset+ComponentStatusLen > len(adu.Objects) {
			break
		}
		obj, err := ParseComponentStatus(adu.Objects[offset:])
		if err != nil {
			logger.Warn("部件运行状态解析失败",
				zap.Int("object_index", i),
				zap.Error(err),
			)
			break
		}
		consumed := ComponentStatusLen
		hasTime := false
		if offset+ComponentStatusLen+6 <= len(adu.Objects) {
			// 是否有追加时间
			if obj.Time[0] != 0 || obj.Time[5] != 0 {
				consumed += 6
				hasTime = true
			}
		}

		// 部件地址 raw（4字节 → uint32 小端）
		rawAddr := uint32(obj.ComponentAddr[0]) |
			uint32(obj.ComponentAddr[1])<<8 |
			uint32(obj.ComponentAddr[2])<<16 |
			uint32(obj.ComponentAddr[3])<<24

		key := uint64(adu.Type)<<40 | uint64(obj.SystemAddress)<<32 |
			uint64(obj.ComponentType)<<24 | uint64(rawAddr)

		d.pointMu.RLock()
		pt, ok := d.pointMap[key]
		d.pointMu.RUnlock()

		if ok {
			if pt.DeviceAddress == "" || pt.DeviceAddress == srcAddrStr {
				// 运行状态作为 Value（uint16 → float64），应用线性缩放（整型原始值）
				value := float64(obj.RunStatus)*pt.Scale + pt.Offset
				if d.publishPoint(f, pt, value, hasTime, obj.Time, logger) {
					published++
				}
			}
		} else {
						logger.Debug("部件状态未匹配到点表",
				zap.String("comp_addr", stringAddr4(obj.ComponentAddr)),
				zap.Uint8("comp_type", obj.ComponentType),
				zap.Uint16("run_status", obj.RunStatus),
			)
		}

		offset += consumed
	}

	return published
}

// mapComponentAnalog 映射部件模拟量值（类型3）
func (d *Driver) mapComponentAnalog(f *Frame, adu *ADU, logger *zap.Logger) int {
	srcAddrStr := StringAddr(f.SrcAddr)
	published := 0

	offset := 0
	for i := 0; i < int(adu.ObjectCount); i++ {
		if offset+ComponentAnalogLen > len(adu.Objects) {
			break
		}
		obj, err := ParseComponentAnalog(adu.Objects[offset:])
		if err != nil {
			logger.Warn("部件模拟量解析失败",
				zap.Int("object_index", i),
				zap.Error(err),
			)
			break
		}
		consumed := ComponentAnalogLen

		rawAddr := uint32(obj.ComponentAddr[0]) |
			uint32(obj.ComponentAddr[1])<<8 |
			uint32(obj.ComponentAddr[2])<<16 |
			uint32(obj.ComponentAddr[3])<<24

		key := uint64(adu.Type)<<40 | uint64(obj.SystemAddress)<<32 |
			uint64(obj.ComponentType)<<24 | uint64(rawAddr)

		d.pointMu.RLock()
		pt, ok := d.pointMap[key]
		d.pointMu.RUnlock()

		if ok {
			if pt.DeviceAddress == "" || pt.DeviceAddress == srcAddrStr {
				// 模拟量工程值 = ScaledValue()（已乘内置 AnalogScale），仅追加 pt.Offset，
				// 不再二次乘 pt.Scale（避免双倍缩放导致死区语义失真）。
				value := obj.ScaledValue() + pt.Offset
				if d.publishPoint(f, pt, value, false, TimeLabel{}, logger) {
					published++
				}
			}
		} else {
						logger.Debug("模拟量未匹配到点表",
				zap.String("comp_addr", stringAddr4(obj.ComponentAddr)),
				zap.Uint8("comp_type", obj.ComponentType),
				zap.Uint8("analog_type", obj.AnalogType),
				zap.Int16("raw_value", obj.AnalogValue),
			)
		}

		offset += consumed
	}

	return published
}

// mapOperationInfo 映射操作信息（类型4/24）
func (d *Driver) mapOperationInfo(f *Frame, adu *ADU, logger *zap.Logger) int {
	srcAddrStr := StringAddr(f.SrcAddr)
	published := 0

	offset := 0
	for i := 0; i < int(adu.ObjectCount); i++ {
		if offset+OperationInfoLen > len(adu.Objects) {
			break
		}
		obj, err := ParseOperationInfo(adu.Objects[offset:])
		if err != nil {
			break
		}
		consumed := OperationInfoLen

		// 按 (MessageType, SystemAddress) 通配查找（操作信息无部件地址）
		d.pointMu.RLock()
		for _, pt := range d.wildPoints {
			if pt.MessageType != adu.Type {
				continue
			}
			if pt.DeviceAddress != "" && pt.DeviceAddress != srcAddrStr {
				continue
			}
			// 编码：操作员编号*256 + 操作码（整型原始值，应用 pt.Scale）
			value := (float64(obj.OperatorID)*256+float64(obj.OpCode))*pt.Scale + pt.Offset
			if d.publishPoint(f, pt, value, false, TimeLabel{}, logger) {
				published++
			}
		}
		d.pointMu.RUnlock()

		offset += consumed
	}

	return published
}

// mapSoftwareVersion 映射软件版本（类型5/25）
func (d *Driver) mapSoftwareVersion(f *Frame, adu *ADU, logger *zap.Logger) int {
	srcAddrStr := StringAddr(f.SrcAddr)
	published := 0

	offset := 0
	for i := 0; i < int(adu.ObjectCount); i++ {
		if offset+SoftwareVersionLen > len(adu.Objects) {
			break
		}
		obj, err := ParseSoftwareVersion(adu.Objects[offset:])
		if err != nil {
			break
		}
		consumed := SoftwareVersionLen

		d.pointMu.RLock()
		for _, pt := range d.wildPoints {
			if pt.MessageType != adu.Type {
				continue
			}
			if pt.DeviceAddress != "" && pt.DeviceAddress != srcAddrStr {
				continue
			}
			// 编码：主版本*256 + 次版本（整型原始值，应用 pt.Scale）
			value := (float64(obj.MajorVersion)*256+float64(obj.MinorVersion))*pt.Scale + pt.Offset
			if d.publishPoint(f, pt, value, false, TimeLabel{}, logger) {
				published++
			}
		}
		d.pointMu.RUnlock()

		offset += consumed
	}

	return published
}

// mapTransmissionDeviceStatus 映射传输装置运行状态（类型21）
func (d *Driver) mapTransmissionDeviceStatus(f *Frame, adu *ADU, logger *zap.Logger) int {
	// 复用系统状态结构（4字节）
	return d.mapSystemStatus(f, adu, logger)
}

// publishPoint 发布单个测点（含死区过滤）
// value 必须是最终工程值（缩放已在调用方完成）：发布前仅依据 DeadbandValue 做过滤。
// 返回 true 表示已发布到 bus，false 表示被死区过滤（供调用方统计正确计数）。
func (d *Driver) publishPoint(f *Frame, pt *PointConfig, value float64, hasTime bool, t TimeLabel, logger *zap.Logger) bool {
	// 时间戳
	var tsNs int64
	if hasTime && !t.IsZero() {
		tsNs = bcdTimeToUnixNano(t)
	} else {
		tsNs = time.Now().UnixNano()
	}

	// 死区过滤：shouldFilter 返回 true 表示"放行/发布"，false 表示"过滤/丢弃"
	if pt.DeadbandValue > 0 && !d.shouldFilter(pt, value, tsNs) {
		return false // 变化在死区内，过滤
	}

	p := model.GetPoint()
	p.ID = d.pointID(pt)
	p.Value = value
	p.Timestamp = tsNs
	p.Quality = model.QualityGood

	d.bus.Publish(p)

	logger.Debug("发布测点",
		zap.String("id", p.ID),
		zap.Float64("value", value),
		zap.Int64("ts_ns", tsNs),
	)
	return true
}

// shouldFilter 死区过滤判定
// 返回 true  = 放行/发布（首次收到，或变化超过死区阈值）
// 返回 false = 过滤/丢弃（变化在死区内）
// 跟踪每个测点（按 pointID）的上次值/时间。
func (d *Driver) shouldFilter(pt *PointConfig, value float64, ts int64) bool {
	key := d.pointID(pt)
	d.pointMu.Lock()
	defer d.pointMu.Unlock()

	last, ok := d.lastValues[key]
	if !ok {
		d.lastValues[key] = value
		d.lastTimes[key] = ts
		return true
	}

	threshold := pt.DeadbandValue
	if pt.DeadbandType == "percent" {
		threshold = abs(last) * pt.DeadbandValue / 100.0
	}

	delta := abs(value - last)
	d.lastTimes[key] = ts
	if delta < threshold {
		return false
	}
	d.lastValues[key] = value
	return true
}

// lastValues/lastTimes 死区跟踪（线程安全）
var _ = (*Driver)(nil) // 确保类型声明在编译期存在

// 关联到 Driver 结构
//（死区跟踪表保存在 driver 结构上）
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// bcdTimeToUnixNano BCD 时间标签 → Unix 纳秒
func bcdTimeToUnixNano(t TimeLabel) int64 {
	// 解析 BCD：t[0]=秒 t[1]=分 t[2]=时 t[3]=日 t[4]=月 t[5]=年(后两位)
	sec := int(bcd2dec(t[0]))
	min := int(bcd2dec(t[1]))
	hour := int(bcd2dec(t[2]))
	day := int(bcd2dec(t[3]))
	month := int(bcd2dec(t[4]))
	year := 2000 + int(bcd2dec(t[5]))

	if sec > 59 || min > 59 || hour > 23 || day > 31 || month > 12 || year > 2099 {
		return time.Now().UnixNano()
	}

	tt := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.Local)
	return tt.UnixNano()
}

func bcd2dec(b byte) int {
	hi := int((b>>4)&0x0F) * 10
	lo := int(b & 0x0F)
	return hi + lo
}

// stringAddr4 4字节部件地址转可读字符串（低字节在前）
func stringAddr4(addr [4]byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X", addr[3], addr[2], addr[1], addr[0])
}
