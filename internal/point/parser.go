// internal/point/parser.go - CSV点表解析器
package point

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/gateway/gateway/internal/driver/iec104"
	"github.com/gateway/gateway/internal/driver/modbus"
	"go.uber.org/zap"
)

// ModbusPoint Modbus点表定义
type ModbusPoint struct {
	// Name 测点名称
	Name string
	// Address 寄存器地址（0-based）
	Address uint16
	// Type 寄存器类型：holding, input, coil, discrete
	Type string
	// DataType 数据类型：int16, uint16, int32, uint32, float32, float64, bool
	DataType string
	// ByteOrder 字节序: "big", "little", "ABCD", "CDAB", "BADC", "DCBA"
	ByteOrder string
	// BitPos 位提取位置(0-15)，-1表示不启用
	BitPos int
	// Scale 缩放系数
	Scale float64
	// Offset 偏移量
	Offset float64
	// Interval 采集间隔（毫秒），0表示使用默认值
	Interval int
}

// IEC104Point IEC104点表定义
type IEC104Point struct {
	// Name 测点名称
	Name string
	// IOA 信息对象地址
	IOA uint32
	// CommonAddress 公共地址（0表示使用驱动配置）
	CommonAddress uint8
	// Type 类型标识：M_SP_NA_1(单点遥信), M_ME_NA_1(归一化值), M_ME_NB_1(标度化值), M_ME_NC_1(短浮点数)
	Type string
	// Interval 采集间隔（毫秒），0表示使用默认值
	Interval int
	// Scale 缩放系数
	Scale float64
	// Offset 偏移量
	Offset float64
	// DeadbandValue 死区阈值
	DeadbandValue float64
	// DeadbandType 死区类型（absolute/percent）
	DeadbandType string
	// Description 测点描述
	Description string
}

// Parser 点表解析器
type Parser struct {
	logger *zap.Logger
}

// NewParser 创建点表解析器
func NewParser(logger *zap.Logger) *Parser {
	return &Parser{
		logger: logger,
	}
}

// ParseModbusCSV 解析Modbus CSV点表文件
// CSV格式（第一行为表头）：
// Name,Address,Type,DataType,Scale,Offset,Interval
// reg0,100,holding,int16,1.0,0,0
// reg1,101,holding,int16,1.0,0,1000
func (p *Parser) ParseModbusCSV(filePath string) ([]ModbusPoint, error) {
	p.logger.Info("开始解析Modbus点表文件", zap.String("file", filePath))

	// 打开CSV文件
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("打开CSV文件失败: %w", err)
	}
	defer file.Close()

	// 创建CSV读取器
	reader := csv.NewReader(file)

	// 读取表头
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("读取CSV表头失败: %w", err)
	}

	// 验证表头
	requiredHeaders := []string{"Name", "Address", "Type", "DataType"}
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[h] = i
	}

	for _, req := range requiredHeaders {
		if _, ok := headerMap[req]; !ok {
			return nil, fmt.Errorf("CSV表头缺少必需字段: %s", req)
		}
	}

	// 解析数据行
	var points []ModbusPoint
	lineNum := 1 // 已经读取了表头
	for {
		lineNum++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取CSV第%d行失败: %w", lineNum, err)
		}

		// 跳过空行
		if len(record) == 0 || (len(record) == 1 && record[0] == "") {
			continue
		}

		// 解析点表
		point, err := p.parseModbusLine(record, headerMap, lineNum)
		if err != nil {
			return nil, err
		}

		points = append(points, point)
	}

	p.logger.Info("Modbus点表解析完成", zap.Int("points", len(points)))
	return points, nil
}

// parseModbusLine 解析单行Modbus点表
func (p *Parser) parseModbusLine(record []string, headerMap map[string]int, lineNum int) (ModbusPoint, error) {
	var point ModbusPoint

	// Name
	point.Name = record[headerMap["Name"]]
	if point.Name == "" {
		return point, fmt.Errorf("第%d行: Name不能为空", lineNum)
	}

	// Address
	addrStr := record[headerMap["Address"]]
	addr, err := strconv.ParseUint(addrStr, 10, 16)
	if err != nil {
		return point, fmt.Errorf("第%d行: Address无效: %s", lineNum, addrStr)
	}
	point.Address = uint16(addr)

	// Type
	point.Type = record[headerMap["Type"]]
	if point.Type == "" {
		point.Type = "holding" // 默认保持寄存器
	}
	// 验证Type
	validTypes := map[string]bool{"holding": true, "input": true, "coil": true, "discrete": true}
	if !validTypes[point.Type] {
		return point, fmt.Errorf("第%d行: Type无效: %s (应为 holding, input, coil 或 discrete)", lineNum, point.Type)
	}

	// DataType
	point.DataType = record[headerMap["DataType"]]
	if point.DataType == "" {
		point.DataType = "int16" // 默认int16
	}
	// 验证DataType
	validDataTypes := map[string]bool{
		"int16": true, "uint16": true,
		"int32": true, "uint32": true,
		"float32": true, "float64": true,
		"bool": true,
	}
	if !validDataTypes[point.DataType] {
		return point, fmt.Errorf("第%d行: DataType无效: %s", lineNum, point.DataType)
	}

	// ByteOrder (可选)
	if idx, ok := headerMap["ByteOrder"]; ok && idx < len(record) && record[idx] != "" {
		point.ByteOrder = record[idx]
		// 验证ByteOrder
		validByteOrders := map[string]bool{
			"big": true, "little": true,
			"ABCD": true, "CDAB": true, "BADC": true, "DCBA": true,
		}
		if !validByteOrders[point.ByteOrder] {
			return point, fmt.Errorf("第%d行: ByteOrder无效: %s (应为 big, little, ABCD, CDAB, BADC 或 DCBA)", lineNum, point.ByteOrder)
		}
	} else {
		point.ByteOrder = "big" // 默认大端序
	}

	// BitPos (可选)
	if idx, ok := headerMap["BitPos"]; ok && idx < len(record) && record[idx] != "" {
		bitPos, err := strconv.Atoi(record[idx])
		if err != nil || bitPos < 0 || bitPos > 15 {
			return point, fmt.Errorf("第%d行: BitPos无效: %s (应为 0-15)", lineNum, record[idx])
		}
		point.BitPos = bitPos
	} else {
		point.BitPos = -1 // -1表示不启用位提取
	}

	// Scale
	if idx, ok := headerMap["Scale"]; ok && idx < len(record) && record[idx] != "" {
		scale, err := strconv.ParseFloat(record[idx], 64)
		if err != nil {
			return point, fmt.Errorf("第%d行: Scale无效: %s", lineNum, record[idx])
		}
		point.Scale = scale
	} else {
		point.Scale = 1.0
	}

	// Offset
	if idx, ok := headerMap["Offset"]; ok && idx < len(record) && record[idx] != "" {
		offset, err := strconv.ParseFloat(record[idx], 64)
		if err != nil {
			return point, fmt.Errorf("第%d行: Offset无效: %s", lineNum, record[idx])
		}
		point.Offset = offset
	} else {
		point.Offset = 0
	}

	// Interval
	if idx, ok := headerMap["Interval"]; ok && idx < len(record) && record[idx] != "" {
		interval, err := strconv.Atoi(record[idx])
		if err != nil || interval < 0 {
			return point, fmt.Errorf("第%d行: Interval无效: %s", lineNum, record[idx])
		}
		point.Interval = interval
	} else {
		point.Interval = 0 // 0表示使用默认值
	}

	return point, nil
}

// ParseIEC104CSV 解析IEC104 CSV点表文件
// CSV格式（第一行为表头）：
// Name,IOA,CommonAddress,Type,Interval
// ai0,100,1,M_ME_NC_1,0
// di0,200,1,M_SP_NA_1,1000
func (p *Parser) ParseIEC104CSV(filePath string) ([]IEC104Point, error) {
	p.logger.Info("开始解析IEC104点表文件", zap.String("file", filePath))

	// 打开CSV文件
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("打开CSV文件失败: %w", err)
	}
	defer file.Close()

	// 创建CSV读取器
	reader := csv.NewReader(file)

	// 读取表头
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("读取CSV表头失败: %w", err)
	}

	// 验证表头
	requiredHeaders := []string{"Name", "IOA"}
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[h] = i
	}

	for _, req := range requiredHeaders {
		if _, ok := headerMap[req]; !ok {
			return nil, fmt.Errorf("CSV表头缺少必需字段: %s", req)
		}
	}

	// 解析数据行
	var points []IEC104Point
	lineNum := 1
	for {
		lineNum++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取CSV第%d行失败: %w", lineNum, err)
		}

		// 跳过空行
		if len(record) == 0 || (len(record) == 1 && record[0] == "") {
			continue
		}

		// 解析点表
		point, err := p.parseIEC104Line(record, headerMap, lineNum)
		if err != nil {
			return nil, err
		}

		points = append(points, point)
	}

	p.logger.Info("IEC104点表解析完成", zap.Int("points", len(points)))
	return points, nil
}

// parseIEC104Line 解析单行IEC104点表
func (p *Parser) parseIEC104Line(record []string, headerMap map[string]int, lineNum int) (IEC104Point, error) {
	var point IEC104Point

	// Name
	point.Name = record[headerMap["Name"]]
	if point.Name == "" {
		return point, fmt.Errorf("第%d行: Name不能为空", lineNum)
	}

	// IOA
	ioaStr := record[headerMap["IOA"]]
	ioa, err := strconv.ParseUint(ioaStr, 10, 32)
	if err != nil {
		return point, fmt.Errorf("第%d行: IOA无效: %s", lineNum, ioaStr)
	}
	point.IOA = uint32(ioa)

	// CommonAddress (可选)
	if idx, ok := headerMap["CommonAddress"]; ok && idx < len(record) && record[idx] != "" {
		addr, err := strconv.ParseUint(record[idx], 10, 8)
		if err != nil {
			return point, fmt.Errorf("第%d行: CommonAddress无效: %s", lineNum, record[idx])
		}
		point.CommonAddress = uint8(addr)
	} else {
		point.CommonAddress = 0 // 0表示使用驱动配置
	}

	// Type
	point.Type = record[headerMap["Type"]]
	if point.Type == "" {
		return point, fmt.Errorf("第%d行: Type不能为空", lineNum)
	}
	// 验证Type
	validTypes := map[string]bool{
		"M_SP_NA_1": true, // 单点遥信
		"M_DP_NA_1": true, // 双点遥信
		"M_ME_NA_1": true, // 归一化值
		"M_ME_NB_1": true, // 标度化值
		"M_ME_NC_1": true, // 短浮点数
		"M_IT_NA_1": true, // 累计量
		"M_PS_NA_1": true, // 32位比特串
	}
	if !validTypes[point.Type] {
		return point, fmt.Errorf("第%d行: Type无效: %s", lineNum, point.Type)
	}

	// Interval (可选)
	if idx, ok := headerMap["Interval"]; ok && idx < len(record) && record[idx] != "" {
		interval, err := strconv.Atoi(record[idx])
		if err != nil || interval < 0 {
			return point, fmt.Errorf("第%d行: Interval无效: %s", lineNum, record[idx])
		}
		point.Interval = interval
	} else {
		point.Interval = 0 // 0表示使用默认值
	}

	// Scale (可选)
	if idx, ok := headerMap["Scale"]; ok && idx < len(record) && record[idx] != "" {
		scale, err := strconv.ParseFloat(record[idx], 64)
		if err != nil {
			return point, fmt.Errorf("第%d行: Scale无效: %s", lineNum, record[idx])
		}
		point.Scale = scale
	} else {
		point.Scale = 1.0
	}

	// Offset (可选)
	if idx, ok := headerMap["Offset"]; ok && idx < len(record) && record[idx] != "" {
		offset, err := strconv.ParseFloat(record[idx], 64)
		if err != nil {
			return point, fmt.Errorf("第%d行: Offset无效: %s", lineNum, record[idx])
		}
		point.Offset = offset
	} else {
		point.Offset = 0
	}

	// DeadbandValue (可选)
	if idx, ok := headerMap["DeadbandValue"]; ok && idx < len(record) && record[idx] != "" {
		deadband, err := strconv.ParseFloat(record[idx], 64)
		if err != nil {
			return point, fmt.Errorf("第%d行: DeadbandValue无效: %s", lineNum, record[idx])
		}
		point.DeadbandValue = deadband
	} else {
		point.DeadbandValue = 0
	}

	// DeadbandType (可选)
	if idx, ok := headerMap["DeadbandType"]; ok && idx < len(record) && record[idx] != "" {
		deadbandType := record[idx]
		if deadbandType != "absolute" && deadbandType != "percent" {
			return point, fmt.Errorf("第%d行: DeadbandType无效: %s (应为 absolute 或 percent)", lineNum, deadbandType)
		}
		point.DeadbandType = deadbandType
	} else {
		point.DeadbandType = "absolute"
	}

	// Description (可选)
	if idx, ok := headerMap["Description"]; ok && idx < len(record) {
		point.Description = record[idx]
	}

	return point, nil
}

// ModbusPointToConfig 将ModbusPoint转换为modbus.PointConfig
func ModbusPointToConfig(point ModbusPoint) modbus.PointConfig {
	// 转换Type
	var regType modbus.RegisterType
	switch point.Type {
	case "holding":
		regType = modbus.HoldingRegister
	case "input":
		regType = modbus.InputRegister
	case "coil":
		regType = modbus.Coil
	case "discrete":
		regType = modbus.DiscreteInput
	default:
		regType = modbus.HoldingRegister
	}

	// 转换DataType
	var dataType modbus.DataType
	switch point.DataType {
	case "int16":
		dataType = modbus.Int16
	case "uint16":
		dataType = modbus.Uint16
	case "int32":
		dataType = modbus.Int32
	case "uint32":
		dataType = modbus.Uint32
	case "float32":
		dataType = modbus.Float32
	case "float64":
		dataType = modbus.Float64
	case "bool":
		dataType = modbus.Bool
	default:
		dataType = modbus.Int16
	}

	return modbus.PointConfig{
		Name:      point.Name,
		Address:   point.Address,
		Type:      regType,
		DataType:  dataType,
		ByteOrder: point.ByteOrder,
		BitPos:    point.BitPos,
		Scale:     point.Scale,
		Offset:    point.Offset,
	}
}

// GetInterval 获取采集间隔
func GetInterval(intervalMs int, defaultInterval time.Duration) time.Duration {
	if intervalMs > 0 {
		return time.Duration(intervalMs) * time.Millisecond
	}
	return defaultInterval
}

// IEC104PointToConfig 将 IEC104Point 转换为 iec104.PointConfig
func IEC104PointToConfig(point IEC104Point) iec104.PointConfig {
	return iec104.PointConfig{
		Name:           point.Name,
		IOA:            point.IOA,
		CA:             point.CommonAddress, // CommonAddress 映射到 CA
		TypeID:         iec104.ParseTypeID(point.Type),
		Scale:          point.Scale,
		Offset:         point.Offset,
		DeadbandValue:  point.DeadbandValue,
		DeadbandType:   point.DeadbandType,
		Description:    point.Description,
	}
}

// IEC104PointsToConfig 批量转换 IEC104Point 列表为 iec104.PointConfig 列表
func IEC104PointsToConfig(points []IEC104Point) []iec104.PointConfig {
	configs := make([]iec104.PointConfig, len(points))
	for i, p := range points {
		configs[i] = IEC104PointToConfig(p)
	}
	return configs
}

// IEC104PointsToMap 将 IEC104Point 列表转换为以 IOA 为键的映射表
func IEC104PointsToMap(points []IEC104Point) map[uint32]*iec104.PointConfig {
	m := make(map[uint32]*iec104.PointConfig, len(points))
	for i := range points {
		cfg := IEC104PointToConfig(points[i])
		m[points[i].IOA] = &cfg
	}
	return m
}
