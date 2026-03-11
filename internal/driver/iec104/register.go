// internal/driver/iec104/register.go - IEC104 驱动注册
package iec104

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
	driver.RegisterDriver("iec104", NewIEC104DriverFromConfig)
}

// iec104PointCSV CSV点表定义（内部使用）
type iec104PointCSV struct {
	Name           string
	IOA            uint32
	CommonAddress  uint8
	Type           string
	Scale          float64
	Offset         float64
	DeadbandValue  float64
	DeadbandType   string
	Description    string
}

// NewIEC104DriverFromConfig 从配置创建 IEC104 驱动实例
// 此函数注册到驱动工厂，由工厂统一调用
func NewIEC104DriverFromConfig(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (driver.Driver, error) {
	// 解析点表文件
	points, err := parseIEC104CSV(drvCfg.PointFile, logger)
	if err != nil {
		return nil, err
	}

	logger.Info("IEC104点表解析完成", zap.Int("points", len(points)))

	// 转换为 IEC104 配置
	iec104Points := make([]PointConfig, 0, len(points))
	for _, pt := range points {
		iec104Pt := convertIEC104Point(pt)
		iec104Points = append(iec104Points, iec104Pt)
	}

	// 创建 IEC104 配置
	iec104Cfg := Config{
		Name:                drvCfg.Name,
		Host:                drvCfg.IEC104.Host,
		Port:                drvCfg.IEC104.Port,
		CommonAddress:       drvCfg.IEC104.CommonAddress,
		Points:              iec104Points,
		ReconnectInterval:   drvCfg.IEC104.ReconnectInterval,
		Timeout:             drvCfg.IEC104.Timeout,
		TestInterval:        drvCfg.IEC104.TestInterval,
		GIInterval:          drvCfg.IEC104.GIInterval,
		ClockSyncInterval:   drvCfg.IEC104.ClockSyncInterval,
		GIStaggeredDelay:    drvCfg.IEC104.GIStaggeredDelay,
		EnableSystemMetrics: drvCfg.IEC104.EnableSystemMetrics,
	}

	// 创建 IEC104 驱动
	drv := New(iec104Cfg, logger)

	logger.Info("IEC104驱动创建完成",
		zap.String("host", drvCfg.IEC104.Host),
		zap.Int("port", drvCfg.IEC104.Port),
	)

	return drv, nil
}

// parseIEC104CSV 解析 IEC104 CSV 点表文件
func parseIEC104CSV(filePath string, logger *zap.Logger) ([]iec104PointCSV, error) {
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
	requiredHeaders := []string{"Name", "IOA"}
	for _, req := range requiredHeaders {
		if _, ok := headerMap[req]; !ok {
			return nil, fmt.Errorf("CSV表头缺少必需字段: %s", req)
		}
	}

	var points []iec104PointCSV
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

		point, err := parseIEC104Line(record, headerMap, lineNum)
		if err != nil {
			return nil, err
		}

		points = append(points, point)
	}

	return points, nil
}

// parseIEC104Line 解析单行 IEC104 点表
func parseIEC104Line(record []string, headerMap map[string]int, lineNum int) (iec104PointCSV, error) {
	var point iec104PointCSV

	// Name
	point.Name = record[headerMap["Name"]]
	if point.Name == "" {
		return point, fmt.Errorf("第%d行: Name不能为空", lineNum)
	}

	// IOA
	ioa, err := strconv.ParseUint(record[headerMap["IOA"]], 10, 32)
	if err != nil {
		return point, fmt.Errorf("第%d行: IOA无效", lineNum)
	}
	point.IOA = uint32(ioa)

	// CommonAddress (可选)
	if idx, ok := headerMap["CommonAddress"]; ok && idx < len(record) && record[idx] != "" {
		addr, err := strconv.ParseUint(record[idx], 10, 8)
		if err != nil {
			point.CommonAddress = 0
		} else {
			point.CommonAddress = uint8(addr)
		}
	} else {
		point.CommonAddress = 0
	}

	// Type (可选，默认 M_ME_NC_1)
	if idx, ok := headerMap["Type"]; ok && idx < len(record) && record[idx] != "" {
		point.Type = record[idx]
	} else {
		point.Type = "M_ME_NC_1"
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

	// DeadbandValue (可选)
	if idx, ok := headerMap["DeadbandValue"]; ok && idx < len(record) && record[idx] != "" {
		deadband, err := strconv.ParseFloat(record[idx], 64)
		if err != nil {
			point.DeadbandValue = 0
		} else {
			point.DeadbandValue = deadband
		}
	} else {
		point.DeadbandValue = 0
	}

	// DeadbandType (可选)
	if idx, ok := headerMap["DeadbandType"]; ok && idx < len(record) && record[idx] != "" {
		point.DeadbandType = record[idx]
	} else {
		point.DeadbandType = "absolute"
	}

	// Description (可选)
	if idx, ok := headerMap["Description"]; ok && idx < len(record) {
		point.Description = record[idx]
	}

	return point, nil
}

// convertIEC104Point 将 CSV 点转换为 PointConfig
func convertIEC104Point(pt iec104PointCSV) PointConfig {
	return PointConfig{
		Name:           pt.Name,
		IOA:            pt.IOA,
		CA:             pt.CommonAddress,
		TypeID:         ParseTypeID(pt.Type),
		Scale:          pt.Scale,
		Offset:         pt.Offset,
		DeadbandValue:  pt.DeadbandValue,
		DeadbandType:   pt.DeadbandType,
		Description:    pt.Description,
	}
}
