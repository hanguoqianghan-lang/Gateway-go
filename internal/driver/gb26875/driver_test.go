// internal/driver/gb26875/driver_test.go - GB/T 26875.3 驱动集成测试
package gb26875

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gateway/gateway/internal/broker"
	"go.uber.org/zap"
)

// h2b3 辅助：HEX 字符串→字节
func h2b3(s string) []byte {
	c := ""
	for _, ch := range s {
		if ch != ' ' {
			c += string(ch)
		}
	}
	b, _ := hex.DecodeString(c)
	return b
}

// buildUploadFrame 用驱动 API 构建一个合法上行帧
func buildUploadFrame(t *testing.T, srcAddr [6]byte, seq uint16, adu []byte) []byte {
	dst := [6]byte{0, 0, 0, 0, 0, 0}
	tl := TimeLabel{0x22, 0x11, 0x0D, 0x0A, 0x09, 0x15}
	return BuildFrame(seq, 1, 1, tl, srcAddr, dst, CmdSendData, adu)
}

// startTestDriver 启动测试驱动到随机端口，返回 bus + 端口
func startTestDriver(t *testing.T, points []PointConfig) (*Driver, *broker.Bus, int) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close() // 关闭，让驱动去 bind

	bus := broker.NewBusWithConfig(broker.BusConfig{
		BufferSize:        1000,
		DeadbandThreshold: 0,
		SubBufferSize:     100,
	})

	logger := zap.NewNop()
	cfg := Config{
		Name:           "test-gb26875",
		Host:           "127.0.0.1",
		Port:           port,
		MaxConnections: 10,
		ReadTimeout:    2 * time.Second,
		WriteTimeout:   2 * time.Second,
		FrameTimeout:   200 * time.Millisecond,
		Version:        1,
		UserVersion:    1,
		Points:         points,
	}
	cfg.fillDefaults()

	drv := New(cfg, logger)
	if err := drv.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := drv.Start(context.Background(), bus); err != nil {
		t.Fatalf("start: %v", err)
	}

	// 等待监听启动
	time.Sleep(100 * time.Millisecond)
	return drv, bus, port
}

// connectClient 用作传输装置客户端连接到测试驱动
func connectClient(t *testing.T, port int) net.Conn {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// readFrame 阻塞读 N 字节，超时 2s
func readFrame(t *testing.T, conn net.Conn, wantLen int) []byte {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, wantLen)
	_, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf
}

// TestEnd2End_AckAndPublish 端到端测试：客户端发上行帧 → 驱动回确认 → bus 收到 PointData
func TestEnd2End_AckAndPublish(t *testing.T) {
	// 测点：系统状态 类型1 / SysAddr=3
	points := []PointConfig{
		{
			Name:          "sys_status_3",
			MessageType:   TypeUploadSystemStatus,
			SystemAddress: 3,
			Scale:         1.0,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())

	// 订阅 bus
	sub := bus.Subscribe()
	go func() {
		// 消费避免 bus 阻塞
		for range sub {
		}
	}()

	// 客户端连接
	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}

	// 构造 ADU：类型1 + 数目1 + 系统状态4字节 (系统类型=1, 系统地址=3, 状态=0x1234)
	adu := []byte{TypeUploadSystemStatus, 0x01, 0x01, 0x03, 0x34, 0x12}
	frame := buildUploadFrame(t, srcAddr, 1, adu)

	conn := connectClient(t, port)
	defer conn.Close()

	// 写入帧
	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 接收确认帧（命令字=3，无ADU，最小30字节）
	ack := readFrame(t, conn, 30)
		if len(ack) < 30 {
		t.Fatalf("ack too short: %d", len(ack))
	}
	if ack[0] != FrameStart1 || ack[1] != FrameStart2 {
		t.Errorf("ack bad start: % X", ack[:2])
	}
	if ack[len(ack)-2] != FrameEnd1 || ack[len(ack)-1] != FrameEnd2 {
		t.Errorf("ack bad end: % X", ack[len(ack)-2:])
	}

	// cmd 字节在 ack[26]（启动符2 + 流水2 + 版本2 + 时间6 + 源6 + 目的6 + ADU长度2 = 26）
	if ack[26] != CmdConfirm {
		t.Errorf("cmd should be Confirm(3), got %d", ack[26])
	}

	// 给 ACK 发送 goroutine 一个机会完成统计更新
	time.Sleep(50 * time.Millisecond)

	// 验证 bus 是否收到 PointData
	// 由于驱动直接调用 bus.Publish，订阅者会收到
	// 但我们另起一个订阅测试
	stats := drv.Stats()
	if stats["frames_parsed"].(uint64) < 1 {
		t.Errorf("frames_parsed should be >= 1, got %v", stats["frames_parsed"])
	}
	if stats["ack_sent"].(uint64) < 1 {
		t.Errorf("ack_sent should be >= 1, got %v", stats["ack_sent"])
	}
}

// TestEnd2End_ComponentAnalog 端到端：模拟量类型3 测点匹配
func TestEnd2End_ComponentAnalog(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "temp_1",
			MessageType:   TypeUploadComponentAnalog,
			SystemAddress: 1,
			ComponentType: 30,
			ComponentAddr: "01000100", // 部件地址（4字节HEX，存储为小端）
			Scale:         0.1,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	conn := connectClient(t, port)
	defer conn.Close()

	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	// ADU: 类型3 + 数目1 + 模拟量10字节
	// 系统类型=1, 系统地址=1, 部件类型=30, 部件地址=01000100(小端=0x00 0x01 0x00 0x01), 模拟量类型=1(温度), 值=250 (=25.0℃)
	adu := []byte{
		TypeUploadComponentAnalog, 0x01,
		0x01, 0x01, 0x1E, 0x00, 0x01, 0x00, 0x01, 0x01, 0xFA, 0x00,
	}
	frame := buildUploadFrame(t, srcAddr, 1, adu)

	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	conn.Write(frame)

	// 收到确认
	ack := readFrame(t, conn, 30)
	if ack[26] != CmdConfirm {
		t.Errorf("cmd should be Confirm(3), got %d", ack[26])
	}

	// 等待驱动处理
	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	t.Logf("Stats: %+v", stats)
	// Add some debug - check if frame was parsed
	if stats["frames_parsed"].(uint64) < 1 {
		t.Errorf("frames_parsed should be >= 1, got %v", stats["frames_parsed"])
	}
	if stats["points_published"].(uint64) < 1 {
		t.Errorf("points_published should be >= 1, got %v", stats["points_published"])
	}
}

// TestEnd2End_NoMatchingPoint 没匹配的测点也应回复 ACK（不重发）
func TestEnd2End_NoMatchingPoint(t *testing.T) {
	// 故意不注册任何测点
	drv, _, port := startTestDriver(t, nil)
	defer drv.Stop(context.Background())

	conn := connectClient(t, port)
	defer conn.Close()

	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	adu := []byte{TypeUploadSystemStatus, 0x01, 0x01, 0x03, 0x34, 0x12}
	frame := buildUploadFrame(t, srcAddr, 1, adu)
	t.Logf("Frame length=%d, hex=%X", len(frame), frame)

	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	n, err := conn.Write(frame)
	t.Logf("Write n=%d err=%v", n, err)

	ack := readFrame(t, conn, 30)
	t.Logf("Got ack: % X", ack)

	if ack[26] != CmdConfirm {
		t.Errorf("cmd should be Confirm even no match, got %d", ack[26])
	}

	time.Sleep(50 * time.Millisecond)
	stats := drv.Stats()
	t.Logf("Stats: %+v", stats)
	if stats["points_published"].(uint64) != 0 {
		t.Errorf("points_published should be 0 (no matching point), got %v", stats["points_published"])
	}
}

// TestEnd2End_BadFrame 损坏帧应被丢弃
func TestEnd2End_BadFrame(t *testing.T) {
	drv, _, port := startTestDriver(t, nil)
	defer drv.Stop(context.Background())

	conn := connectClient(t, port)
	defer conn.Close()

	// 构造一个含启动符但校验和错误的"看起来像帧"的数据
	// 帧结构: 40 40 + 25字节CU + 0字节ADU + 1字节CS(故意错) + 23 23
	badFrame := make([]byte, 30)
	badFrame[0] = 0x40
	badFrame[1] = 0x40
	// CS 用 0xFF（错误）
	badFrame[27] = 0xFF
	badFrame[28] = 0x23
	badFrame[29] = 0x23

	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	conn.Write(badFrame)

	// 不应收到任何确认帧
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 30)
	_, err := conn.Read(buf)
	// 应有超时错误
	if err == nil {
		t.Error("expected read timeout for bad frame (no response)")
	}

	time.Sleep(50 * time.Millisecond)
	stats := drv.Stats()
	if stats["frames_received"].(uint64) < 1 {
		t.Errorf("frames_received should be >= 1, got %v", stats["frames_received"])
	}
	if stats["frames_rejected"].(uint64) < 1 {
		t.Errorf("frames_rejected should be >= 1, got %v", stats["frames_rejected"])
	}
}

// TestBuildFrame_RealRoundtrip 用真实报文数据 roundtrip
func TestBuildFrame_RealRoundtrip(t *testing.T) {
	// 案例1 hex（含CS）= 案例1 hex 报文的字节流
	raw := h2b3("404000000101011E16080C15800D00000000000000000000040002190101000E2323")

	// 解析 → 重新构建 → 再解析
	f1, err := ParseFrame(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	built := BuildFrame(f1.SequenceNo, f1.Version, f1.UserVer, f1.Time, f1.SrcAddr, f1.DstAddr, f1.Cmd, f1.ADU)
	f2, err := ParseFrame(built)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if f1.SequenceNo != f2.SequenceNo || f1.Cmd != f2.Cmd {
		t.Errorf("roundtrip mismatch")
	}
}

// TestEnd2End_ComponentStatus 端到端：部件运行状态类型2 测点匹配
func TestEnd2End_ComponentStatus(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "comp_1",
			MessageType:   TypeUploadComponentStatus,
			SystemAddress: 3,
			ComponentType: 30,
			ComponentAddr: "01000100",
			Scale:         1.0,
			Offset:        0,
		},
	}

	// Debug: verify points before creating driver
	for i, p := range points {
		t.Logf("Input point[%d]: msgType=%d sysAddr=%d compType=%d compAddr=%s",
			i, p.MessageType, p.SystemAddress, p.ComponentType, p.ComponentAddr)
	}

	// Debug: test ParseComponentAddrString directly
	b, err := ParseComponentAddrString("01000100")
	t.Logf("ParseComponentAddrString('01000100') = [%02X %02X %02X %02X] err=%v",
		b[0], b[1], b[2], b[3], err)
	if err == nil {
		raw := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
		key := uint64(uint8(2))<<40 | uint64(uint8(3))<<32 | uint64(uint8(30))<<24 | uint64(raw)
		t.Logf("Expected key: 0x%X (type=2 sys=3 comptype=30 raw=0x%X)", key, raw)
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	// Debug: print point map keys
	drv.pointMu.RLock()
	for k, v := range drv.pointMap {
		t.Logf("PointMap key=0x%X (%d): name=%s msgType=%d sysAddr=%d compType=%d compAddr=%s",
			k, k, v.Name, v.MessageType, v.SystemAddress, v.ComponentType, v.ComponentAddr)
	}
	drv.pointMu.RUnlock()

	conn := connectClient(t, port)
	defer conn.Close()

	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	// ADU: 类型2 + 数目1 + 部件状态40字节
	// 部件状态格式:
	//   系统类型=1(1字节), 系统地址=3(1字节), 部件类型=30(1字节), 部件地址=01000100小端=00 01 00 01(4字节)
	//   运行状态=1(2字节小端=01 00), 后续填充到40字节
	adu := []byte{
		TypeUploadComponentStatus, 0x01,  // type=2, count=1
		0x01, 0x03, 0x1E,  // systemType=1, systemAddress=3, componentType=30
		0x00, 0x01, 0x00, 0x01,  // componentAddr=01000100 (小端存储)
		0x01, 0x00,  // runStatus=1 (小端)
	}
	// 信息对象固定 40 字节：前面已写 9 字节(系统类型/地址/部件类型/部件地址4/运行状态2)，
	// 需再补 31 字节(40-9)到 40 字节，否则 ParseComponentStatus 需 40 字节会解析失败。
	adu = append(adu, make([]byte, 40-9)...)
	frame := buildUploadFrame(t, srcAddr, 1, adu)

	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	conn.Write(frame)

	ack := readFrame(t, conn, 30)
	if ack[26] != CmdConfirm {
		t.Errorf("cmd should be Confirm(3), got %d", ack[26])
	}

	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	t.Logf("Stats: %+v", stats)
	// Add some debug - check if frame was parsed
	if stats["frames_parsed"].(uint64) < 1 {
		t.Errorf("frames_parsed should be >= 1, got %v", stats["frames_parsed"])
	}
	if stats["points_published"].(uint64) < 1 {
		// Debug: check point map keys
		t.Logf("Point map should have key for type=2, sys=3, comp_type=30, comp_addr=01000100")
		t.Errorf("points_published should be >= 1, got %v", stats["points_published"])
	}
}

// TestEnd2End_OperationInfo 端到端：操作信息类型4 测点匹配
func TestEnd2End_OperationInfo(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "op_1",
			MessageType:   TypeUploadOperationInfo,
			SystemAddress: 5,
			Scale:         1.0,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	conn := connectClient(t, port)
	defer conn.Close()

	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	// ADU: 类型4 + 数目1 + 操作信息4字节
	// 系统类型=1, 系统地址=5, 操作员=1, 操作码=1
	adu := []byte{
		TypeUploadOperationInfo, 0x01,
		0x01, 0x05, 0x01, 0x01,
	}
	frame := buildUploadFrame(t, srcAddr, 1, adu)

	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	conn.Write(frame)

	ack := readFrame(t, conn, 30)
	if ack[26] != CmdConfirm {
		t.Errorf("cmd should be Confirm(3), got %d", ack[26])
	}

	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	t.Logf("Stats: %+v", stats)
	// Add some debug - check if frame was parsed
	if stats["frames_parsed"].(uint64) < 1 {
		t.Errorf("frames_parsed should be >= 1, got %v", stats["frames_parsed"])
	}
	if stats["points_published"].(uint64) < 1 {
		t.Errorf("points_published should be >= 1, got %v", stats["points_published"])
	}
}

// TestEnd2End_SWVersion 端到端：软件版本类型5 测点匹配
func TestEnd2End_SWVersion(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "sw_1",
			MessageType:   TypeUploadSWVersion,
			SystemAddress: 6,
			Scale:         1.0,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	conn := connectClient(t, port)
	defer conn.Close()

	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	// ADU: 类型5 + 数目1 + 软件版本4字节
	// 系统类型=1, 系统地址=6, 版本=0x010203
	adu := []byte{
		TypeUploadSWVersion, 0x01,
		0x01, 0x06, 0x03, 0x02, 0x01,
	}
	frame := buildUploadFrame(t, srcAddr, 1, adu)

	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	conn.Write(frame)

	ack := readFrame(t, conn, 30)
	if ack[26] != CmdConfirm {
		t.Errorf("cmd should be Confirm(3), got %d", ack[26])
	}

	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	t.Logf("Stats: %+v", stats)
	// Add some debug - check if frame was parsed
	if stats["frames_parsed"].(uint64) < 1 {
		t.Errorf("frames_parsed should be >= 1, got %v", stats["frames_parsed"])
	}
	if stats["points_published"].(uint64) < 1 {
		t.Errorf("points_published should be >= 1, got %v", stats["points_published"])
	}
}

// TestEnd2End_TransmissionDeviceStatus 端到端：传输装置状态类型21 测点匹配
func TestEnd2End_TransmissionDeviceStatus(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "tx_status_1",
			MessageType:   TypeUploadTransmissionDeviceStatus,
			SystemAddress: 7,
			Scale:         1.0,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	conn := connectClient(t, port)
	defer conn.Close()

	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	// ADU: 类型21 + 数目1 + 传输装置状态
	// 系统类型=1, 系统地址=7, 状态=1, 信号强度=85
	adu := []byte{
		TypeUploadTransmissionDeviceStatus, 0x01,
		0x01, 0x07, 0x01, 0x55, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	frame := buildUploadFrame(t, srcAddr, 1, adu)

	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	conn.Write(frame)

	ack := readFrame(t, conn, 30)
	if ack[26] != CmdConfirm {
		t.Errorf("cmd should be Confirm(3), got %d", ack[26])
	}

	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	t.Logf("Stats: %+v", stats)
	// Add some debug - check if frame was parsed
	if stats["frames_parsed"].(uint64) < 1 {
		t.Errorf("frames_parsed should be >= 1, got %v", stats["frames_parsed"])
	}
	if stats["points_published"].(uint64) < 1 {
		t.Errorf("points_published should be >= 1, got %v", stats["points_published"])
	}
}

// TestEnd2End_DeadbandFilter 端到端：死区过滤测试
func TestEnd2End_DeadbandFilter(t *testing.T) {
	points := []PointConfig{
		{
			Name:           "temp_deadband",
			MessageType:    TypeUploadComponentAnalog,
			SystemAddress:  1,
			ComponentType:  30,
			ComponentAddr:  "01000100",
			Scale:          0.1,
			Offset:         0,
			DeadbandValue:  1.0, // 1.0 度死区
			DeadbandType:   "absolute",
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	conn := connectClient(t, port)
	defer conn.Close()

	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}

	// 第一次发送 25.0°C (raw=250 = 0x00FA 小端)
	// 10字节对象: 系统类型1 + 系统地址1 + 部件类型30 + 部件地址[00 01 00 01]
	//           + 模拟量类型3(温度) + 值(小端=FA 00)
	adu := []byte{
		TypeUploadComponentAnalog, 0x01,
		0x01, 0x01, 0x1E, 0x00, 0x01, 0x00, 0x01, 0x03, 0xFA, 0x00,
	}
	frame := buildUploadFrame(t, srcAddr, 1, adu)
	conn.Write(frame)
	readFrame(t, conn, 30)

	// 等待处理
	time.Sleep(100 * time.Millisecond)

	// 第二次发送 25.5°C (raw=255 = 0x00FF 小端) - 死区内，应被过滤
	adu2 := []byte{
		TypeUploadComponentAnalog, 0x01,
		0x01, 0x01, 0x1E, 0x00, 0x01, 0x00, 0x01, 0x03, 0xFF, 0x00,
	}
	frame2 := buildUploadFrame(t, srcAddr, 2, adu2)
	conn.Write(frame2)
	readFrame(t, conn, 30)

	time.Sleep(100 * time.Millisecond)

	// 第三次发送 26.2°C (raw=262 = 0x0106 小端) - 超出死区，应发布
	adu3 := []byte{
		TypeUploadComponentAnalog, 0x01,
		0x01, 0x01, 0x1E, 0x00, 0x01, 0x00, 0x01, 0x03, 0x06, 0x01,
	}
	frame3 := buildUploadFrame(t, srcAddr, 3, adu3)
	conn.Write(frame3)
	readFrame(t, conn, 30)

	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	// 第一次发布 + 第三次发布 = 2 次
	if stats["points_published"].(uint64) != 2 {
		t.Errorf("points_published should be 2 (first + third, second filtered), got %v", stats["points_published"])
	}
}

// TestEnd2End_MultiConnection 多连接测试
func TestEnd2End_MultiConnection(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "sys_status",
			MessageType:   TypeUploadSystemStatus,
			SystemAddress: 3,
			Scale:         1.0,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	// 两个不同地址的客户端
	srcAddr1 := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00} // 800D00000000
	srcAddr2 := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x01} // 800D00000001

	conn1 := connectClient(t, port)
	defer conn1.Close()
	conn2 := connectClient(t, port)
	defer conn2.Close()

	// 客户端1发送
	adu1 := []byte{TypeUploadSystemStatus, 0x01, 0x01, 0x03, 0x34, 0x12}
	frame1 := buildUploadFrame(t, srcAddr1, 1, adu1)
	conn1.Write(frame1)
	readFrame(t, conn1, 30)

	// 客户端2发送
	adu2 := []byte{TypeUploadSystemStatus, 0x01, 0x01, 0x03, 0x34, 0x12}
	frame2 := buildUploadFrame(t, srcAddr2, 1, adu2)
	conn2.Write(frame2)
	readFrame(t, conn2, 30)

	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	// 两个连接各发送1次，共发布2次
	if stats["points_published"].(uint64) != 2 {
		t.Errorf("points_published should be 2 (two connections), got %v", stats["points_published"])
	}
	if stats["connections"].(uint64) != 2 {
		t.Errorf("connections should be 2, got %v", stats["connections"])
	}
}

// TestEnd2End_ReRegister 重注册测试：首帧前用 remote key，首帧后按源地址重注册
func TestEnd2End_ReRegister(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "sys_status",
			MessageType:   TypeUploadSystemStatus,
			SystemAddress: 3,
			Scale:         1.0,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	conn := connectClient(t, port)
	defer conn.Close()

	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	adu := []byte{TypeUploadSystemStatus, 0x01, 0x01, 0x03, 0x34, 0x12}
	frame := buildUploadFrame(t, srcAddr, 1, adu)
	conn.Write(frame)
	readFrame(t, conn, 30)

	time.Sleep(50 * time.Millisecond)

	stats := drv.Stats()
	// 验证重注册发生（连接 key 从 remote addr 变为源地址）
	// 这里只能间接验证：统计连接数应为1
	if stats["connections"].(uint64) != 1 {
		t.Errorf("connections should be 1, got %v", stats["connections"])
	}
	if stats["points_published"].(uint64) < 1 {
		t.Errorf("points_published should be >= 1, got %v", stats["points_published"])
	}
}

// TestEnd2End_DeviceAddressMatch 端到端：带 DeviceAddress 的测点应正确匹配线网源地址
// 验证 6 字节地址线网字节序约定：线网字节 [0x80,0x0D,0,0,0,0] 与 CSV DeviceAddress "800D00000000" 一致。
func TestEnd2End_DeviceAddressMatch(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "dev_sys_status",
			DeviceAddress: "800D00000000", // 对应线网源地址 [0x80,0x0D,0,0,0,0]
			MessageType:   TypeUploadSystemStatus,
			SystemAddress: 3,
			Scale:         1.0,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	conn := connectClient(t, port)
	defer conn.Close()

	// 客户端源地址 = 0x80,0x0D,0,0,0,0 (= DeviceAddress "800D00000000")
	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	// ADU: 类型1 + 数目1 + 系统状态: 系统类型=1, 系统地址=3, 状态=0x1234
	adu := []byte{TypeUploadSystemStatus, 0x01, 0x01, 0x03, 0x34, 0x12}
	frame := buildUploadFrame(t, srcAddr, 1, adu)
	conn.Write(frame)
	readFrame(t, conn, 30) // 等待 ACK

	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	if stats["frames_parsed"].(uint64) < 1 {
		t.Errorf("frames_parsed should be >= 1, got %v", stats["frames_parsed"])
	}
	// DeviceAddress 匹配 → 发布 1 次
	if stats["points_published"].(uint64) != 1 {
		t.Errorf("points_published should be exactly 1 (matching device), got %v", stats["points_published"])
	}
}

// TestEnd2End_DeviceAddressMismatch 端到端：DeviceAddress 不匹配的测点应被过滤
func TestEnd2End_DeviceAddressMismatch(t *testing.T) {
	points := []PointConfig{
		{
			Name:          "other_dev_status",
			DeviceAddress: "800D00000001", // 不同装置，不应被匹配
			MessageType:   TypeUploadSystemStatus,
			SystemAddress: 3,
			Scale:         1.0,
			Offset:        0,
		},
	}

	drv, bus, port := startTestDriver(t, points)
	defer drv.Stop(context.Background())
	_ = bus

	conn := connectClient(t, port)
	defer conn.Close()

	// 客户端源地址 = 0x80,0x0D,0,0,0,0 (= "800D00000000")，与点表 "800D00000001" 不匹配
	srcAddr := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	adu := []byte{TypeUploadSystemStatus, 0x01, 0x01, 0x03, 0x34, 0x12}
	frame := buildUploadFrame(t, srcAddr, 1, adu)
	conn.Write(frame)
	readFrame(t, conn, 30) // 等待 ACK

	time.Sleep(100 * time.Millisecond)

	stats := drv.Stats()
	if stats["frames_parsed"].(uint64) < 1 {
		t.Errorf("frames_parsed should be >= 1, got %v", stats["frames_parsed"])
	}
	// DeviceAddress 不匹配 → 发布 0 次
	if stats["points_published"].(uint64) != 0 {
		t.Errorf("points_published should be 0 (non-matching device filtered), got %v", stats["points_published"])
	}
}