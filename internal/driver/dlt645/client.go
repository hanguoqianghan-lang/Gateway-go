// internal/driver/dlt645/client.go - DL/T 645 客户端（支持串口和 TCP）
package dlt645

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/goburrow/serial"
	"go.uber.org/zap"
)

// Transport 接口定义传输层通用操作
type Transport interface {
	Connect() error
	Close() error
	IsConnected() bool
	Send(data []byte) error
	ReceiveFrame(timeout time.Duration) (*Frame, error)
	Flush()
}

// SerialTransport 串口传输实现
type SerialTransport struct {
	config *Config
	logger *zap.Logger

	mu     sync.Mutex
	port   serial.Port
	isOpen bool

	rxBuf []byte
	rxMu  sync.Mutex
}

func NewSerialTransport(config *Config, logger *zap.Logger) *SerialTransport {
	return &SerialTransport{
		config: config,
		logger: logger.With(zap.String("transport", "serial")),
		rxBuf:  make([]byte, 0, 4096),
	}
}

func (t *SerialTransport) Connect() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isOpen {
		return nil
	}

	cfg := &serial.Config{
		Address:  t.config.SerialPort,
		BaudRate: t.config.BaudRate,
		DataBits: t.config.DataBits,
		StopBits: t.config.StopBits,
		Parity:   t.parseParity(t.config.Parity),
		Timeout:  t.config.ResponseTimeout,
	}

	t.logger.Info("尝试打开串口",
		zap.String("port", t.config.SerialPort),
		zap.Int("baud_rate", t.config.BaudRate),
		zap.String("parity", t.config.Parity),
	)

	port, err := serial.Open(cfg)
	if err != nil {
		return fmt.Errorf("open serial port %s failed: %w", t.config.SerialPort, err)
	}

	t.port = port
	t.isOpen = true

	t.logger.Info("串口打开成功",
		zap.String("port", t.config.SerialPort),
		zap.Int("baud_rate", t.config.BaudRate),
		zap.String("parity", t.config.Parity),
	)

	return nil
}

func (t *SerialTransport) parseParity(parity string) string {
	switch parity {
	case "even":
		return "E"
	case "odd":
		return "O"
	default:
		return "N"
	}
}

func (t *SerialTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.isOpen {
		return nil
	}

	if t.port != nil {
		t.port.Close()
	}

	t.isOpen = false
	t.rxBuf = t.rxBuf[:0]

	t.logger.Info("serial port closed")
	return nil
}

func (t *SerialTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.isOpen
}

func (t *SerialTransport) Send(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.isOpen {
		return errors.New("serial port not open")
	}

	n, err := t.port.Write(data)
	if err != nil {
		return fmt.Errorf("write failed: %w", err)
	}

	if n != len(data) {
		return fmt.Errorf("incomplete write: %d/%d bytes", n, len(data))
	}

	t.logger.Debug("data sent",
		zap.Int("bytes", n),
		zap.String("hex", fmt.Sprintf("% X", data)),
	)

	return nil
}

func (t *SerialTransport) ReceiveFrame(timeout time.Duration) (*Frame, error) {
	t.mu.Lock()
	isOpen := t.isOpen
	t.mu.Unlock()

	if !isOpen {
		return nil, errors.New("serial port not open")
	}

	startTime := time.Now()

	for time.Since(startTime) < timeout {
		t.rxMu.Lock()
		if len(t.rxBuf) >= 12 {
			idx := -1
			for i := 0; i < len(t.rxBuf); i++ {
				if t.rxBuf[i] == FrameStart && i+12 <= len(t.rxBuf) {
					frameStart := i
					if frameStart+10 < len(t.rxBuf) {
						l := int(t.rxBuf[frameStart+9])
						frameLen := 12 + l
						if frameStart+frameLen <= len(t.rxBuf) {
							idx = frameStart
							break
						}
					}
				}
			}

			if idx >= 0 {
				frameStart := idx
				l := int(t.rxBuf[frameStart+9])
				frameLen := 12 + l

				result := make([]byte, frameLen)
				copy(result, t.rxBuf[frameStart:frameStart+frameLen])
				t.rxBuf = t.rxBuf[frameStart+frameLen:]
				t.rxMu.Unlock()

				t.logger.Debug("frame received",
					zap.Int("bytes", len(result)),
					zap.String("hex", fmt.Sprintf("% X", result)),
				)

				frame, err := ParseFrame(result, t.config.ProtocolVersion)
				if err != nil {
					return nil, fmt.Errorf("parse frame failed: %w", err)
				}
				return frame, nil
			}
		}
		t.rxMu.Unlock()

		readBuf := make([]byte, 256)
		t.mu.Lock()
		if t.isOpen && t.port != nil {
			n, err := t.port.Read(readBuf)
			t.mu.Unlock()
			if err == nil && n > 0 {
				t.rxMu.Lock()
				t.rxBuf = append(t.rxBuf, readBuf[:n]...)
				t.rxMu.Unlock()
			} else if err != nil && err != io.EOF {
				// 非 EOF 错误可能是超时，继续尝试
			}
		} else {
			t.mu.Unlock()
		}

		time.Sleep(10 * time.Millisecond)
	}

	return nil, errors.New("receive timeout")
}

func (t *SerialTransport) Flush() {
	t.rxMu.Lock()
	t.rxBuf = t.rxBuf[:0]
	t.rxMu.Unlock()
}

// TCPTransport TCP 传输实现
type TCPTransport struct {
	config *Config
	logger *zap.Logger

	mu     sync.Mutex
	conn   net.Conn
	isOpen bool

	rxBuf []byte
	rxMu  sync.Mutex
}

func NewTCPTransport(config *Config, logger *zap.Logger) *TCPTransport {
	return &TCPTransport{
		config: config,
		logger: logger.With(zap.String("transport", "tcp")),
		rxBuf:  make([]byte, 0, 4096),
	}
}

func (t *TCPTransport) Connect() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isOpen && t.conn != nil {
		return nil
	}

	t.logger.Info("尝试连接 TCP",
		zap.String("addr", t.config.TCPAddr),
	)

	ctx, cancel := context.WithTimeout(context.Background(), t.config.ResponseTimeout)
	defer cancel()

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", t.config.TCPAddr)
	if err != nil {
		return fmt.Errorf("connect to %s failed: %w", t.config.TCPAddr, err)
	}

	// 设置读取截止时间
	conn.SetReadDeadline(time.Now().Add(t.config.ResponseTimeout))

	t.conn = conn
	t.isOpen = true

	t.logger.Info("TCP 连接成功",
		zap.String("addr", t.config.TCPAddr),
		zap.String("local", conn.LocalAddr().String()),
		zap.String("remote", conn.RemoteAddr().String()),
	)

	return nil
}

func (t *TCPTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.isOpen {
		return nil
	}

	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}

	t.isOpen = false
	t.rxBuf = t.rxBuf[:0]

	t.logger.Info("TCP 连接关闭")
	return nil
}

func (t *TCPTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.isOpen && t.conn != nil
}

func (t *TCPTransport) Send(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.isOpen || t.conn == nil {
		return errors.New("TCP connection not open")
	}

	// 设置写入截止时间
	t.conn.SetWriteDeadline(time.Now().Add(t.config.ResponseTimeout))

	n, err := t.conn.Write(data)
	if err != nil {
		return fmt.Errorf("write failed: %w", err)
	}

	if n != len(data) {
		return fmt.Errorf("incomplete write: %d/%d bytes", n, len(data))
	}

	t.logger.Debug("data sent",
		zap.Int("bytes", n),
		zap.String("hex", fmt.Sprintf("% X", data)),
	)

	return nil
}

func (t *TCPTransport) ReceiveFrame(timeout time.Duration) (*Frame, error) {
	t.mu.Lock()
	isOpen := t.isOpen
	conn := t.conn
	t.mu.Unlock()

	if !isOpen || conn == nil {
		return nil, errors.New("TCP connection not open")
	}

	startTime := time.Now()

	for time.Since(startTime) < timeout {
		t.rxMu.Lock()
		if len(t.rxBuf) >= 12 {
			idx := -1
			for i := 0; i < len(t.rxBuf); i++ {
				if t.rxBuf[i] == FrameStart && i+12 <= len(t.rxBuf) {
					frameStart := i
					if frameStart+10 < len(t.rxBuf) {
						l := int(t.rxBuf[frameStart+9])
						frameLen := 12 + l
						if frameStart+frameLen <= len(t.rxBuf) {
							idx = frameStart
							break
						}
					}
				}
			}

			if idx >= 0 {
				frameStart := idx
				l := int(t.rxBuf[frameStart+9])
				frameLen := 12 + l

				result := make([]byte, frameLen)
				copy(result, t.rxBuf[frameStart:frameStart+frameLen])
				t.rxBuf = t.rxBuf[frameStart+frameLen:]
				t.rxMu.Unlock()

				t.logger.Debug("frame received",
					zap.Int("bytes", len(result)),
					zap.String("hex", fmt.Sprintf("% X", result)),
				)

				frame, err := ParseFrame(result, t.config.ProtocolVersion)
				if err != nil {
					return nil, fmt.Errorf("parse frame failed: %w", err)
				}
				return frame, nil
			}
		}
		t.rxMu.Unlock()

		// 设置读取截止时间
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		readBuf := make([]byte, 256)
		n, err := conn.Read(readBuf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// 超时继续尝试
				continue
			}
			// 连接断开
			t.Close()
			return nil, fmt.Errorf("read failed: %w", err)
		}

		if n > 0 {
			t.rxMu.Lock()
			t.rxBuf = append(t.rxBuf, readBuf[:n]...)
			t.rxMu.Unlock()
		}
	}

	return nil, errors.New("receive timeout")
}

func (t *TCPTransport) Flush() {
	t.rxMu.Lock()
	t.rxBuf = t.rxBuf[:0]
	t.rxMu.Unlock()
}

// Client DL/T 645 统一客户端（支持串口和 TCP）
type Client struct {
	config    *Config
	logger    *zap.Logger
	transport Transport
}

// NewClient 创建客户端（根据配置自动选择传输方式）
func NewClient(config *Config, logger *zap.Logger) *Client {
	var transport Transport
	switch config.Transport {
	case TransportTCP:
		transport = NewTCPTransport(config, logger)
	default:
		transport = NewSerialTransport(config, logger)
	}

	return &Client{
		config:    config,
		logger:    logger.With(zap.String("client", "dlt645")),
		transport: transport,
	}
}

// Connect 连接
func (c *Client) Connect() error {
	return c.transport.Connect()
}

// Close 关闭连接
func (c *Client) Close() error {
	return c.transport.Close()
}

// IsConnected 检查连接状态
func (c *Client) IsConnected() bool {
	return c.transport.IsConnected()
}

// Send 发送数据
func (c *Client) Send(data []byte) error {
	return c.transport.Send(data)
}

// ReceiveFrame 接收完整帧
func (c *Client) ReceiveFrame(timeout time.Duration) (*Frame, error) {
	return c.transport.ReceiveFrame(timeout)
}

// SendFrame 发送帧
func (c *Client) SendFrame(frame []byte) error {
	return c.transport.Send(frame)
}

// SendRequest 发送读请求并等待响应
func (c *Client) SendRequest(address [6]byte, dataID []byte) (*Frame, error) {
	// 构建请求帧
	req, err := BuildRequest(address, dataID, c.config.ProtocolVersion, c.config.UseLeadingByte)
	if err != nil {
		return nil, fmt.Errorf("build request failed: %w", err)
	}

	c.logger.Debug("发送请求帧",
		zap.String("address", StringAddress(address)),
		zap.String("data_id", fmt.Sprintf("% X", dataID)),
		zap.String("frame_hex", fmt.Sprintf("% X", req)),
	)

	// 发送
	if err := c.Send(req); err != nil {
		return nil, fmt.Errorf("send request failed: %w", err)
	}

	// 接收响应
	resp, err := c.ReceiveFrame(c.config.ResponseTimeout)
	if err != nil {
		return nil, fmt.Errorf("receive response failed: %w", err)
	}

	// 检查地址是否匹配（支持广播地址）
	if !c.matchAddress(resp.Address, address) {
		return nil, fmt.Errorf("address mismatch: expected % X, got % X", address, resp.Address)
	}

	return resp, nil
}

// matchAddress 检查地址是否匹配
// 支持广播地址 AAAAAAAAAAAA
// 帧中地址是低字节在前，需要反转后与点表地址比较
func (c *Client) matchAddress(received, expected [6]byte) bool {
	// 广播地址匹配（低字节在前的 AA AA AA AA AA AA）
	broadcast := [6]byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	if received == broadcast {
		return true
	}

	// received 是低字节在前，反转后应该等于 expected（高字节在前）
	// 例如: received = [00,18,01,05,20,01] -> 反转为 [01,20,05,01,18,00] = expected
	reversed := [6]byte{
		received[5], received[4], received[3],
		received[2], received[1], received[0],
	}

	return reversed == expected
}

// Flush 刷新接收缓冲区
func (c *Client) Flush() {
	c.transport.Flush()
}