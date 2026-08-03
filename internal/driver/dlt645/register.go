// internal/driver/dlt645/register.go - DL/T 645 驱动注册
package dlt645

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/gateway/gateway/config"
	"github.com/gateway/gateway/internal/driver"
	"go.uber.org/zap"
)

func init() {
	// 注册驱动
	driver.RegisterDriver("dlt645", NewDLT645DriverFromConfig)
}

// NewDLT645DriverFromConfig 从配置创建驱动实例
func NewDLT645DriverFromConfig(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (driver.Driver, error) {
	// 创建默认配置
	cfg := DefaultConfig()
	cfg.ID = drvCfg.ID
	cfg.Name = drvCfg.Name

	// 解析串口配置
	if drvCfg.DLT645 != nil {
		cfg.SerialPort = drvCfg.DLT645.SerialPort
		cfg.BaudRate = drvCfg.DLT645.BaudRate
		cfg.DataBits = drvCfg.DLT645.DataBits
		cfg.StopBits = drvCfg.DLT645.StopBits
		cfg.Parity = drvCfg.DLT645.Parity

		// 协议版本
		cfg.ProtocolVersion = parseProtocolVersion(drvCfg.DLT645.ProtocolVersion)

		// 前导字节
		cfg.UseLeadingByte = drvCfg.DLT645.UseLeadingByte
		cfg.LeadingByteCount = drvCfg.DLT645.LeadingByteCount

		// 超时配置
		if drvCfg.DLT645.CharTimeout > 0 {
			cfg.CharTimeout = drvCfg.DLT645.CharTimeout
		}
		if drvCfg.DLT645.FrameTimeout > 0 {
			cfg.FrameTimeout = drvCfg.DLT645.FrameTimeout
		}
		if drvCfg.DLT645.ResponseTimeout > 0 {
			cfg.ResponseTimeout = drvCfg.DLT645.ResponseTimeout
		}

		// 重试配置
		cfg.MaxRetry = drvCfg.DLT645.MaxRetry
		cfg.RetryInterval = drvCfg.DLT645.RetryInterval

		// 采集间隔
		if drvCfg.DLT645.PollInterval > 0 {
			cfg.PollInterval = drvCfg.DLT645.PollInterval
		}
		if drvCfg.DLT645.QueryIntervalPerPoint > 0 {
			cfg.QueryIntervalPerPoint = drvCfg.DLT645.QueryIntervalPerPoint
		}
	}

	// 解析点表文件
	if drvCfg.PointFile != "" {
		points, err := parsePointsFromCSV(drvCfg.PointFile, cfg.ProtocolVersion, logger)
		if err != nil {
			return nil, fmt.Errorf("parse point file failed: %w", err)
		}
		cfg.Points = append(cfg.Points, points...)
	}

	logger.Info("DLT645 driver created",
		zap.String("port", cfg.SerialPort),
		zap.Int("baud_rate", cfg.BaudRate),
		zap.String("parity", cfg.Parity),
		zap.Int("protocol_version", int(cfg.ProtocolVersion)),
		zap.Int("points", len(cfg.Points)),
	)

	return New(cfg, logger), nil
}

// parseProtocolVersion 解析协议版本
func parseProtocolVersion(version string) ProtocolVersion {
	switch version {
	case "1997", "97":
		return Version1997
	default:
		return Version2007
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 点表解析
// ─────────────────────────────────────────────────────────────────────────────

// parsePointsFromCSV 解析点表 CSV 文件
// CSV 格式：Name,Address,DataID,Scale,Offset,Unit,Precision,Interval,DeadbandValue,DeadbandType
func parsePointsFromCSV(filePath string, version ProtocolVersion, logger *zap.Logger) ([]PointConfig, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open CSV file failed: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true
	reader.Comment = '#'

	// 读取表头
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV header failed: %w", err)
	}

	// 构建表头索引
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[strings.TrimSpace(h)] = i
	}

	// 验证必需字段
	for _, req := range []string{"Name", "Address", "DataID"} {
		if _, ok := headerMap[req]; !ok {
			return nil, fmt.Errorf("CSV header missing required field: %s", req)
		}
	}

	var points []PointConfig
	lineNum := 1
	skipped := 0

	for {
		lineNum++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// 读取错误也跳过，继续处理后续行
			if logger != nil {
				logger.Warn("DLT645 CSV read line error, skipping",
					zap.Int("line", lineNum),
					zap.String("file", filePath),
					zap.Error(err),
				)
			}
			skipped++
			continue
		}

		// 跳过空行
		if len(record) == 0 || (len(record) == 1 && strings.TrimSpace(record[0]) == "") {
			continue
		}

		pt, err := parsePointLine(record, headerMap, version, lineNum)
		if err != nil {
			if logger != nil {
				logger.Warn("DLT645 CSV line parse error, skipping",
					zap.Int("line", lineNum),
					zap.String("file", filePath),
					zap.Strings("record", record),
					zap.Error(err),
				)
			}
			skipped++
			continue
		}

		points = append(points, pt)
	}

	if len(points) == 0 && skipped > 0 {
		return nil, fmt.Errorf("all %d data lines in CSV are invalid: %s", skipped, filePath)
	}

	if logger != nil {
		logger.Info("DLT645 CSV parsed",
			zap.Int("points", len(points)),
			zap.Int("skipped", skipped),
		)
	}

	return points, nil
}

// parsePointLine 解析单行点表
func parsePointLine(record []string, headerMap map[string]int, version ProtocolVersion, lineNum int) (PointConfig, error) {
	var pt PointConfig

	// Name (必需)
	if idx, ok := headerMap["Name"]; ok && idx < len(record) {
		pt.Name = strings.TrimSpace(record[idx])
	}
	if pt.Name == "" {
		return pt, fmt.Errorf("line %d: Name cannot be empty", lineNum)
	}

	// Address (必需)
	if idx, ok := headerMap["Address"]; ok && idx < len(record) {
		pt.Address = strings.TrimSpace(record[idx])
	}
	if pt.Address == "" {
		return pt, fmt.Errorf("line %d: Address cannot be empty", lineNum)
	}

	// DataID (必需)
	if idx, ok := headerMap["DataID"]; ok && idx < len(record) {
		pt.DataID = strings.TrimSpace(record[idx])
	}
	if pt.DataID == "" {
		return pt, fmt.Errorf("line %d: DataID cannot be empty", lineNum)
	}

	// Scale (可选，默认 1.0)
	if idx, ok := headerMap["Scale"]; ok && idx < len(record) && record[idx] != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(record[idx]), 64); err == nil {
			pt.Scale = v
		} else {
			pt.Scale = 1.0
		}
	} else {
		pt.Scale = 1.0
	}

	// Offset (可选，默认 0)
	if idx, ok := headerMap["Offset"]; ok && idx < len(record) && record[idx] != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(record[idx]), 64); err == nil {
			pt.Offset = v
		}
	}

	// Unit (可选)
	if idx, ok := headerMap["Unit"]; ok && idx < len(record) {
		pt.Unit = strings.TrimSpace(record[idx])
	}

	// Precision (可选，默认 0)
	if idx, ok := headerMap["Precision"]; ok && idx < len(record) && record[idx] != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(record[idx])); err == nil {
			pt.Precision = v
		}
	}

	// Interval (可选，默认 0)
	if idx, ok := headerMap["Interval"]; ok && idx < len(record) && record[idx] != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(record[idx])); err == nil && v >= 0 {
			pt.Interval = v
		}
	}

	// DeadbandValue (可选)
	if idx, ok := headerMap["DeadbandValue"]; ok && idx < len(record) && record[idx] != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(record[idx]), 64); err == nil {
			pt.DeadbandValue = v
		}
	}

	// DeadbandType (可选，默认 absolute)
	if idx, ok := headerMap["DeadbandType"]; ok && idx < len(record) && record[idx] != "" {
		if strings.ToLower(strings.TrimSpace(record[idx])) == "percent" {
			pt.DeadbandType = DeadbandPercent
		} else {
			pt.DeadbandType = DeadbandAbsolute
		}
	}

	return pt, nil
}

// ParsePointsCSV 解析 CSV 点表文件
func ParsePointsCSV(filePath string, version ProtocolVersion) ([]PointConfig, error) {
	return parsePointsFromCSV(filePath, version, nil)
}