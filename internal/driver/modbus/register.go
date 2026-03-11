// internal/driver/modbus/register.go - Modbus 驱动注册
package modbus

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/cgn/gateway/config"
	"github.com/cgn/gateway/internal/driver"
	"go.uber.org/zap"
)

func init() {
	// 在包初始化时自动注册驱动
	driver.RegisterDriver("modbus_tcp", NewModbusDriverFromConfig)
}

// modbusPointCSV CSV点表定义（内部使用）
type modbusPointCSV struct {
	Name       string
	Address    uint16
	Type       string
	DataType   string
	ByteOrder  string
	BitPos     int
	Scale      float64
	Offset     float64
	Interval   int
}

// NewModbusDriverFromConfig 从配置创建 Modbus 驱动实例
// 此函数注册到驱动工厂，由工厂统一调用
func NewModbusDriverFromConfig(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (driver.Driver, error) {
	// 解析点表文件
	points, err := parseModbusCSV(drvCfg.PointFile, logger)
	if err != nil {
		return nil, err
	}

	logger.Info("Modbus点表解析完成", zap.Int("points", len(points)))

	// 转换为 Modbus 配置
	modbusPoints := make([]PointConfig, 0, len(points))
	for _, pt := range points {
		modbusPt := convertModbusPoint(pt)
		modbusPoints = append(modbusPoints, modbusPt)
	}

	// 创建 Modbus 配置
	modbusCfg := ModbusConfig{
		Slaves: []SlaveConfig{
			{
				ID:               drvCfg.Name,
				Host:             drvCfg.Modbus.Host,
				Port:             drvCfg.Modbus.Port,
				UnitID:           drvCfg.Modbus.UnitID,
				PollInterval:     drvCfg.Modbus.PollInterval,
				Timeout:          drvCfg.Modbus.Timeout,
				MaxRetryInterval: drvCfg.Modbus.MaxRetryInterval,
				Points:           modbusPoints,
			},
		},
	}

	// 创建 Modbus 驱动
	drv := NewDriver(modbusCfg, logger)

	logger.Info("Modbus驱动创建完成",
		zap.String("host", drvCfg.Modbus.Host),
		zap.Int("port", drvCfg.Modbus.Port),
	)

	return drv, nil
}

// parseModbusCSV 解析 Modbus CSV 点表文件
func parseModbusCSV(filePath string, logger *zap.Logger) ([]modbusPointCSV, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("打开CSV文件失败: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// 读取表头
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("读取CSV表头失败: %w", err)
	}

	// 构建表头索引
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[h] = i
	}

	// 验证必需字段
	requiredHeaders := []string{"Name", "Address", "Type", "DataType"}
	for _, req := range requiredHeaders {
		if _, ok := headerMap[req]; !ok {
			return nil, fmt.Errorf("CSV表头缺少必需字段: %s", req)
		}
	}

	var points []modbusPointCSV
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

		point, err := parseModbusLine(record, headerMap, lineNum)
		if err != nil {
			return nil, err
		}

		points = append(points, point)
	}

	return points, nil
}

// parseModbusLine 解析单行 Modbus 点表
func parseModbusLine(record []string, headerMap map[string]int, lineNum int) (modbusPointCSV, error) {
	var point modbusPointCSV

	// Name
	point.Name = record[headerMap["Name"]]
	if point.Name == "" {
		return point, fmt.Errorf("第%d行: Name不能为空", lineNum)
	}

	// Address
	addr, err := strconv.ParseUint(record[headerMap["Address"]], 10, 16)
	if err != nil {
		return point, fmt.Errorf("第%d行: Address无效", lineNum)
	}
	point.Address = uint16(addr)

	// Type
	point.Type = record[headerMap["Type"]]
	if point.Type == "" {
		point.Type = "holding"
	}

	// DataType
	point.DataType = record[headerMap["DataType"]]
	if point.DataType == "" {
		point.DataType = "int16"
	}

	// ByteOrder (可选)
	if idx, ok := headerMap["ByteOrder"]; ok && idx < len(record) && record[idx] != "" {
		point.ByteOrder = record[idx]
	} else {
		point.ByteOrder = "big"
	}

	// BitPos (可选)
	if idx, ok := headerMap["BitPos"]; ok && idx < len(record) && record[idx] != "" {
		bitPos, err := strconv.Atoi(record[idx])
		if err != nil || bitPos < 0 || bitPos > 15 {
			point.BitPos = -1
		} else {
			point.BitPos = bitPos
		}
	} else {
		point.BitPos = -1
	}

	// Scale (可选)
	if idx, ok := headerMap["Scale"]; ok && idx < len(record) && record[idx] != "" {
		scale, err := strconv.ParseFloat(record[idx], 64)
		if err != nil {
			point.Scale = 1.0
		} else {
			point.Scale = scale
		}
	} else {
		point.Scale = 1.0
	}

	// Offset (可选)
	if idx, ok := headerMap["Offset"]; ok && idx < len(record) && record[idx] != "" {
		offset, err := strconv.ParseFloat(record[idx], 64)
		if err != nil {
			point.Offset = 0
		} else {
			point.Offset = offset
		}
	} else {
		point.Offset = 0
	}

	// Interval (可选)
	if idx, ok := headerMap["Interval"]; ok && idx < len(record) && record[idx] != "" {
		interval, err := strconv.Atoi(record[idx])
		if err != nil || interval < 0 {
			point.Interval = 0
		} else {
			point.Interval = interval
		}
	} else {
		point.Interval = 0
	}

	return point, nil
}

// convertModbusPoint 将 CSV 点转换为 PointConfig
func convertModbusPoint(pt modbusPointCSV) PointConfig {
	// 转换 Type
	var regType RegisterType
	switch pt.Type {
	case "holding":
		regType = HoldingRegister
	case "input":
		regType = InputRegister
	case "coil":
		regType = Coil
	case "discrete":
		regType = DiscreteInput
	default:
		regType = HoldingRegister
	}

	// 转换 DataType
	var dataType DataType
	switch pt.DataType {
	case "int16":
		dataType = Int16
	case "uint16":
		dataType = Uint16
	case "int32":
		dataType = Int32
	case "uint32":
		dataType = Uint32
	case "float32":
		dataType = Float32
	case "float64":
		dataType = Float64
	case "bool":
		dataType = Bool
	default:
		dataType = Int16
	}

	return PointConfig{
		Name:      pt.Name,
		Address:   pt.Address,
		Type:      regType,
		DataType:  dataType,
		ByteOrder: pt.ByteOrder,
		BitPos:    pt.BitPos,
		Scale:     pt.Scale,
		Offset:    pt.Offset,
	}
}
