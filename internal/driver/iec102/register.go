// internal/driver/iec102/register.go - IEC 60870-5-102 驱动注册
package iec102

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/gateway/gateway/config"
	"github.com/gateway/gateway/internal/driver"
	"go.uber.org/zap"
)

func init() {
	// 在包初始化时自动注册驱动
	driver.RegisterDriver("iec102", NewIEC102DriverFromConfig)
}

// iec102PointCSV CSV 点表定义（内部使用）
type iec102PointCSV struct {
	Name          string
	CA            uint8
	IOA           uint16
	TypeID        uint8
	Scale         float64
	Offset        float64
	Interval      int
	DeadbandValue float64
	DeadbandType  string
}

// NewIEC102DriverFromConfig 从配置创建 IEC102 驱动实例
func NewIEC102DriverFromConfig(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (driver.Driver, error) {
	// 解析点表文件
	points, err := parseIEC102CSV(drvCfg.PointFile, logger)
	if err != nil {
		return nil, err
	}

	logger.Info("IEC102 point table parsed", zap.Int("points", len(points)))

	// 转换为 IEC102 配置
	iec102Points := make([]PointConfig, 0, len(points))
	for _, pt := range points {
		iec102Pt := convertIEC102Point(pt)
		iec102Points = append(iec102Points, iec102Pt)
	}

	// 创建 IEC102 配置
	iec102Cfg := DefaultConfig()
	iec102Cfg.ID = drvCfg.ID
	iec102Cfg.Name = drvCfg.Name
	iec102Cfg.Points = iec102Points

	// 解析串口配置
	if drvCfg.IEC102 != nil {
		iec102Cfg.SerialPort = drvCfg.IEC102.SerialPort
		iec102Cfg.BaudRate = drvCfg.IEC102.BaudRate
		iec102Cfg.DataBits = drvCfg.IEC102.DataBits
		iec102Cfg.StopBits = drvCfg.IEC102.StopBits
		iec102Cfg.Parity = drvCfg.IEC102.Parity
		iec102Cfg.CommonAddress = uint8(drvCfg.IEC102.CommonAddress)
		iec102Cfg.LinkAddress = uint8(drvCfg.IEC102.LinkAddress)
		iec102Cfg.BalancedMode = drvCfg.IEC102.BalancedMode
		iec102Cfg.BackgroundScanInterval = drvCfg.IEC102.BackgroundScanInterval
		iec102Cfg.PeriodicReadInterval = drvCfg.IEC102.PeriodicReadInterval
		iec102Cfg.PollInterval = drvCfg.IEC102.PollInterval
		iec102Cfg.ResponseTimeout = drvCfg.IEC102.Timeout
	}

	// 创建 IEC102 驱动
	drv := New(iec102Cfg, logger)

	logger.Info("IEC102 driver created",
		zap.String("port", iec102Cfg.SerialPort),
		zap.Int("baud_rate", iec102Cfg.BaudRate),
		zap.String("parity", iec102Cfg.Parity),
	)

	return drv, nil
}

// parseIEC102CSV 解析 IEC102 CSV 点表文件
func parseIEC102CSV(filePath string, logger *zap.Logger) ([]iec102PointCSV, error) {
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

	// 验证必需字段
	requiredHeaders := []string{"Name", "CA", "IOA", "TypeID"}
	for _, req := range requiredHeaders {
		if _, ok := headerMap[req]; !ok {
			return nil, fmt.Errorf("CSV header missing required field: %s", req)
		}
	}

	var points []iec102PointCSV
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

		point, err := parseIEC102Line(record, headerMap, lineNum)
		if err != nil {
			return nil, err
		}

		points = append(points, point)
	}

	return points, nil
}

// parseIEC102Line 解析单行 IEC102 点表
func parseIEC102Line(record []string, headerMap map[string]int, lineNum int) (iec102PointCSV, error) {
	var point iec102PointCSV

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

	// IOA (Information Object Address)
	ioa, err := strconv.ParseUint(record[headerMap["IOA"]], 10, 16)
	if err != nil {
		return point, fmt.Errorf("line %d: invalid IOA", lineNum)
	}
	point.IOA = uint16(ioa)

	// TypeID
	typeID, err := strconv.ParseUint(record[headerMap["TypeID"]], 10, 8)
	if err != nil {
		return point, fmt.Errorf("line %d: invalid TypeID", lineNum)
	}
	point.TypeID = uint8(typeID)

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

// convertIEC102Point 将 CSV 点转换为 PointConfig
func convertIEC102Point(pt iec102PointCSV) PointConfig {
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
		IOA:           pt.IOA,
		TypeID:        pt.TypeID,
		Scale:         pt.Scale,
		Offset:        pt.Offset,
		Interval:      pt.Interval,
		DeadbandValue: pt.DeadbandValue,
		DeadbandType:  deadbandType,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 配置结构体扩展（用于 YAML 解析）
// ─────────────────────────────────────────────────────────────────────────────

// IEC102Config IEC102 配置（用于 YAML）
type IEC102Config struct {
	SerialPort            string        `yaml:"serial_port"`
	BaudRate              int           `yaml:"baud_rate"`
	DataBits              int           `yaml:"data_bits"`
	StopBits              int           `yaml:"stop_bits"`
	Parity                string        `yaml:"parity"`
	Timeout               time.Duration `yaml:"timeout"`
	CommonAddress         int           `yaml:"common_address"`
	LinkAddress           int           `yaml:"link_address"`
	BalancedMode          bool          `yaml:"balanced_mode"`
	BackgroundScanInterval time.Duration `yaml:"background_scan_interval"`
	PeriodicReadInterval   time.Duration `yaml:"periodic_read_interval"`
	PollInterval           time.Duration `yaml:"poll_interval"`
	MaxRetry               int           `yaml:"max_retry"`
	RetryInterval          time.Duration `yaml:"retry_interval"`
}
