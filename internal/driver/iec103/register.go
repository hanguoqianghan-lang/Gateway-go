// internal/driver/iec103/register.go - IEC 60870-5-103 驱动注册
package iec103

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/gateway/gateway/config"
	"github.com/gateway/gateway/internal/driver"
	"go.uber.org/zap"
)

func init() {
	// 在包初始化时自动注册驱动
	driver.RegisterDriver("iec103", NewIEC103DriverFromConfig)
}

// iec103PointCSV CSV 点表定义（内部使用）
type iec103PointCSV struct {
	Name          string
	CA            uint8
	FUN           uint8
	INF           uint8
	TypeID        uint8
	Scale         float64
	Offset        float64
	DeadbandValue float64
	DeadbandType  string
}

// NewIEC103DriverFromConfig 从配置创建 IEC103 驱动实例
func NewIEC103DriverFromConfig(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (driver.Driver, error) {
	// 解析点表文件
	points, err := parseIEC103CSV(drvCfg.PointFile, logger)
	if err != nil {
		return nil, err
	}

	logger.Info("IEC103 point table parsed", zap.Int("points", len(points)))

	// 转换为 IEC103 配置
	iec103Points := make([]PointConfig, 0, len(points))
	for _, pt := range points {
		iec103Pt := convertIEC103Point(pt)
		iec103Points = append(iec103Points, iec103Pt)
	}

	// 创建 IEC103 配置
	iec103Cfg := DefaultConfig()
	iec103Cfg.ID = drvCfg.ID
	iec103Cfg.Name = drvCfg.Name
	iec103Cfg.Points = iec103Points

	// 解析串口配置
	if drvCfg.IEC103 != nil {
		iec103Cfg.SerialPort = drvCfg.IEC103.SerialPort
		iec103Cfg.BaudRate = drvCfg.IEC103.BaudRate
		iec103Cfg.DataBits = drvCfg.IEC103.DataBits
		iec103Cfg.StopBits = drvCfg.IEC103.StopBits
		iec103Cfg.Parity = drvCfg.IEC103.Parity
		iec103Cfg.CommonAddress = uint8(drvCfg.IEC103.CommonAddress)
		iec103Cfg.LinkAddress = uint8(drvCfg.IEC103.LinkAddress)
		iec103Cfg.BalancedMode = drvCfg.IEC103.BalancedMode
		iec103Cfg.GIInterval = drvCfg.IEC103.GIInterval
		iec103Cfg.PollInterval = drvCfg.IEC103.PollInterval
		iec103Cfg.ResponseTimeout = drvCfg.IEC103.Timeout
		iec103Cfg.MaxRetry = drvCfg.IEC103.MaxRetry
		iec103Cfg.RetryInterval = drvCfg.IEC103.RetryInterval
		iec103Cfg.SOEQueueSize = drvCfg.IEC103.SOEQueueSize
		iec103Cfg.SOEWorkerCount = drvCfg.IEC103.SOEWorkerCount
	}

	// 创建 IEC103 驱动
	drv := New(iec103Cfg, logger)

	logger.Info("IEC103 driver created",
		zap.String("port", iec103Cfg.SerialPort),
		zap.Int("baud_rate", iec103Cfg.BaudRate),
		zap.String("parity", iec103Cfg.Parity),
		zap.Int("soe_queue_size", iec103Cfg.SOEQueueSize),
		zap.Int("soe_worker_count", iec103Cfg.SOEWorkerCount),
	)

	return drv, nil
}

// parseIEC103CSV 解析 IEC103 CSV 点表文件
// 表头：Name, CA, FUN, INF, TypeID, Scale, Offset, DeadbandValue, DeadbandType
func parseIEC103CSV(filePath string, logger *zap.Logger) ([]iec103PointCSV, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open CSV file failed: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// 读取表头
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV header failed: %w", err)
	}

	// 构建表头索引
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[h] = i
	}

	// 验证必需字段（IEC103 特有：FUN 和 INF）
	requiredHeaders := []string{"Name", "CA", "FUN", "INF"}
	for _, req := range requiredHeaders {
		if _, ok := headerMap[req]; !ok {
			return nil, fmt.Errorf("CSV header missing required field: %s", req)
		}
	}

	var points []iec103PointCSV
	lineNum := 1

	for {
		lineNum++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read CSV line %d failed: %w", lineNum, err)
		}

		// 跳过空行
		if len(record) == 0 || (len(record) == 1 && record[0] == "") {
			continue
		}

		point, err := parseIEC103Line(record, headerMap, lineNum)
		if err != nil {
			return nil, err
		}

		points = append(points, point)
	}

	return points, nil
}

// parseIEC103Line 解析单行 IEC103 点表
func parseIEC103Line(record []string, headerMap map[string]int, lineNum int) (iec103PointCSV, error) {
	var point iec103PointCSV

	// Name
	point.Name = record[headerMap["Name"]]
	if point.Name == "" {
		return point, fmt.Errorf("line %d: Name cannot be empty", lineNum)
	}

	// CA (Common Address)
	ca, err := strconv.ParseUint(record[headerMap["CA"]], 10, 8)
	if err != nil {
		return point, fmt.Errorf("line %d: invalid CA", lineNum)
	}
	point.CA = uint8(ca)

	// FUN (Function Type) - IEC103 特有
	fun, err := strconv.ParseUint(record[headerMap["FUN"]], 10, 8)
	if err != nil {
		return point, fmt.Errorf("line %d: invalid FUN", lineNum)
	}
	point.FUN = uint8(fun)

	// INF (Information Number) - IEC103 特有
	inf, err := strconv.ParseUint(record[headerMap["INF"]], 10, 8)
	if err != nil {
		return point, fmt.Errorf("line %d: invalid INF", lineNum)
	}
	point.INF = uint8(inf)

	// TypeID (可选)
	if idx, ok := headerMap["TypeID"]; ok && idx < len(record) && record[idx] != "" {
		typeID, err := strconv.ParseUint(record[idx], 10, 8)
		if err != nil {
			point.TypeID = 0
		} else {
			point.TypeID = uint8(typeID)
		}
	} else {
		point.TypeID = 0
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

	return point, nil
}

// convertIEC103Point 将 CSV 点转换为 PointConfig
func convertIEC103Point(pt iec103PointCSV) PointConfig {
	// 转换 DeadbandType
	var deadbandType DeadbandType
	switch pt.DeadbandType {
	case "percent":
		deadbandType = DeadbandPercent
	default:
		deadbandType = DeadbandAbsolute
	}

	return PointConfig{
		Name:          pt.Name,
		CA:            pt.CA,
		FUN:           pt.FUN,
		INF:           pt.INF,
		TypeID:        pt.TypeID,
		Scale:         pt.Scale,
		Offset:        pt.Offset,
		DeadbandValue: pt.DeadbandValue,
		DeadbandType:  deadbandType,
	}
}
