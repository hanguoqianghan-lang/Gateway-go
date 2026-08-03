package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestLoadGB26875Config(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config.yaml")
	csvPath := filepath.Join(tmpDir, "points.csv")

	yamlContent := `
gateway:
  name: "TestGateway"
  version: "1.0.0"
  log_level: "debug"

drivers:
  - id: "gb26875-test"
    type: "gb26875"
    enabled: true
    name: "test-gb26875"
    point_file: "` + strings.ReplaceAll(csvPath, "\\", "/") + `"
    gb26875:
      host: "0.0.0.0"
      port: 5001
      max_connections: 100
      read_timeout: "10s"
      write_timeout: "5s"
      frame_timeout: "200ms"
      clock_sync_interval: "3600s"
      version: 1
      user_version: 1
      enable_system_metrics: true
      adu_buffer_size: 5000

exporters:
  mqtt:
    enabled: false
  kafka:
    enabled: false
  batch:
    max_size: 500
    max_latency: "200ms"

bus:
  buffer_size: 8192
  deadband_threshold: 0
`
	csvContent := `Name,DeviceAddress,MessageType,SystemType,SystemAddress,ComponentType,ComponentAddr,AnalogType,AddrFormat,Scale,Offset,DeadbandValue,DeadbandType,Description
sys_status_1,800D00000000,1,1,3,,,,1,1,0,,系统状态
comp_status_1,800D00000000,2,1,3,30,01000100,,1,1,0,,部件运行状态
temp_1,800D00000000,3,1,1,30,01000100,3,1,0.1,0,0.5,absolute,温度传感器
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	logger, _ := zap.NewDevelopment()
	loader := NewLoader(yamlPath, logger)
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 验证
	if cfg.Gateway.Name != "TestGateway" {
		t.Errorf("gateway name: %s", cfg.Gateway.Name)
	}

	if len(cfg.Drivers) != 1 {
		t.Fatalf("want 1 driver, got %d", len(cfg.Drivers))
	}

	drv := cfg.Drivers[0]
	if drv.ID != "gb26875-test" {
		t.Errorf("driver id: %s", drv.ID)
	}
	if drv.Type != "gb26875" {
		t.Errorf("driver type: %s", drv.Type)
	}

	if drv.GB26875 == nil {
		t.Fatal("GB26875 config is nil")
	}

	if drv.GB26875.Host != "0.0.0.0" {
		t.Errorf("host: %s", drv.GB26875.Host)
	}
	if drv.GB26875.Port != 5001 {
		t.Errorf("port: %d", drv.GB26875.Port)
	}
	if drv.GB26875.MaxConnections != 100 {
		t.Errorf("max_connections: %d", drv.GB26875.MaxConnections)
	}
	if drv.GB26875.ReadTimeout != 10*1000000000 {
		t.Errorf("read_timeout: %d", drv.GB26875.ReadTimeout)
	}
	if drv.GB26875.WriteTimeout != 5*1000000000 {
		t.Errorf("write_timeout: %d", drv.GB26875.WriteTimeout)
	}
	if drv.GB26875.FrameTimeout != 200*1000000 {
		t.Errorf("frame_timeout: %d", drv.GB26875.FrameTimeout)
	}
	if drv.GB26875.Version != 1 {
		t.Errorf("version: %d", drv.GB26875.Version)
	}
	if drv.GB26875.UserVersion != 1 {
		t.Errorf("user_version: %d", drv.GB26875.UserVersion)
	}
	if !drv.GB26875.EnableSystemMetrics {
		t.Errorf("enable_system_metrics: %v", drv.GB26875.EnableSystemMetrics)
	}
	if drv.GB26875.ADUBufferSize != 5000 {
		t.Errorf("adu_buffer_size: %d", drv.GB26875.ADUBufferSize)
	}

	// 验证点表
	if len(drv.PointFile) == 0 {
		t.Error("point_file should be set")
	}
}
