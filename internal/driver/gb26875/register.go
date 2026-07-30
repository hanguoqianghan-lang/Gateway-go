// internal/driver/gb26875/register.go - GB/T 26875.3 驱动注册
package gb26875

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
	driver.RegisterDriver("gb26875", NewGB26875DriverFromConfig)
}

// NewGB26875DriverFromConfig 从配置创建驱动实例
// 此函数注册到驱动工厂，由工厂统一调用
func NewGB26875DriverFromConfig(ctx context.Context, drvCfg config.DriverConfig, logger *zap.Logger) (driver.Driver, error) {
	cfg := Config{
		Name: drvCfg.Name,
	}
	cfg.fillDefaults()

	// 直接访问 drvCfg.GB26875 字段（S5 已添加到 config.DriverConfig）
	if drvCfg.GB26875 != nil {
		gb := drvCfg.GB26875
		if gb.Host != "" {
			cfg.Host = gb.Host
		}
		if gb.Port > 0 {
			cfg.Port = gb.Port
		}
		if gb.LocalAddress != "" {
			cfg.LocalAddress = gb.LocalAddress
		}
		if gb.MaxConnections > 0 {
			cfg.MaxConnections = gb.MaxConnections
		}
		if gb.ReadTimeout > 0 {
			cfg.ReadTimeout = gb.ReadTimeout
		}
		if gb.WriteTimeout > 0 {
			cfg.WriteTimeout = gb.WriteTimeout
		}
		if gb.FrameTimeout > 0 {
			cfg.FrameTimeout = gb.FrameTimeout
		}
		if gb.ClockSyncInterval > 0 {
			cfg.ClockSyncInterval = gb.ClockSyncInterval
		}
		if gb.Version > 0 {
			cfg.Version = gb.Version
		}
		if gb.UserVersion > 0 {
			cfg.UserVersion = gb.UserVersion
		}
		cfg.EnableSystemMetrics = gb.EnableSystemMetrics
	}

	// 解析点表 CSV
	if drvCfg.PointFile != "" {
		points, err := ParsePointsCSV(drvCfg.PointFile, logger)
		if err != nil {
			return nil, fmt.Errorf("parse point file failed: %w", err)
		}
		cfg.Points = append(cfg.Points, points...)
	}

	logger.Info("GB/T 26875.3 driver created",
		zap.String("host", cfg.Host),
		zap.Int("port", cfg.Port),
		zap.Int("points", len(cfg.Points)),
	)

	return New(cfg, logger), nil
}

// ── CSV 点表解析 ─────────────────────────────────────────────────────

// ParsePointsCSV 解析 CSV 点表文件（公开 API）
func ParsePointsCSV(filePath string, logger *zap.Logger) ([]PointConfig, error) {
	return parsePointsCSV(filePath, logger)
}

// parsePointsCSV 解析 CSV 点表（内部实现）
//
// CSV 格式：
//
//	Name,DeviceAddress,MessageType,SystemType,SystemAddress,
//	ComponentType,ComponentAddr,AnalogType,AddrFormat,
//	Scale,Offset,DeadbandValue,DeadbandType,Description
func parsePointsCSV(filePath string, logger *zap.Logger) ([]PointConfig, error) {
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
	for _, req := range []string{"Name", "MessageType"} {
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
			if logger != nil {
				logger.Warn("GB26875 CSV read line error, skipping",
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

		pt, err := parsePointLine(record, headerMap, lineNum)
		if err != nil {
			if logger != nil {
				logger.Warn("GB26875 CSV line parse error, skipping",
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
		logger.Info("GB26875 CSV parsed",
			zap.Int("points", len(points)),
			zap.Int("skipped", skipped),
		)
	}

	return points, nil
}

// parsePointLine 解析单行点表
func parsePointLine(record []string, headerMap map[string]int, lineNum int) (PointConfig, error) {
	var pt PointConfig

	// Name (必需)
	if idx, ok := headerMap["Name"]; ok && idx < len(record) {
		pt.Name = strings.TrimSpace(record[idx])
	}
	if pt.Name == "" {
		return pt, fmt.Errorf("line %d: Name cannot be empty", lineNum)
	}

	// DeviceAddress (可选，6字节HEX字符串)
	if idx, ok := headerMap["DeviceAddress"]; ok && idx < len(record) && strings.TrimSpace(record[idx]) != "" {
		pt.DeviceAddress = strings.TrimSpace(record[idx])
		// 验证格式（不是必需验证，但提前报错有助于排错）
		if _, err := ParseAddrString(pt.DeviceAddress); err != nil {
			// 允许短格式（4字节）：当作部件地址解析
			if _, err4 := ParseComponentAddrString(pt.DeviceAddress); err4 != nil {
				return pt, fmt.Errorf("line %d: DeviceAddress 解析失败: %w", lineNum, err)
			}
		}
	}

	// MessageType (必需)
	if idx, ok := headerMap["MessageType"]; ok && idx < len(record) {
		s := strings.TrimSpace(record[idx])
		if s == "" {
			return pt, fmt.Errorf("line %d: MessageType cannot be empty", lineNum)
		}
		v, err := strconv.ParseUint(s, 10, 8)
		if err != nil {
			return pt, fmt.Errorf("line %d: MessageType 解析失败: %s", lineNum, s)
		}
		pt.MessageType = uint8(v)
	}

	// SystemType (可选，默认 0)
	if idx, ok := headerMap["SystemType"]; ok && idx < len(record) && record[idx] != "" {
		v, err := strconv.ParseUint(strings.TrimSpace(record[idx]), 10, 8)
		if err != nil {
			return pt, fmt.Errorf("line %d: SystemType 解析失败", lineNum)
		}
		pt.SystemType = uint8(v)
	}

	// SystemAddress (可选，默认 0)
	if idx, ok := headerMap["SystemAddress"]; ok && idx < len(record) && record[idx] != "" {
		v, err := strconv.ParseUint(strings.TrimSpace(record[idx]), 10, 8)
		if err != nil {
			return pt, fmt.Errorf("line %d: SystemAddress 解析失败", lineNum)
		}
		pt.SystemAddress = uint8(v)
	}

	// ComponentType (可选)
	if idx, ok := headerMap["ComponentType"]; ok && idx < len(record) && record[idx] != "" {
		v, err := strconv.ParseUint(strings.TrimSpace(record[idx]), 10, 8)
		if err != nil {
			return pt, fmt.Errorf("line %d: ComponentType 解析失败", lineNum)
		}
		pt.ComponentType = uint8(v)
	}

	// ComponentAddr (可选，4字节HEX字符串)
	if idx, ok := headerMap["ComponentAddr"]; ok && idx < len(record) && record[idx] != "" {
		pt.ComponentAddr = strings.TrimSpace(record[idx])
		if _, err := ParseComponentAddrString(pt.ComponentAddr); err != nil {
			return pt, fmt.Errorf("line %d: ComponentAddr 解析失败: %w", lineNum, err)
		}
	}

	// AnalogType (可选)
	if idx, ok := headerMap["AnalogType"]; ok && idx < len(record) && record[idx] != "" {
		v, err := strconv.ParseUint(strings.TrimSpace(record[idx]), 10, 8)
		if err != nil {
			return pt, fmt.Errorf("line %d: AnalogType 解析失败", lineNum)
		}
		pt.AnalogType = uint8(v)
	}

	// AddrFormat (可选，默认 1)
	if idx, ok := headerMap["AddrFormat"]; ok && idx < len(record) && record[idx] != "" {
		v, err := strconv.ParseUint(strings.TrimSpace(record[idx]), 10, 8)
		if err != nil {
			pt.AddrFormat = 1
		} else {
			pt.AddrFormat = uint8(v)
		}
	} else {
		pt.AddrFormat = 1
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

	// DeadbandValue (可选)
	if idx, ok := headerMap["DeadbandValue"]; ok && idx < len(record) && record[idx] != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(record[idx]), 64); err == nil {
			pt.DeadbandValue = v
		}
	}

	// DeadbandType (可选，默认 absolute)
	if idx, ok := headerMap["DeadbandType"]; ok && idx < len(record) && record[idx] != "" {
		pt.DeadbandType = strings.ToLower(strings.TrimSpace(record[idx]))
		if pt.DeadbandType != "absolute" && pt.DeadbandType != "percent" {
			pt.DeadbandType = "absolute"
		}
	} else {
		pt.DeadbandType = "absolute"
	}

	// Description (可选)
	if idx, ok := headerMap["Description"]; ok && idx < len(record) {
		pt.Description = strings.TrimSpace(record[idx])
	}

	return pt, nil
}