// internal/driver/guowang102/driver_test.go - 国网102驱动集成测试
package guowang102

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/model"
)

const maxChunkSize = 200

// ─────────────────────────────────────────────────────────────────────────────
// 模拟器服务端 - 使用异步文件发送
// ─────────────────────────────────────────────────────────────────────────────

type simServer struct {
	listener net.Listener
	conn     net.Conn
	done     chan struct{}
	wg       sync.WaitGroup
	files    map[string][]byte
	state    string
	sendFCB  bool
	fileIdx  int
	chunkIdx int
	fileList []string
	fileChan chan struct{} // 触发文件发送
	mu       sync.Mutex
}

func newSimServer(files map[string][]byte) *simServer {
	return &simServer{
		done:     make(chan struct{}),
		files:    files,
		state:    "WAIT_RESET",
		fileList: make([]string, 0, len(files)),
		fileChan: make(chan struct{}, 10),
	}
}

func (s *simServer) Start(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	s.listener = listener

	for name := range s.files {
		s.fileList = append(s.fileList, name)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		s.conn = conn
		s.handleConn(t)
	}()
}

func (s *simServer) Addr() net.Addr {
	return s.listener.Addr()
}

func (s *simServer) handleConn(t *testing.T) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-s.done:
			return
		default:
			s.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := s.conn.Read(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return
			}
			s.processFrame(t, buf[:n])
		}
	}
}

func (s *simServer) processFrame(t *testing.T, frame []byte) {
	if len(frame) == 0 {
		return
	}

	t.Logf("Simulator RX (%d bytes): %X", len(frame), frame)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 解析 FCB 位（如果存在）
	var fcb bool
	if len(frame) >= 2 {
		fcb = (frame[1] & 0x20) != 0
	}
	prm := (frame[1] & 0x40) != 0
	fc := frame[1] & 0x0F

	t.Logf("Simulator: frame[1]=0x%02X, PRM=%v, FCB=%v, FC=%d", frame[1], prm, fcb, fc)

	switch frame[0] {
	case 0x10: // 固定帧
		if len(frame) < 6 {
			return
		}

		switch fc {
		case 0x00: // 复位链路
			if s.state == "WAIT_RESET" {
				s.state = "RESET_SENT"
				s.sendFCB = false
				// 回固定帧 FC=0, FCB=0 (PRM=0, FC=0) -> 0x00
				ack := []byte{0x10, 0x00, 0xFF, 0xFF, 0x00, 0x16}
				ack[4] = calcCS(ack[1:4])
				t.Logf("Simulator TX (6 bytes): %X", ack)
				s.sendRaw(ack)
			}
		case 0x04: // 启动数据传输
			if s.state == "RESET_SENT" {
				s.state = "OPERATIONAL"
				// 回固定帧 FC=0, FCB=1 (匹配主站发送的 FC=4, FCB=0 -> 翻转后 pendingFCB=true)
				// 控制域: PRM=0, FCB=1, FC=0 -> 0x20
				ack := []byte{0x10, 0x20, 0xFF, 0xFF, 0x00, 0x16}
				ack[4] = calcCS(ack[1:4])
				t.Logf("Simulator TX (6 bytes): %X", ack)
				s.sendRaw(ack)
			}
		case 0x09: // 链路状态请求
			if s.state == "RESET_SENT" {
				s.state = "OPERATIONAL"
				ack := []byte{0x10, 0x20, 0xFF, 0xFF, 0x00, 0x16}
				ack[4] = calcCS(ack[1:4])
				t.Logf("Simulator TX (6 bytes): %X", ack)
				s.sendRaw(ack)
			} else if s.state == "OPERATIONAL" {
				ctrl := byte(0x0B) // FC=11 上行响应
				resp := []byte{0x10, ctrl, 0xFF, 0xFF, 0x00, 0x16}
				resp[4] = calcCS(resp[1:4])
				t.Logf("Simulator TX (6 bytes): %X", resp)
				s.sendRaw(resp)
			}
		case 0x0B: // 召唤2级数据 (FC=11) - 子站应返回 ACD=1 表示有 1级数据
			if s.state == "OPERATIONAL" && s.fileIdx < len(s.fileList) {
				ctrl := byte(0x08 | 0x40) // ACD=1, FC=8 (有数据回答)
				ack := []byte{0x10, ctrl, 0xFF, 0xFF, 0x00, 0x16}
				ack[4] = calcCS(ack[1:4])
				t.Logf("Simulator TX (6 bytes): %X", ack)
				s.sendRaw(ack)
				// 触发文件发送
				select {
				case s.fileChan <- struct{}{}:
				default:
				}
			} else if s.state == "OPERATIONAL" {
				ctrl := byte(0x09) // FC=9 无所召唤数据
				resp := []byte{0x10, ctrl, 0xFF, 0xFF, 0x00, 0x16}
				resp[4] = calcCS(resp[1:4])
				s.sendRaw(resp)
			}
		case 0x0A: // 召唤1级数据 - 触发文件发送
			if s.state == "OPERATIONAL" && s.fileIdx < len(s.fileList) {
				ctrl := byte(0x08 | 0x40) // ACD=1, FC=8
				ack := []byte{0x10, ctrl, 0xFF, 0xFF, 0x00, 0x16}
				ack[4] = calcCS(ack[1:4])
				t.Logf("Simulator RX FC=10, responding with ACD=1")
				t.Logf("Simulator TX (6 bytes): %X", ack)
				s.sendRaw(ack)
				// 触发文件发送
				select {
				case s.fileChan <- struct{}{}:
				default:
				}
			}
		case 0x03: // 发送确认 (主站确认分片)
			if fcb == s.sendFCB {
				s.sendFCB = !s.sendFCB
			}
		}

	case 0x68: // 可变帧
		if len(frame) < 13 {
			return
		}
		typeID := frame[7]
		cot := frame[9]

		if cot == 0x0A { // COT=0x0A 文件接收完成
			asdu := []byte{typeID, 0x01, 0x0B, 0x00, 0xFF, 0xFF, 0x00}
			ctrl := byte(0x43) | (boolToByte(s.sendFCB) << 5) // FC=3, FCV=1, PRM=1
			resp := buildVariableFrame(ctrl, 0xFFFF, asdu)
			s.sendRaw(resp)
			s.sendFCB = !s.sendFCB
			s.chunkIdx = 0
			s.fileIdx++
		}

	case 0xE5: // 单字节确认
		if fcb == s.sendFCB {
			s.sendFCB = !s.sendFCB
		}
	}
}

// 文件发送 goroutine - 独立运行，不阻塞帧处理
func (s *simServer) fileSender(t *testing.T) {
	for {
		select {
		case <-s.done:
			return
		case <-s.fileChan:
			// 发送当前文件
			if s.fileIdx >= len(s.fileList) {
				continue
			}

			content := s.files[s.fileList[s.fileIdx]]
			s.mu.Lock()
			s.chunkIdx = 0
			s.mu.Unlock()

			for {
				s.mu.Lock()
				if s.chunkIdx*maxChunkSize >= len(content) {
					s.mu.Unlock()
					break
				}

				chunk := content[s.chunkIdx*maxChunkSize : testMin((s.chunkIdx+1)*maxChunkSize, len(content))]
				isLast := (s.chunkIdx+1)*maxChunkSize >= len(content)
				cot := byte(0x07)
				if !isLast {
					cot = 0x08
				}

				asdu := buildFileTransferASDUTest(139, cot, s.fileList[s.fileIdx], chunk)
				s.mu.Lock()
				ctrl := byte(0x43) | (boolToByte(s.sendFCB) << 5)
				s.mu.Unlock()
				frame := buildVariableFrame(ctrl, 0xFFFF, asdu)

				s.sendRaw(frame)
				s.mu.Lock()
				s.chunkIdx++
				s.mu.Unlock()

				if isLast {
					break
				}

				time.Sleep(30 * time.Millisecond)
			}
		}
	}
}

func (s *simServer) sendRaw(data []byte) {
	if s.conn != nil {
		s.conn.Write(data)
	}
}

func (s *simServer) Close() {
	close(s.done)
	if s.conn != nil {
		s.conn.Close()
	}
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
}

// ─────────────────────────────────────────────────────────────────────────────
// 测试用例
// ─────────────────────────────────────────────────────────────────────────────

// TestEndToEnd_FileTransfer 端到端文件传输测试
func TestEndToEnd_FileTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// 1. 创建测试文件
	testFiles := map[string][]byte{
		"WF_TEST_001.wpd": []byte("ENERGY_PREDICTION_TEST_DATA\nLINE2\nLINE3\nLINE4\nLINE5\n"),
	}

	// 2. 启动模拟器
	sim := newSimServer(testFiles)
	sim.Start(t)
	defer sim.Close()

	// 启动文件发送 goroutine
	go sim.fileSender(t)

	// 3. 等待服务器就绪
	time.Sleep(100 * time.Millisecond)

	// 4. 创建驱动配置
	logger, _ := zap.NewDevelopment()
	cfg := &DriverConfig{
		Host:                   "127.0.0.1",
		Port:                   sim.Addr().(*net.TCPAddr).Port,
		LinkAddress:            DefaultLinkAddress,
		CommonAddress:          DefaultCommonAddress,
		ConnectTimeout:         "5s",
		ReadTimeout:            "10s",
		WriteTimeout:           "5s",
		KeepAliveInterval:      "5s",
		LinkStatusInterval:     "30s",
		BackgroundScanInterval: "100ms", // 快速轮询用于测试
		PeriodicReadInterval:   "100ms",
		MaxRetry:               3,
		RetryInterval:          "1s",
		FrameTimeout:           "5s",
		StorageDir:             t.TempDir(),
		MaxFileSize:            20480,
		FileTimeout:            "10s",
		LogLevel:               "debug",
	}

	// 5. 创建驱动实例
	d := &Driver{
		cfg:    cfg,
		logger: logger.Named("guowang102_test"),
	}

	ctx := context.Background()
	if err := d.Init(ctx); err != nil {
		t.Fatalf("Driver Init failed: %v", err)
	}

	// 6. 创建 EventBus
	bus := broker.NewBus(1000)

	// 7. 启动驱动
	if err := d.Start(ctx, bus); err != nil {
		t.Fatalf("Driver Start failed: %v", err)
	}

	// 8. 订阅事件
	sub := bus.Subscribe()

	// 9. 等待文件传输完成 (最多 15 秒)
	fileReceived := false
	timeout := time.After(15 * time.Second)

	for !fileReceived {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for file transfer")
		case p := <-sub:
			if p != nil && p.Quality == model.QualityGood {
				t.Logf("Received point: %s, value type: %T, len: %d", p.ID, p.Value, len(p.Value.([]byte)))
				fileReceived = true
			}
			if p != nil {
				model.PutPoint(p)
			}
		}
	}

	// 10. 验证文件落盘
	stats := d.GetStats()
	t.Logf("Driver stats: %+v", stats)

	if stats["files_completed"].(uint64) == 0 {
		t.Error("Expected at least 1 file completed")
	}

	// 11. 停止驱动
	d.Stop(ctx)
}

// TestEndToEnd_MultipleFiles 多文件传输测试
func TestEndToEnd_MultipleFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	testFiles := map[string][]byte{
		"WF_ENERGY_001.wpd":  []byte("ENERGY_1\nDATA\n"),
		"WF_SHORT_001.wpd":   []byte("SHORT_1\nDATA\n"),
		"WF_ULTRA_001.wpd":   []byte("ULTRA_1\nDATA\n"),
	}

	sim := newSimServer(testFiles)
	sim.Start(t)
	defer sim.Close()

	go sim.fileSender(t)

	time.Sleep(100 * time.Millisecond)

	logger, _ := zap.NewDevelopment()
	cfg := &DriverConfig{
		Host:                   "127.0.0.1",
		Port:                   sim.Addr().(*net.TCPAddr).Port,
		LinkAddress:            DefaultLinkAddress,
		CommonAddress:          DefaultCommonAddress,
		ConnectTimeout:         "5s",
		ReadTimeout:            "10s",
		WriteTimeout:           "5s",
		KeepAliveInterval:      "5s",
		LinkStatusInterval:     "30s",
		BackgroundScanInterval: "100ms",
		PeriodicReadInterval:   "100ms",
		MaxRetry:               3,
		RetryInterval:          "1s",
		FrameTimeout:           "5s",
		StorageDir:             t.TempDir(),
		MaxFileSize:            20480,
		FileTimeout:            "10s",
		LogLevel:               "debug",
	}

	d := &Driver{
		cfg:    cfg,
		logger: logger.Named("guowang102_test"),
	}

	ctx := context.Background()
	if err := d.Init(ctx); err != nil {
		t.Fatalf("Driver Init failed: %v", err)
	}

	bus := broker.NewBus(1000)
	if err := d.Start(ctx, bus); err != nil {
		t.Fatalf("Driver Start failed: %v", err)
	}

	sub := bus.Subscribe()

	// 等待所有文件传输完成
	fileCount := 0
	timeout := time.After(20 * time.Second)

	for fileCount < len(testFiles) {
		select {
		case <-timeout:
			t.Fatalf("Timeout waiting for multi-file transfer, got %d/%d", fileCount, len(testFiles))
		case p := <-sub:
			if p != nil && p.Quality == model.QualityGood {
				t.Logf("Received file point: %s, size: %d bytes", p.ID, len(p.Value.([]byte)))
				fileCount++
			}
			if p != nil {
				model.PutPoint(p)
			}
		}
	}

	stats := d.GetStats()
	t.Logf("Driver stats: %+v", stats)

	if stats["files_completed"].(uint64) != uint64(len(testFiles)) {
		t.Errorf("Expected %d files completed, got %d", len(testFiles), stats["files_completed"])
	}

	d.Stop(ctx)
}

// ─────────────────────────────────────────────────────────────────────────────
// 测试辅助函数
// ─────────────────────────────────────────────────────────────────────────────

func boolToByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func calcCS(data []byte) byte {
	sum := 0
	for _, b := range data {
		sum += int(b)
	}
	return byte(sum & 0xFF)
}

func buildVariableFrame(ctrl byte, addr uint16, asdu []byte) []byte {
	l := len(asdu) + 3
	if l > 255 {
		l = 255
		asdu = asdu[:252]
	}

	addrLow := byte(addr & 0xFF)
	addrHigh := byte((addr >> 8) & 0xFF)

	header := []byte{0x68, byte(l), byte(l), 0x68, ctrl, addrLow, addrHigh}
	cs := calcCS(append(header[4:], asdu...))
	frame := append(header, asdu...)
	frame = append(frame, cs, 0x16)
	return frame
}

func buildFileTransferASDUTest(typeID int, cot byte, fileName string, content []byte) []byte {
	nameBytes := []byte(fileName)
	namePadded := make([]byte, 32)
	copy(namePadded, nameBytes)

	asdu := []byte{
		byte(typeID), // TypeID
		0x01,         // VSQ
		cot,          // COT
		0x00,         // OA
		0xFF, 0xFF,   // CA (0xFFFF)
		0x00,         // RecordAddr
	}
	asdu = append(asdu, namePadded...)
	asdu = append(asdu, content...)
	return asdu
}

func testMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}