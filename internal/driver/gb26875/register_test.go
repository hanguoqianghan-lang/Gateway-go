// internal/driver/gb26875/register_test.go - GB/T 26875.3 CSV 点表解析测试
package gb26875

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"

	"github.com/gateway/gateway/config"
	"go.uber.org/zap"
)

// writeCSV 写入临时 CSV 文件
func writeCSV(t *testing.T, rows [][]string) string {
	t.Helper()
	f, err := os.CreateTemp("", "gb26875-*.csv")
	if err != nil {
		t.Fatalf("create temp csv: %v", err)
	}
	w := csv.NewWriter(f)
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatalf("write csv row: %v", err)
		}
	}
	w.Flush()
	f.Close()
	return f.Name()
}

func TestParsePointsCSV_Basic(t *testing.T) {
	rows := [][]string{
		{"Name", "DeviceAddress", "MessageType", "SystemType", "SystemAddress",
			"ComponentType", "ComponentAddr", "AnalogType", "AddrFormat",
			"Scale", "Offset", "DeadbandValue", "DeadbandType", "Description"},
		{"sys_status_3", "800D00000000", "1", "1", "3", "", "", "", "1", "1", "0", "0", "absolute", "系统状态"},
		{"temp_1", "800D00000000", "3", "1", "1", "30", "01000100", "3", "1", "0.1", "0", "0.5", "absolute", "温度"},
	}
	file := writeCSV(t, rows)
	defer os.Remove(file)

	points, err := ParsePointsCSV(file, zap.NewNop())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("want 2 points, got %d", len(points))
	}

	// 第1个测点：系统状态
	p0 := points[0]
	if p0.Name != "sys_status_3" {
		t.Errorf("name: %s", p0.Name)
	}
	if p0.DeviceAddress != "800D00000000" {
		t.Errorf("dev addr: %s", p0.DeviceAddress)
	}
	if p0.MessageType != 1 {
		t.Errorf("msg type: %d", p0.MessageType)
	}
	if p0.SystemAddress != 3 {
		t.Errorf("sys addr: %d", p0.SystemAddress)
	}
	if p0.AddrFormat != 1 {
		t.Errorf("addr format: %d", p0.AddrFormat)
	}
	if p0.Scale != 1 {
		t.Errorf("scale: %f", p0.Scale)
	}

	// 第2个测点：模拟量
	p1 := points[1]
	if p1.Name != "temp_1" {
		t.Errorf("name: %s", p1.Name)
	}
	if p1.MessageType != 3 {
		t.Errorf("msg type: %d", p1.MessageType)
	}
	if p1.ComponentType != 30 {
		t.Errorf("comp type: %d", p1.ComponentType)
	}
	if p1.ComponentAddr != "01000100" {
		t.Errorf("comp addr: %s", p1.ComponentAddr)
	}
	if p1.AnalogType != 3 {
		t.Errorf("analog type: %d", p1.AnalogType)
	}
	if p1.Scale != 0.1 {
		t.Errorf("scale: %f", p1.Scale)
	}
	if p1.DeadbandValue != 0.5 {
		t.Errorf("deadband: %f", p1.DeadbandValue)
	}
	if p1.Description != "温度" {
		t.Errorf("desc: %s", p1.Description)
	}
}

func TestParsePointsCSV_DefaultValues(t *testing.T) {
	// 只指定 Name 和 MessageType
	rows := [][]string{
		{"Name", "MessageType"},
		{"p1", "2"},
	}
	file := writeCSV(t, rows)
	defer os.Remove(file)

	points, err := ParsePointsCSV(file, zap.NewNop())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("want 1 point, got %d", len(points))
	}

	p := points[0]
	if p.Scale != 1.0 {
		t.Errorf("scale default: %f", p.Scale)
	}
	if p.Offset != 0 {
		t.Errorf("offset default: %f", p.Offset)
	}
	if p.AddrFormat != 1 {
		t.Errorf("addr format default: %d", p.AddrFormat)
	}
	if p.DeadbandType != "absolute" {
		t.Errorf("deadband type default: %s", p.DeadbandType)
	}
}

func TestParsePointsCSV_BadLines(t *testing.T) {
	// 含空行、注释、错误行（应有容忍机制）
	rows := [][]string{
		{"Name", "MessageType", "Scale"},
		{"# this is a comment", "", ""}, // csv.Reader 已会跳过 # 行
		{"p1", "2", "1.0"},
		{"", "", ""},               // 空行，跳过
		{"p2", "not-a-number", ""}, // 错误行，跳过并警告
		{"p3", "3", ""},            // 正常
	}
	file := writeCSV(t, rows)
	defer os.Remove(file)

	points, err := ParsePointsCSV(file, zap.NewNop())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 应至少保留 p1 和 p3
	if len(points) < 2 {
		t.Errorf("want >= 2 points, got %d", len(points))
	}
}

func TestParsePointsCSV_MissingName(t *testing.T) {
	rows := [][]string{
		{"Name", "MessageType"},
		{"", "1"},
	}
	file := writeCSV(t, rows)
	defer os.Remove(file)

	_, err := ParsePointsCSV(file, zap.NewNop())
	// 该行 Name 为空 + MessageType 不为空 → 解析失败 → 触发"所有行无效"错误
	if err == nil {
		t.Error("expected error when Name is empty")
	}
}

func TestParsePointsCSV_MissingHeader(t *testing.T) {
	rows := [][]string{
		{"Foo", "Bar"},
		{"1", "2"},
	}
	file := writeCSV(t, rows)
	defer os.Remove(file)

	_, err := ParsePointsCSV(file, zap.NewNop())
	if err == nil {
		t.Error("expected error for missing Name header")
	}
}

func TestParsePointsCSV_AllDataInvalid(t *testing.T) {
	// 所有数据行都错误 → 返回错误
	rows := [][]string{
		{"Name", "MessageType"},
		{"p1", "abc"},   // bad msgtype
		{"p2", "xyz"},   // bad msgtype
	}
	file := writeCSV(t, rows)
	defer os.Remove(file)

	_, err := ParsePointsCSV(file, zap.NewNop())
	if err == nil {
		t.Error("expected error when all data lines are invalid")
	}
}

func TestParsePointsCSV_FileNotFound(t *testing.T) {
	_, err := ParsePointsCSV("nonexistent.csv", zap.NewNop())
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestParsePointsCSV_ComponentAddr(t *testing.T) {
	rows := [][]string{
		{"Name", "MessageType", "ComponentAddr"},
		{"p1", "2", "01000100"},
		{"p2", "2", "01-00-01-00"},
		{"p3", "2", "AABBCCDD"},
	}
	file := writeCSV(t, rows)
	defer os.Remove(file)

	points, err := ParsePointsCSV(file, zap.NewNop())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("want 3, got %d", len(points))
	}
	for _, p := range points {
		if p.ComponentAddr == "" {
			t.Errorf("ComponentAddr should be set")
		}
	}
}

func TestParsePointsCSV_DeadbandTypes(t *testing.T) {
	rows := [][]string{
		{"Name", "MessageType", "DeadbandType"},
		{"p1", "1", "absolute"},
		{"p2", "1", "percent"},
		{"p3", "1", "unknown"}, // 默认 absolute
	}
	file := writeCSV(t, rows)
	defer os.Remove(file)

	points, err := ParsePointsCSV(file, zap.NewNop())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if points[0].DeadbandType != "absolute" {
		t.Errorf("p1: %s", points[0].DeadbandType)
	}
	if points[1].DeadbandType != "percent" {
		t.Errorf("p2: %s", points[1].DeadbandType)
	}
	if points[2].DeadbandType != "absolute" {
		t.Errorf("p3: %s", points[2].DeadbandType)
	}
}

// TestNewGB26875DriverFromConfig 验证驱动工厂函数
func TestNewGB26875DriverFromConfig(t *testing.T) {
	rows := [][]string{
		{"Name", "MessageType"},
		{"p1", "2"},
	}
	csvPath := writeCSV(t, rows)
	defer os.Remove(csvPath)

	drvCfg := config.DriverConfig{
		ID:        "test-1",
		Name:      "test-gb26875",
		Type:      "gb26875",
		Enabled:   true,
		PointFile: csvPath,
	}

	drv, err := NewGB26875DriverFromConfig(context.Background(), drvCfg, zap.NewNop())
	if err != nil {
		t.Fatalf("create driver: %v", err)
	}
	if drv.Name() != "gb26875" {
		t.Errorf("name: %s", drv.Name())
	}
}

// TestNewGB26875DriverFromConfig_BadCSV CSV 解析失败
func TestNewGB26875DriverFromConfig_BadCSV(t *testing.T) {
	drvCfg := config.DriverConfig{
		ID:        "test-1",
		Name:      "test-gb26875",
		Type:      "gb26875",
		Enabled:   true,
		PointFile: "nonexistent.csv",
	}

	_, err := NewGB26875DriverFromConfig(context.Background(), drvCfg, zap.NewNop())
	if err == nil {
		t.Error("expected error for missing point file")
	}
}

// TestNewGB26875DriverFromConfig_NoPoints 无点表也能创建驱动
func TestNewGB26875DriverFromConfig_NoPoints(t *testing.T) {
	drvCfg := config.DriverConfig{
		ID:      "test-1",
		Name:    "test-gb26875",
		Type:    "gb26875",
		Enabled: true,
		// 无 PointFile
	}

	drv, err := NewGB26875DriverFromConfig(context.Background(), drvCfg, zap.NewNop())
	if err != nil {
		t.Fatalf("create driver: %v", err)
	}
	if drv == nil {
		t.Fatal("driver is nil")
	}
}

// TestInit 验证 Init 流程
func TestInit_ValidConfig(t *testing.T) {
	cfg := Config{
		Name: "test",
		Host: "127.0.0.1",
		Port: 5001,
		Points: []PointConfig{
			{Name: "p1", MessageType: 2, SystemAddress: 1},
			{Name: "p2", MessageType: 3, SystemAddress: 1, ComponentType: 30, ComponentAddr: "01000100"},
		},
	}
	cfg.fillDefaults()

	drv := New(cfg, zap.NewNop())
	if err := drv.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
}

// TestInit_InvalidName 缺少 Name 字段
func TestInit_InvalidName(t *testing.T) {
	cfg := Config{Port: 5001}
	cfg.fillDefaults()

	drv := New(cfg, zap.NewNop())
	if err := drv.Init(context.Background()); err == nil {
		t.Error("expected error for missing Name")
	}
}

// TestInit_BadComponentAddr ComponentAddr 格式错误
func TestInit_BadComponentAddr(t *testing.T) {
	cfg := Config{
		Name: "test",
		Port: 5001,
		Points: []PointConfig{
			{Name: "p1", MessageType: 2, ComponentAddr: "BAD"},
		},
	}
	cfg.fillDefaults()

	drv := New(cfg, zap.NewNop())
	if err := drv.Init(context.Background()); err == nil {
		t.Error("expected error for bad ComponentAddr")
	}
}

// helper 防止编译报错
var _ = filepath.Separator