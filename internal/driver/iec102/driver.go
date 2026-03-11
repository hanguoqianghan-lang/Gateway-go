// internal/driver/iec102/driver.go - IEC 60870-5-102 驱动生命周期管理
package iec102

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/model"
	"go.uber.org/zap"
)

// Driver IEC102 驱动
type Driver struct {
	config Config
	logger *zap.Logger
	bus    *broker.Bus

	// IEC102 客户端
	client *Client

	// 点表映射（O(1) 查找）
	pointMap map[string]*PointConfig
	pointMu  sync.RWMutex

	// 运行状态
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	state  uint32 // 0=未启动 1=运行中 2=已停止

	// 连接状态
	isConnected uint32 // 0=断开 1=已连接

	// 处理器
	handler *Handler

	// 统计信息
	atomicStats struct {
		pollCount           uint64
		errCount            uint64
		backgroundScanCount uint64
		periodicReadCount   uint64
		asduReceivedCount   uint64
		connectionDuration  int64
		reconnectCount      uint64
	}
}

// New 创建 IEC102 驱动实例
func New(config Config, logger *zap.Logger) *Driver {
	return &Driver{
		config:   config,
		logger:   logger.With(zap.String("driver", "iec102")),
		pointMap: make(map[string]*PointConfig),
	}
}

// Name 实现 driver.Driver 接口
func (d *Driver) Name() string {
	return d.config.Name
}

// Init 实现 driver.Driver 接口
func (d *Driver) Init(_ context.Context) error {
	// 校验配置
	if err := d.config.Validate(); err != nil {
		return err
	}

	// 构建点表映射（O(1) 查找）
	d.pointMu.Lock()
	defer d.pointMu.Unlock()

	for i, pt := range d.config.Points {
		if pt.Name == "" {
			return fmt.Errorf("iec102: point[%d] missing Name field", i)
		}

		// 构建复合键：CA/IOA
		key := fmt.Sprintf("%d/%d", pt.CA, pt.IOA)
		d.pointMap[key] = &d.config.Points[i]

		d.logger.Debug("point mapping",
			zap.String("name", pt.Name),
			zap.Uint8("ca", pt.CA),
			zap.Uint16("ioa", pt.IOA),
			zap.String("key", key),
		)
	}

	// 创建客户端
	d.client = NewClient(d.config, d.logger)

	// 创建处理器
	d.handler = NewHandler(d, d.logger)

	d.logger.Info("IEC102 driver initialized",
		zap.String("port", d.config.SerialPort),
		zap.Int("baud_rate", d.config.BaudRate),
		zap.String("parity", d.config.Parity),
		zap.Bool("balanced_mode", d.config.BalancedMode),
		zap.Int("points", len(d.config.Points)),
	)

	return nil
}

// Start 实现 driver.Driver 接口
func (d *Driver) Start(ctx context.Context, bus *broker.Bus) error {
	if !atomic.CompareAndSwapUint32(&d.state, 0, 1) {
		return fmt.Errorf("iec102: driver already running")
	}

	d.bus = bus
	d.ctx, d.cancel = context.WithCancel(ctx)

	// 启动后台连接协程（非阻塞）
	d.wg.Add(1)
	go d.connectLoop()

	d.logger.Info("IEC102 driver started (connecting in background)")
	return nil
}

// Stop 实现 driver.Driver 接口
func (d *Driver) Stop(_ context.Context) error {
	if !atomic.CompareAndSwapUint32(&d.state, 1, 2) {
		return nil // 未启动或已停止
	}

	d.logger.Info("stopping IEC102 driver...")
	d.cancel()

	d.wg.Wait()

	// 关闭客户端
	if d.client != nil {
		if err := d.client.Close(); err != nil {
			d.logger.Error("close client failed", zap.Error(err))
		}
	}

	d.logger.Info("IEC102 driver stopped")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 连接管理
// ─────────────────────────────────────────────────────────────────────────────

// connectLoop 连接循环（指数退避重连）
func (d *Driver) connectLoop() {
	defer d.wg.Done()

	retryInterval := d.config.RetryInterval
	maxRetryInterval := 60 * time.Second

	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}

		// 尝试连接
		if err := d.tryConnect(); err != nil {
			d.logger.Warn("connect failed, will retry",
				zap.Error(err),
				zap.Duration("retry_interval", retryInterval),
			)
			atomic.AddUint64(&d.atomicStats.errCount, 1)

			// 等待重试
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(retryInterval):
			}

			// 指数退避
			retryInterval = retryInterval * 2
			if retryInterval > maxRetryInterval {
				retryInterval = maxRetryInterval
			}
			continue
		}

		// 连接成功，重置重试间隔
		retryInterval = d.config.RetryInterval
		atomic.StoreInt64(&d.atomicStats.connectionDuration, time.Now().Unix())
		atomic.AddUint64(&d.atomicStats.reconnectCount, 1)

		// 发送链路复位
		if err := d.client.SendResetLink(); err != nil {
			d.logger.Error("send reset link failed", zap.Error(err))
			d.client.Close()
			continue
		}

		// 启动接收循环
		d.wg.Add(1)
		go d.receiveLoop()

		// 启动背景扫描定时器
		if d.config.BackgroundScanInterval > 0 {
			d.wg.Add(1)
			go d.backgroundScanLoop()
		}

		// 启动周期读取定时器
		if d.config.PeriodicReadInterval > 0 {
			d.wg.Add(1)
			go d.periodicReadLoop()
		}

		// 等待断线
		<-d.ctx.Done()
		return
	}
}

// tryConnect 尝试连接
func (d *Driver) tryConnect() error {
	if err := d.client.Connect(); err != nil {
		return err
	}

	atomic.StoreUint32(&d.isConnected, 1)
	d.logger.Info("serial port connected",
		zap.String("port", d.config.SerialPort),
	)

	return nil
}

// receiveLoop 接收循环
func (d *Driver) receiveLoop() {
	defer d.wg.Done()
	defer func() {
		atomic.StoreUint32(&d.isConnected, 0)
		d.client.Close()
		d.publishDisconnected()
	}()

	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}

		// 接收帧
		frame, err := d.client.ReceiveFrame(d.config.FrameTimeout)
		if err != nil {
			if err.Error() == "EOF" {
				d.logger.Warn("serial port closed")
				return
			}
			d.logger.Debug("receive frame failed", zap.Error(err))
			continue
		}

		// 处理帧
		d.handleFrame(frame)
	}
}

// handleFrame 处理帧
func (d *Driver) handleFrame(frame *Frame) {
	switch frame.Type {
	case FrameTypeFixed:
		d.handleFixedFrame(frame)

	case FrameTypeVariable:
		d.handleVariableFrame(frame)

	case FrameTypeSingle:
		d.logger.Debug("received single byte ACK")
	}
}

// handleFixedFrame 处理固定长度帧
func (d *Driver) handleFixedFrame(frame *Frame) {
	control := frame.Control
	frameType := control & 0x03

	switch frameType {
	case C_U:
		functionCode := int(control & 0x0F)
		d.handleUFrame(functionCode)

	case C_S:
		d.logger.Debug("received S frame")
	}
}

// handleUFrame 处理 U 帧
func (d *Driver) handleUFrame(functionCode int) {
	switch functionCode {
	case FC_RESET_REMOTE_LINK:
		d.logger.Info("received RESET_REMOTE_LINK")

	case FC_START_DATA_TRANSFER:
		d.logger.Info("received START_DATA_TRANSFER")
		// 启动数据传输后，立即发送背景扫描
		d.sendCounterInterrogation()
	}
}

// handleVariableFrame 处理可变长度帧
func (d *Driver) handleVariableFrame(frame *Frame) {
	// 解析 ASDU
	asdu, err := ParseASDU(frame.ASDU)
	if err != nil {
		d.logger.Error("parse ASDU failed", zap.Error(err))
		return
	}

	atomic.AddUint64(&d.atomicStats.asduReceivedCount, 1)

	// 处理 ASDU
	if err := d.handler.HandleASDU(asdu); err != nil {
		d.logger.Error("handle ASDU failed",
			zap.Error(err),
			zap.Uint8("type_id", asdu.TypeID),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 背景扫描（Background Scan）
// ─────────────────────────────────────────────────────────────────────────────

// backgroundScanLoop 背景扫描定时器循环
func (d *Driver) backgroundScanLoop() {
	defer d.wg.Done()

	ticker := time.NewTicker(d.config.BackgroundScanInterval)
	defer ticker.Stop()

	// 立即发送一次背景扫描
	d.sendCounterInterrogation()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// 检查连接状态
			if atomic.LoadUint32(&d.isConnected) == 0 {
				continue
			}
			d.sendCounterInterrogation()
		}
	}
}

// sendCounterInterrogation 发送计数量召唤命令（背景扫描）
func (d *Driver) sendCounterInterrogation() error {
	if d.client == nil || !d.client.IsConnected() {
		return nil
	}

	if err := d.client.SendCounterInterrogation(); err != nil {
		d.logger.Error("send counter interrogation failed", zap.Error(err))
		atomic.AddUint64(&d.atomicStats.errCount, 1)
		return err
	}

	atomic.AddUint64(&d.atomicStats.backgroundScanCount, 1)

	d.logger.Info("counter interrogation sent (background scan)",
		zap.Uint64("count", atomic.LoadUint64(&d.atomicStats.backgroundScanCount)),
	)

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 周期读取（Periodic Read）
// ─────────────────────────────────────────────────────────────────────────────

// periodicReadLoop 周期读取定时器循环
func (d *Driver) periodicReadLoop() {
	defer d.wg.Done()

	ticker := time.NewTicker(d.config.PeriodicReadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// 检查连接状态
			if atomic.LoadUint32(&d.isConnected) == 0 {
				continue
			}
			d.sendPeriodicRead()
		}
	}
}

// sendPeriodicRead 发送周期读取命令
func (d *Driver) sendPeriodicRead() error {
	if d.client == nil || !d.client.IsConnected() {
		return nil
	}

	// 遍历所有点表，发送读命令
	d.pointMu.RLock()
	points := make([]*PointConfig, 0, len(d.pointMap))
	for _, pt := range d.pointMap {
		points = append(points, pt)
	}
	d.pointMu.RUnlock()

	for _, pt := range points {
		if err := d.client.SendReadCommand(pt.CA, pt.IOA); err != nil {
			d.logger.Error("send read command failed",
				zap.Error(err),
				zap.Uint8("ca", pt.CA),
				zap.Uint16("ioa", pt.IOA),
			)
			continue
		}
		atomic.AddUint64(&d.atomicStats.periodicReadCount, 1)
	}

	d.logger.Debug("periodic read sent",
		zap.Int("points", len(points)),
	)

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 断线处理
// ─────────────────────────────────────────────────────────────────────────────

// publishDisconnected 发布断线质量戳
func (d *Driver) publishDisconnected() {
	if d.bus == nil {
		return
	}

	ts := time.Now().UnixNano()
	d.pointMu.RLock()
	defer d.pointMu.RUnlock()

	for _, pt := range d.pointMap {
		p := model.GetPoint()
		p.ID = fmt.Sprintf("%s/iec102/%s", d.config.Name, pt.Name)
		p.Value = nil
		p.Timestamp = ts
		p.Quality = model.QualityNotConnected
		d.bus.Publish(p)
	}

	d.logger.Info("disconnected quality stamps published",
		zap.Int("points", len(d.pointMap)),
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// 统计信息
// ─────────────────────────────────────────────────────────────────────────────

// Stats 返回运行统计信息
func (d *Driver) Stats() map[string]interface{} {
	return map[string]interface{}{
		"poll_count":            atomic.LoadUint64(&d.atomicStats.pollCount),
		"err_count":             atomic.LoadUint64(&d.atomicStats.errCount),
		"background_scan_count": atomic.LoadUint64(&d.atomicStats.backgroundScanCount),
		"periodic_read_count":   atomic.LoadUint64(&d.atomicStats.periodicReadCount),
		"asdu_received_count":   atomic.LoadUint64(&d.atomicStats.asduReceivedCount),
		"reconnect_count":       atomic.LoadUint64(&d.atomicStats.reconnectCount),
		"connected":             atomic.LoadUint32(&d.isConnected) == 1,
		"connection_duration": func() time.Duration {
			if ct := atomic.LoadInt64(&d.atomicStats.connectionDuration); ct > 0 {
				return time.Since(time.Unix(ct, 0))
			}
			return 0
		}(),
	}
}
