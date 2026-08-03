// internal/driver/guowang102/client_test.go - TCP 客户端单元测试
package guowang102

import (
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestClientConfig_Defaults(t *testing.T) {
	cfg := DefaultClientConfig()
	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host: want 127.0.0.1, got %s", cfg.Host)
	}
	if cfg.Port != 6960 {
		t.Errorf("Port: want 6960, got %d", cfg.Port)
	}
	if cfg.LinkAddress != DefaultLinkAddress {
		t.Errorf("LinkAddress: want 0xFFFF, got 0x%04X", cfg.LinkAddress)
	}
	if cfg.ConnectTimeout != 10*time.Second {
		t.Errorf("ConnectTimeout: want 10s, got %v", cfg.ConnectTimeout)
	}
}

func TestClient_CreateAndClose(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 0 // 无效端口，连接会失败

	client := NewClient(cfg, logger)
	defer client.Close()

	// 未连接时 IsConnected 应为 false
	if client.IsConnected() {
		t.Error("IsConnected should be false before connect")
	}

	// 连接失败
	err := client.Connect()
	if err == nil {
		t.Error("Connect should fail with invalid port")
	}

	// 关闭未连接的客户端不应报错
	err = client.Close()
	if err != nil {
		t.Errorf("Close unconnected: %v", err)
	}
}

// TestClient_ConnectAcceptLoop 测试连接接受循环（需要真实的 TCP 服务端）
func TestClient_ConnectAcceptLoop(t *testing.T) {
	// 启动一个简单的 TCP 服务端
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().(*net.TCPAddr)
	t.Logf("Test server listening on %s", serverAddr.String())

	// 接收连接的 goroutine
	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			t.Logf("accept error: %v", err)
			return
		}
		connCh <- conn
	}()

	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = serverAddr.Port
	cfg.ConnectTimeout = 5 * time.Second

	client := NewClient(cfg, logger)
	defer client.Close()

	// 连接
	err = client.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	if !client.IsConnected() {
		t.Error("IsConnected should be true after connect")
	}

	// 等待服务端接受连接
	select {
	case serverConn := <-connCh:
		t.Logf("Server accepted connection from %s", serverConn.RemoteAddr())
		serverConn.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not accept connection")
	}

	// 验证统计
	stats := client.GetStats()
	if stats.TxFrames == 0 && stats.RxFrames == 0 {
		t.Log("No frames exchanged yet (expected for connect-only test)")
	}
	if stats.Reconnects == 0 {
		t.Error("Reconnects should be >= 1")
	}
}

func TestClient_SendFixedFrame(t *testing.T) {
	// 启动 TCP 服务端
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().(*net.TCPAddr)

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		connCh <- conn
	}()

	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = serverAddr.Port
	cfg.ConnectTimeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second

	client := NewClient(cfg, logger)
	defer client.Close()

	err = client.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	serverConn := <-connCh
	defer serverConn.Close()

	// 发送固定帧：复位链路
	frame := BuildResetLink(DefaultLinkAddress)
	err = client.SendFrame(frame)
	if err != nil {
		t.Fatalf("SendFrame failed: %v", err)
	}

	// 服务端读取并验证
	buf := make([]byte, 64)
	n, err := serverConn.Read(buf)
	if err != nil {
		t.Fatalf("Server read failed: %v", err)
	}
	received := buf[:n]
	t.Logf("Server received: %X", received)

	// 验证帧结构
	if len(received) != 6 {
		t.Errorf("Expected 6 bytes, got %d", len(received))
	}
	if received[0] != StartByteFixed {
		t.Errorf("Start byte: want 0x10, got 0x%02X", received[0])
	}
	if received[5] != EndByte {
		t.Errorf("End byte: want 0x16, got 0x%02X", received[5])
	}

	// 统计检查
	stats := client.GetStats()
	if stats.TxFrames != 1 {
		t.Errorf("TxFrames: want 1, got %d", stats.TxFrames)
	}
}

func TestClient_SendVariableFrame(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().(*net.TCPAddr)

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		connCh <- conn
	}()

	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = serverAddr.Port
	cfg.ConnectTimeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second

	client := NewClient(cfg, logger)
	defer client.Close()

	err = client.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	serverConn := <-connCh
	defer serverConn.Close()

	// 发送可变帧
	asdu := []byte{0x01, 0x01, 0x07, 0xFF, 0xFF, 0x00, 0x48, 0x65, 0x6C, 0x6C, 0x6F}
	frame := BuildVariableFrame(0x53, DefaultLinkAddress, asdu) // FC=3, FCV=1, PRM=1
	err = client.SendFrame(frame)
	if err != nil {
		t.Fatalf("SendFrame failed: %v", err)
	}

	// 服务端读取
	buf := make([]byte, 128)
	n, err := serverConn.Read(buf)
	if err != nil {
		t.Fatalf("Server read failed: %v", err)
	}
	received := buf[:n]
	t.Logf("Server received %d bytes: %X", n, received)

	// 验证可变帧结构
	if received[0] != StartByteVariable {
		t.Errorf("Start byte: want 0x68, got 0x%02X", received[0])
	}
	if received[3] != StartByteVariable {
		t.Errorf("Second start: want 0x68, got 0x%02X", received[3])
	}
	if received[n-1] != EndByte {
		t.Errorf("End byte: want 0x16, got 0x%02X", received[n-1])
	}
}

func TestClient_ReceiveFrame_Fixed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().(*net.TCPAddr)

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		connCh <- conn
	}()

	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = serverAddr.Port
	cfg.ConnectTimeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second

	client := NewClient(cfg, logger)
	defer client.Close()

	err = client.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	serverConn := <-connCh
	defer serverConn.Close()

	// 服务端发送固定帧确认 (FC=0, PRM=0)
	ackFrame := []byte{0x10, 0x00, 0xFF, 0xFF, 0xFE, 0x16} // C=0, A=FFFF, CS=FE, 16H
	_, err = serverConn.Write(ackFrame)
	if err != nil {
		t.Fatalf("Server write failed: %v", err)
	}

	// 客户端接收
	frameData, isSingle, err := client.ReceiveFrame()
	if err != nil {
		t.Fatalf("ReceiveFrame failed: %v", err)
	}
	if isSingle {
		t.Error("Expected fixed frame, got single ACK")
	}
	if len(frameData) != 6 {
		t.Errorf("Expected 6 bytes, got %d", len(frameData))
	}
	t.Logf("Client received: %X", frameData)

	// 解析验证
	frame, err := ParseFrame(frameData)
	if err != nil {
		t.Fatalf("ParseFrame failed: %v", err)
	}
	if frame.Type != FrameTypeFixed {
		t.Errorf("Type: want Fixed, got %v", frame.Type)
	}
	if frame.GetFunctionCode() != 0 {
		t.Errorf("FC: want 0, got %d", frame.GetFunctionCode())
	}
}

func TestClient_ReceiveFrame_Variable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().(*net.TCPAddr)

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		connCh <- conn
	}()

	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = serverAddr.Port
	cfg.ConnectTimeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second

	client := NewClient(cfg, logger)
	defer client.Close()

	err = client.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	serverConn := <-connCh
	defer serverConn.Close()

	// 服务端发送可变帧
	asdu := []byte{0x01, 0x01, 0x07, 0xFF, 0xFF, 0x00}
	frame := BuildVariableFrame(0x08, DefaultLinkAddress, asdu) // 上行响应 FC=8
	_, err = serverConn.Write(frame)
	if err != nil {
		t.Fatalf("Server write failed: %v", err)
	}

	// 客户端接收
	frameData, isSingle, err := client.ReceiveFrame()
	if err != nil {
		t.Fatalf("ReceiveFrame failed: %v", err)
	}
	if isSingle {
		t.Error("Expected variable frame, got single ACK")
	}
	t.Logf("Client received %d bytes: %X", len(frameData), frameData)

	parsed, err := ParseFrame(frameData)
	if err != nil {
		t.Fatalf("ParseFrame failed: %v", err)
	}
	if parsed.Type != FrameTypeVariable {
		t.Errorf("Type: want Variable, got %v", parsed.Type)
	}
	if len(parsed.ASDU) != len(asdu) {
		t.Errorf("ASDU len: want %d, got %d", len(asdu), len(parsed.ASDU))
	}
}

func TestClient_ReceiveFrame_SingleACK(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().(*net.TCPAddr)

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		connCh <- conn
	}()

	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = serverAddr.Port
	cfg.ConnectTimeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second

	client := NewClient(cfg, logger)
	defer client.Close()

	err = client.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	serverConn := <-connCh
	defer serverConn.Close()

	// 服务端发送单字节确认 0xE5
	_, err = serverConn.Write([]byte{0xE5})
	if err != nil {
		t.Fatalf("Server write failed: %v", err)
	}

	// 客户端接收
	frameData, isSingle, err := client.ReceiveFrame()
	if err != nil {
		t.Fatalf("ReceiveFrame failed: %v", err)
	}
	if !isSingle {
		t.Error("Expected single ACK")
	}
	if len(frameData) != 1 || frameData[0] != 0xE5 {
		t.Errorf("Expected [0xE5], got %X", frameData)
	}
}

func TestClient_FCB_Management(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	client := NewClient(cfg, logger)
	defer client.Close()

	// 初始 FCB 应为 false
	if client.GetFCB() {
		t.Error("Initial FCB should be false")
	}

	// 翻转 FCB
	fcb1 := client.ToggleFCB()
	if !fcb1 {
		t.Error("First toggle should be true")
	}

	fcb2 := client.ToggleFCB()
	if fcb2 {
		t.Error("Second toggle should be false")
	}

	// 复位 FCB
	client.ResetFCB()
	if client.GetFCB() {
		t.Error("After reset FCB should be false")
	}
}

func TestClient_Reconnect(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cfg := DefaultClientConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 12345 // 不存在的端口
	cfg.ConnectTimeout = 1 * time.Second

	client := NewClient(cfg, logger)
	defer client.Close()

	// Client.Connect() 只尝试一次，重连逻辑在驱动层实现
	err := client.Connect()
	if err == nil {
		t.Error("Connect should fail with invalid port")
	}

	// 验证错误计数增加
	stats := client.GetStats()
	t.Logf("Reconnects: %d, Errors: %d", stats.Reconnects, stats.Errors)
	if stats.Errors == 0 {
		t.Error("Should have recorded error")
	}
}