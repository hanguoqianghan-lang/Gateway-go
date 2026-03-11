package iec104server

import (
	"fmt"
	"time"

	"github.com/gateway/gateway/internal/model"
	"github.com/wendy512/go-iecp5/asdu"
)

// ASDUBuilder ASDU 组包器
type ASDUBuilder struct {
	config         Config
	mappingManager *MappingManager
	params         *asdu.Params
}

// NewASDUBuilder 创建组包器
func NewASDUBuilder(config Config, mappingMgr *MappingManager) *ASDUBuilder {
	// 创建 ASDU 参数
	params := &asdu.Params{
		CauseSize:      2, // 传输原因字节数
		CommonAddrSize: 2, // 公共地址字节数
		InfoObjAddrSize: 3, // 信息对象地址字节数
	}

	return &ASDUBuilder{
		config:         config,
		mappingManager: mappingMgr,
		params:         params,
	}
}

// BuildASDU 根据 PointData 构建 ASDU
func (b *ASDUBuilder) BuildASDU(data *model.PointData, cause asdu.Cause) (*asdu.ASDU, error) {
	mapping, ok := b.mappingManager.GetMapping(data.ID)
	if !ok {
		return nil, fmt.Errorf("no mapping found for point: %s", data.ID)
	}

	// 创建 ASDU 标识符
	identifier := asdu.Identifier{
		Type:       mapping.TypeID,
		Variable:   asdu.VariableStruct{IsSequence: false},
		Coa:        asdu.CauseOfTransmission{Cause: cause},
		CommonAddr: asdu.CommonAddr(b.config.CommonAddress),
	}

	// 创建 ASDU
	a := asdu.NewASDU(b.params, identifier)

	// 根据类型标识添加信息对象
	if err := b.addInfoObj(a, data, mapping); err != nil {
		return nil, err
	}

	return a, nil
}

// BuildBatchASDUs 批量构建 ASDU（处理 APDU 长度限制）
// 返回多个 ASDU，每个 ASDU 的长度不超过 maxAPDULength
func (b *ASDUBuilder) BuildBatchASDUs(dataList []*model.PointData, cause asdu.Cause) ([]*asdu.ASDU, error) {
	if len(dataList) == 0 {
		return nil, nil
	}

	var result []*asdu.ASDU
	var currentASDU *asdu.ASDU
	var currentLen int
	var currentTypeID asdu.TypeID
	var currentCount int

	// ASDU 头部固定长度：TypeID(1) + VSQ(1) + COT(2) + CommonAddr(2) = 6 字节
	const asduHeaderLen = 6

	// APDU 头部固定长度：启动字节(1) + 长度字段(1) + 控制域(4) = 6 字节
	// ASDU 最大长度 = APDU 最大长度 - 6
	maxASDULen := int(b.config.MaxAPDULength) - 6

	for _, data := range dataList {
		mapping, ok := b.mappingManager.GetMapping(data.ID)
		if !ok {
			continue // 跳过未映射的点
		}

		// 计算该信息对象的长度
		infoObjLen := b.calcInfoObjLen(mapping.TypeID)

		// 检查是否需要创建新的 ASDU
		needNewASDU := currentASDU == nil ||
			currentTypeID != mapping.TypeID ||
			currentLen+infoObjLen > maxASDULen

		if needNewASDU {
			// 保存当前 ASDU
			if currentASDU != nil && currentCount > 0 {
				// 设置信息对象数量
				currentASDU.Identifier.Variable = asdu.VariableStruct{
					IsSequence: false,
					Number:     byte(currentCount),
				}
				result = append(result, currentASDU)
			}

			// 创建新的 ASDU
			identifier := asdu.Identifier{
				Type:       mapping.TypeID,
				Variable:   asdu.VariableStruct{IsSequence: false},
				Coa:        asdu.CauseOfTransmission{Cause: cause},
				CommonAddr: asdu.CommonAddr(b.config.CommonAddress),
			}
			currentASDU = asdu.NewASDU(b.params, identifier)
			currentLen = asduHeaderLen
			currentTypeID = mapping.TypeID
			currentCount = 0
		}

		// 添加信息对象到当前 ASDU
		if err := b.addInfoObj(currentASDU, data, mapping); err != nil {
			return nil, err
		}
		currentLen += infoObjLen
		currentCount++
	}

	// 保存最后一个 ASDU
	if currentASDU != nil && currentCount > 0 {
		currentASDU.Identifier.Variable = asdu.VariableStruct{
			IsSequence: false,
			Number:     byte(currentCount),
		}
		result = append(result, currentASDU)
	}

	return result, nil
}

// calcInfoObjLen 计算信息对象长度
func (b *ASDUBuilder) calcInfoObjLen(typeID asdu.TypeID) int {
	switch typeID {
	// 单点遥信
	case asdu.M_SP_NA_1:
		return 4 // IOA(3) + SIQ(1)
	case asdu.M_SP_TB_1:
		return 11 // IOA(3) + SIQ(1) + CP56Time2a(7)

	// 双点遥信
	case asdu.M_DP_NA_1:
		return 4 // IOA(3) + DIQ(1)
	case asdu.M_DP_TB_1:
		return 11

	// 步位置
	case asdu.M_ST_NA_1:
		return 5 // IOA(3) + VTI(1) + QDS(1)
	case asdu.M_ST_TB_1:
		return 12

	// 比特串
	case asdu.M_BO_NA_1:
		return 7 // IOA(3) + BSI(4)
	case asdu.M_BO_TB_1:
		return 14

	// 遥测值（归一化值）
	case asdu.M_ME_NA_1:
		return 5 // IOA(3) + Value(2) + QDS(1)
	case asdu.M_ME_TD_1:
		return 12

	// 遥测值（标度化值）
	case asdu.M_ME_NB_1:
		return 5 // IOA(3) + Value(2) + QDS(1)
	case asdu.M_ME_TE_1:
		return 12

	// 遥测值（浮点值）
	case asdu.M_ME_NC_1:
		return 8 // IOA(3) + Value(4) + QDS(1)
	case asdu.M_ME_TF_1:
		return 15

	// 遥测值（短浮点）
	case asdu.M_ME_ND_1:
		return 5 // IOA(3) + Value(2)

	default:
		return 8 // 默认最大长度
	}
}

// addInfoObj 添加信息对象到 ASDU
func (b *ASDUBuilder) addInfoObj(a *asdu.ASDU, data *model.PointData, mapping *PointMapping) error {
	// 添加信息对象地址
	if err := a.AppendInfoObjAddr(asdu.InfoObjAddr(mapping.IOA)); err != nil {
		return err
	}

	qds := b.mapQuality(data.Quality)

	switch mapping.TypeID {
	case asdu.M_SP_NA_1:
		// 单点遥信
		siq := byte(0)
		if b.toBool(data.Value) {
			siq = 1
		}
		a.AppendBytes(siq | byte(qds))

	case asdu.M_SP_TB_1:
		// 带时标的单点遥信
		siq := byte(0)
		if b.toBool(data.Value) {
			siq = 1
		}
		a.AppendBytes(siq | byte(qds))
		a.AppendCP56Time2a(b.toTime(data.Timestamp), time.Local)

	case asdu.M_DP_NA_1:
		// 双点遥信
		diq := b.toDoublePointValue(data.Value)
		a.AppendBytes(diq | byte(qds))

	case asdu.M_DP_TB_1:
		// 带时标的双点遥信
		diq := b.toDoublePointValue(data.Value)
		a.AppendBytes(diq | byte(qds))
		a.AppendCP56Time2a(b.toTime(data.Timestamp), time.Local)

	case asdu.M_ME_NA_1:
		// 遥测值（归一化值）
		value := b.toInt16(data.Value, mapping.Scale, mapping.Offset)
		a.AppendNormalize(asdu.Normalize(value))
		a.AppendBytes(byte(qds))

	case asdu.M_ME_NB_1:
		// 遥测值（标度化值）
		value := b.toInt16(data.Value, mapping.Scale, mapping.Offset)
		a.AppendScaled(value)
		a.AppendBytes(byte(qds))

	case asdu.M_ME_NC_1:
		// 遥测值（浮点值）
		value := b.toFloat32(data.Value, mapping.Scale, mapping.Offset)
		a.AppendFloat32(value)
		a.AppendBytes(byte(qds))

	case asdu.M_ME_ND_1:
		// 遥测值（短浮点，不带品质）
		value := b.toFloat32(data.Value, mapping.Scale, mapping.Offset)
		a.AppendFloat32(value)

	case asdu.M_ST_NA_1:
		// 步位置信息
		value := b.toInt8(data.Value)
		a.AppendBytes(byte(value))
		a.AppendBytes(byte(qds))

	case asdu.M_BO_NA_1:
		// 比特串
		value := b.toUint32(data.Value)
		a.AppendBitsString32(value)

	default:
		return fmt.Errorf("unsupported type ID: %d", mapping.TypeID)
	}

	return nil
}

// mapQuality 映射质量码
func (b *ASDUBuilder) mapQuality(quality uint8) asdu.QualityDescriptor {
	switch quality {
	case model.QualityGood:
		return asdu.QDSGood
	case model.QualityBad, model.QualityCommFail, model.QualityTimeout:
		return asdu.QDSInvalid
	case model.QualityUncertain:
		return asdu.QDSInvalid
	default:
		return asdu.QDSGood
	}
}

// toBool 转换为布尔值
func (b *ASDUBuilder) toBool(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	default:
		return false
	}
}

// toDoublePointValue 转换为双点值
// 0=不确定/中间，1=分，2=合，3=不确定
func (b *ASDUBuilder) toDoublePointValue(value interface{}) byte {
	switch v := value.(type) {
	case bool:
		if v {
			return 2 // 合
		}
		return 1 // 分
	case int:
		return byte(v % 4)
	case int64:
		return byte(v % 4)
	default:
		return 0 // 不确定
	}
}

// toFloat32 转换为 float32（应用缩放和偏移）
func (b *ASDUBuilder) toFloat32(value interface{}, scale, offset float64) float32 {
	var v float64
	switch val := value.(type) {
	case float64:
		v = val
	case float32:
		v = float64(val)
	case int:
		v = float64(val)
	case int64:
		v = float64(val)
	default:
		v = 0
	}
	return float32(v*scale + offset)
}

// toInt16 转换为 int16（应用缩放和偏移）
func (b *ASDUBuilder) toInt16(value interface{}, scale, offset float64) int16 {
	var v float64
	switch val := value.(type) {
	case float64:
		v = val
	case float32:
		v = float64(val)
	case int:
		v = float64(val)
	case int64:
		v = float64(val)
	default:
		v = 0
	}
	return int16(v*scale + offset)
}

// toInt8 转换为 int8
func (b *ASDUBuilder) toInt8(value interface{}) int8 {
	switch v := value.(type) {
	case int:
		return int8(v)
	case int64:
		return int8(v)
	case float64:
		return int8(v)
	default:
		return 0
	}
}

// toUint32 转换为 uint32
func (b *ASDUBuilder) toUint32(value interface{}) uint32 {
	switch v := value.(type) {
	case uint:
		return uint32(v)
	case uint32:
		return v
	case int:
		return uint32(v)
	case int64:
		return uint32(v)
	default:
		return 0
	}
}

// toTime 转换为 time.Time
func (b *ASDUBuilder) toTime(timestamp int64) time.Time {
	return time.Unix(0, timestamp)
}
