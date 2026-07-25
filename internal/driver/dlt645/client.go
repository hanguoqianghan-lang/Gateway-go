// internal/driver/dlt645/client.go - DL/T 645 串口客户端
package dlt645

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/goburrow/serial"
	"go.uber.org/zap"
)

// Client DL/T 645 串口客户端
type Client struct {
	config *Config
	logger *zap.Logger

	mu       sync.Mutex
	port     serial.Port
	isOpen   bool

	// 接收缓冲区
	rxBuf []byte
	rxMu  sync.Mutex
}

// NewClient 创建客户端
func NewClient(config *Config, logger *zap.Logger) *Client {
	return &Client{
		config: config,
		logger: logger.With(zap.String("client", "dlt645")),
		rxBuf:  make([]byte, 0, 4096),
	}
}

// Connect 连接串口
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isOpen {
		return nil
	}

	// 配置串口参数
	cfg := &serial.Config{
		Address:  c.config.SerialPort,
		BaudRate: c.config.BaudRate,
		DataBits: c.config.DataBits,
		StopBits: c.config.StopBits,
		Parity:   c.parseParity(c.config.Parity),
		Timeout:  c.config.ResponseTimeout, // 使用响应超时而非字符超时
	}

	c.logger.Info("尝试打开串口",
		zap.String("port", c.config.SerialPort),
		zap.Int("baud_rate", c.config.BaudRate),
		zap.String("parity", c.config.Parity),
	)

	// 打开串口
	port, err := serial.Open(cfg)
	if err != nil {
		return fmt.Errorf("open serial port %s failed: %w", c.config.SerialPort, err)
	}

	c.port = port
	c.isOpen = true

	c.logger.Info("串口打开成功",
		zap.String("port", c.config.SerialPort),
		zap.Int("baud_rate", c.config.BaudRate),
		zap.String("parity", c.config.Parity),
	)

	return nil
}

// parseParity 解析校验位
func (c *Client) parseParity(parity string) string {
	switch parity {
	case "even":
		return "E"
	case "odd":
		return "O"
	default:
		return "N"
	}
}

// Close 关闭连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isOpen {
		return nil
	}

	if c.port != nil {
		c.port.Close()
	}

	c.isOpen = false
	c.rxBuf = c.rxBuf[:0]

	c.logger.Info("serial port closed")
	return nil
}

// IsConnected 检查连接状态
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isOpen
}

// Send 发送数据
func (c *Client) Send(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isOpen {
		return errors.New("serial port not open")
	}

	n, err := c.port.Write(data)
	if err != nil {
		return fmt.Errorf("write failed: %w", err)
	}

	if n != len(data) {
		return fmt.Errorf("incomplete write: %d/%d bytes", n, len(data))
	}

	c.logger.Debug("data sent",
		zap.Int("bytes", n),
		zap.String("hex", fmt.Sprintf("% X", data)),
	)

	return nil
}

// ReceiveFrame 接收完整帧
func (c *Client) ReceiveFrame(timeout time.Duration) (*Frame, error) {
	// DL/T 645 帧以 0x68 开始，以 0x16 结束
	// 先读取到 0x68，然后读取长度，再读取完整帧

	c.mu.Lock()
	isOpen := c.isOpen
	c.mu.Unlock()

	if !isOpen {
		return nil, errors.New("serial port not open")
	}

	startTime := time.Now()

	for time.Since(startTime) < timeout {
		// 检查是否找到完整的帧
		c.rxMu.Lock()
		if len(c.rxBuf) >= 12 { // 最小帧: 68 + 6地址 + 68 + C + L(>=2) + CS + 16
			idx := -1
			for i := 0; i < len(c.rxBuf); i++ {
				if c.rxBuf[i] == FrameStart && i+12 <= len(c.rxBuf) {
					// 找到可能的帧头，检查是否有完整帧
					frameStart := i
					if frameStart+10 < len(c.rxBuf) {
						l := int(c.rxBuf[frameStart+9])
						frameLen := 12 + l
						if frameStart+frameLen <= len(c.rxBuf) {
							idx = frameStart
							break
						}
					}
				}
			}

			if idx >= 0 {
				frameStart := idx
				l := int(c.rxBuf[frameStart+9])
				frameLen := 12 + l

				result := make([]byte, frameLen)
				copy(result, c.rxBuf[frameStart:frameStart+frameLen])
				c.rxBuf = c.rxBuf[frameStart+frameLen:]
				c.rxMu.Unlock()

				c.logger.Debug("frame received",
					zap.Int("bytes", len(result)),
					zap.String("hex", fmt.Sprintf("% X", result)),
				)

				// 解析帧
				frame, err := ParseFrame(result, c.config.ProtocolVersion)
				if err != nil {
					return nil, fmt.Errorf("parse frame failed: %w", err)
				}
				return frame, nil
			}
		}
		c.rxMu.Unlock()

		// 尝试读取数据
		readBuf := make([]byte, 256)
		c.mu.Lock()
		if c.isOpen && c.port != nil {
			n, err := c.port.Read(readBuf)
			c.mu.Unlock()
			if err == nil && n > 0 {
				c.rxMu.Lock()
				c.rxBuf = append(c.rxBuf, readBuf[:n]...)
				c.rxMu.Unlock()
			} else if err != nil && err != io.EOF {
				// 非 EOF 错误可能是超时，继续尝试
			}
		} else {
			c.mu.Unlock()
		}

		// 短暂休眠
		time.Sleep(10 * time.Millisecond)
	}

	return nil, errors.New("receive timeout")
}

// SendFrame 发送帧
func (c *Client) SendFrame(frame []byte) error {
	return c.Send(frame)
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
	c.rxMu.Lock()
	c.rxBuf = c.rxBuf[:0]
	c.rxMu.Unlock()
}