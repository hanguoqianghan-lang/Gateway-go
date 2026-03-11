// internal/driver/iec103/driver.go - IEC 60870-5-103 驱动生命周期管理
package iec103

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/driver"
	"github.com/gateway/gateway/internal/model"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// Driver IEC103 驱动
// ─────────────────────────────────────────────────────────────────────────────

// Driver IEC103 驱动实现
type Driver struct {
	config Config
	logger *zap.Logger

	// 客户端和处理器
	client  *Client
	handler *Handler

	// 总线
	bus *broker.Bus

	// 生命周期
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// 状态
	running int32

	// 统计
	stats DriverStats
}

// DriverStats 驱动统计信息
type DriverStats struct {
	ConnectCount    uint64 // 连接次数
	DisconnectCount uint64 // 断开次数
	GICount         uint64 // 总召唤次数
	RxCount         uint64 // 接收帧数
	TxCount         uint64 // 发送帧数
	ErrCount        uint64 // 错误计数
}

// New 创建 IEC103 驱动
func New(config Config, logger *zap.Logger) *Driver {
	return &Driver{
		config: config,
		logger: logger,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Driver 接口实现
// ─────────────────────────────────────────────────────────────────────────────

// Init 初始化驱动
func (d *Driver) Init(ctx context.Context) error {
	// 校验配置
	if err := d.config.Validate(); err != nil {
		return err
	}

	// 创建上下文
	d.ctx, d.cancel = context.WithCancel(ctx)

	// 创建客户端
	d.client = NewClient(d.config, d.logger)

	// 创建处理器
	d.handler = NewHandler(d.config, d.logger)

	// 构建点表索引（基于 FUN/INF）
	d.handler.BuildPointMap(d.config.Points)

	// 设置发布回调
	d.handler.SetPublishFunc(d.publishData)

	d.logger.Info("IEC103 driver initialized",
		zap.String("id", d.config.ID),
		zap.String("name", d.config.Name),
		zap.Int("points", len(d.config.Points)),
		zap.Bool("balanced_mode", d.config.BalancedMode),
	)

	return nil
}

// Start 启动驱动
func (d *Driver) Start(ctx context.Context, bus *broker.Bus) error {
	if atomic.LoadInt32(&d.running) == 1 {
		return nil
	}

	d.bus = bus
	atomic.StoreInt32(&d.running, 1)

	// 启动处理器
	d.handler.Start()

	// 启动连接循环
	d.wg.Add(1)
	go d.connectLoop()

	d.logger.Info("IEC103 driver started",
		zap.String("id", d.config.ID),
	)

	return nil
}

// Stop 停止驱动
func (d *Driver) Stop(ctx context.Context) error {
	if atomic.LoadInt32(&d.running) == 0 {
		return nil
	}

	atomic.StoreInt32(&d.running, 0)
	d.cancel()
	d.wg.Wait()

	// 停止处理器
	d.handler.Stop()

	// 关闭客户端
	if d.client != nil {
		d.client.Close()
	}

	d.logger.Info("IEC103 driver stopped",
		zap.String("id", d.config.ID),
	)

	return nil
}

// Name 返回驱动名称
func (d *Driver) Name() string {
	return d.config.Name
}

// ─────────────────────────────────────────────────────────────────────────────
// 连接管理
// ─────────────────────────────────────────────────────────────────────────────

// connectLoop 连接循环
func (d *Driver) connectLoop() {
	defer d.wg.Done()

	retryCount := 0
	retryInterval := d.config.RetryInterval

	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}

		// 连接
		err := d.client.Connect()
		if err != nil {
			d.logger.Error("connect failed",
				zap.String("id", d.config.ID),
				zap.Error(err),
			)

			retryCount++
			if retryCount > d.config.MaxRetry {
				retryCount = d.config.MaxRetry
			}

			// 指数退避
			waitTime := retryInterval * time.Duration(retryCount)
			if waitTime > 30*time.Second {
				waitTime = 30 * time.Second
			}

			select {
			case <-d.ctx.Done():
				return
			case <-time.After(waitTime):
				continue
			}
		}

		// 连接成功
		atomic.AddUint64(&d.stats.ConnectCount, 1)
		retryCount = 0
		d.logger.Info("connected",
			zap.String("id", d.config.ID),
			zap.String("port", d.config.SerialPort),
		)

		// 发送链路复位
		if err := d.client.SendResetLink(); err != nil {
			d.logger.Error("send reset link failed", zap.Error(err))
			d.client.Close()
			continue
		}

		// 启动接收循环
		d.wg.Add(1)
		reconnectCh := make(chan struct{})
		go d.receiveLoop(reconnectCh)

		// 启动总召唤循环（非平衡模式）
		if !d.config.BalancedMode {
			d.wg.Add(1)
			go d.giLoop(reconnectCh)
		}

		// 等待重连信号
		<-reconnectCh
		d.client.Close()
		atomic.AddUint64(&d.stats.DisconnectCount, 1)
	}
}

// receiveLoop 接收循环
func (d *Driver) receiveLoop(reconnectCh chan struct{}) {
	defer d.wg.Done()

	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}

		// 接收帧
		frame, err := d.client.ReceiveFrame(d.config.FrameTimeout)
		if err != nil {
			if d.ctx.Err() != nil {
				return
			}

			d.logger.Error("receive frame failed", zap.Error(err))
			atomic.AddUint64(&d.stats.ErrCount, 1)

			// 触发重连
			select {
			case reconnectCh <- struct{}{}:
			default:
			}
			return
		}

		atomic.AddUint64(&d.stats.RxCount, 1)

		// 处理帧
		if err := d.handleFrame(frame); err != nil {
			d.logger.Error("handle frame failed", zap.Error(err))
		}
	}
}

// handleFrame 处理帧
func (d *Driver) handleFrame(frame *Frame) error {
	switch frame.Type {
	case FrameTypeFixed:
		return d.handleFixedFrame(frame)
	case FrameTypeVariable:
		return d.handleVariableFrame(frame)
	case FrameTypeSingle:
		// 单字节确认，忽略
		return nil
	default:
		return nil
	}
}

// handleFixedFrame 处理固定长度帧
func (d *Driver) handleFixedFrame(frame *Frame) error {
	// 解析控制域
	frameType := (frame.Control & 0x03)
	functionCode := int(frame.Control & 0x0F)

	switch frameType {
	case C_U:
		// U 帧
		switch functionCode {
		case FC_RESET_REMOTE_LINK:
			d.logger.Debug("reset link confirmed")
		case FC_SEND_CONFIRM:
			d.logger.Debug("send confirmed")
		}
	case C_S:
		// S 帧（确认帧）
		d.logger.Debug("S frame received")
	}

	return nil
}

// handleVariableFrame 处理可变长度帧
func (d *Driver) handleVariableFrame(frame *Frame) error {
	// 解析 ASDU
	asdu, err := ParseASDU(frame.ASDU)
	if err != nil {
		return err
	}

	// 处理 ASDU
	return d.handler.HandleASDU(asdu)
}

// giLoop 总召唤循环
func (d *Driver) giLoop(reconnectCh chan struct{}) {
	defer d.wg.Done()

	// 立即发送一次总召唤
	d.sendGeneralInterrogation()

	ticker := time.NewTicker(d.config.GIInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-reconnectCh:
			return
		case <-ticker.C:
			d.sendGeneralInterrogation()
		}
	}
}

// sendGeneralInterrogation 发送总召唤
func (d *Driver) sendGeneralInterrogation() {
	if err := d.client.SendGeneralInterrogation(); err != nil {
		d.logger.Error("send general interrogation failed", zap.Error(err))
		return
	}

	atomic.AddUint64(&d.stats.GICount, 1)
	d.logger.Debug("general interrogation sent")
}

// ─────────────────────────────────────────────────────────────────────────────
// 数据发布
// ─────────────────────────────────────────────────────────────────────────────

// publishData 发布数据到总线
func (d *Driver) publishData(data *model.PointData) {
	if d.bus == nil {
		return
	}

	// 设置数据源（使用 ID 字段）
	data.ID = d.config.Name + "/" + data.ID

	// 发布到总线
	d.bus.Publish(data)

	// 释放对象
	model.PutPoint(data)
}

// ─────────────────────────────────────────────────────────────────────────────
// 统计信息
// ─────────────────────────────────────────────────────────────────────────────

// GetStats 获取统计信息
func (d *Driver) GetStats() map[string]interface{} {
	clientStats := d.client.GetStats()
	handlerStats := d.handler.GetStats()

	return map[string]interface{}{
		"driver_id":        d.config.ID,
		"driver_name":      d.config.Name,
		"running":          atomic.LoadInt32(&d.running) == 1,
		"connect_count":    atomic.LoadUint64(&d.stats.ConnectCount),
		"disconnect_count": atomic.AddUint64(&d.stats.DisconnectCount, 0),
		"gi_count":         atomic.LoadUint64(&d.stats.GICount),
		"rx_count":         atomic.LoadUint64(&d.stats.RxCount),
		"tx_count":         atomic.LoadUint64(&d.stats.TxCount),
		"err_count":        atomic.LoadUint64(&d.stats.ErrCount),
		"client_tx":        clientStats.TxCount,
		"client_rx":        clientStats.RxCount,
		"client_err":       clientStats.ErrCount,
		"soe_total":        handlerStats.TotalEvents,
		"soe_events":       handlerStats.SOEEvents,
		"soe_dropped":      handlerStats.DroppedEvents,
		"published_count":  handlerStats.PublishedCount,
		"point_count":      d.handler.GetPointCount(),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 驱动注册
// ─────────────────────────────────────────────────────────────────────────────

// 确保实现 Driver 接口
var _ driver.Driver = (*Driver)(nil)
