// internal/driver/dlt645/driver.go - DL/T 645 驱动生命周期管理
package dlt645

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

// Driver DL/T 645 驱动
type Driver struct {
	config Config
	logger *zap.Logger
	bus    *broker.Bus

	// 客户端
	client *Client

	// 点表映射: key = 地址_数据标识
	pointMap map[string]*PointConfig
	pointMu  sync.RWMutex

	// 运行状态
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	state  uint32 // 0=未启动 1=运行中 2=已停止

	// 连接状态
	isConnected uint32

	// 处理器
	handler *Handler

	// 统计信息
	atomicStats struct {
		pollCount         uint64
		errCount          uint64
		frameReceivedCount uint64
		connectionDuration int64
		reconnectCount    uint64
	}
}

// New 创建驱动实例
func New(config *Config, logger *zap.Logger) *Driver {
	return &Driver{
		config:   *config,
		logger:   logger.With(zap.String("driver", "dlt645")),
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

	// 构建点表映射
	d.pointMu.Lock()
	defer d.pointMu.Unlock()

	for i, pt := range d.config.Points {
		if pt.Name == "" {
			return fmt.Errorf("dlt645: point[%d] missing Name field", i)
		}

		// 解析地址（验证格式）
		addrBytes, err := ParseAddressString(pt.Address)
		if err != nil {
			return fmt.Errorf("dlt645: point[%d] invalid address %s: %w", i, pt.Address, err)
		}

		// 解析数据标识
		dataID, err := ParseDataIDString(pt.DataID, d.config.ProtocolVersion)
		if err != nil {
			return fmt.Errorf("dlt645: point[%d] invalid dataID %s: %w", i, pt.DataID, err)
		}

		// 构建键（地址反转以匹配帧格式，DataID 不反转直接拼接）
		addrStr := fmt.Sprintf("%02X%02X%02X%02X%02X%02X",
			addrBytes[5], addrBytes[4], addrBytes[3],
			addrBytes[2], addrBytes[1], addrBytes[0])
		// DataID 直接拼接（低字节在前）
		dataIDStr := fmt.Sprintf("%02X%02X%02X%02X",
			dataID[0], dataID[1], dataID[2], dataID[3])
		key := fmt.Sprintf("%s_%s", addrStr, dataIDStr)
		d.pointMap[key] = &d.config.Points[i]

		d.logger.Debug("point mapping",
			zap.String("name", pt.Name),
			zap.String("address", pt.Address),
			zap.String("data_id", pt.DataID),
			zap.String("key", key),
		)
	}

	// 创建客户端
	d.client = NewClient(&d.config, d.logger)

	// 创建处理器
	d.handler = NewHandler(d, &d.config, d.logger)

	d.logger.Info("DLT645 driver initialized",
		zap.String("port", d.config.SerialPort),
		zap.Int("baud_rate", d.config.BaudRate),
		zap.String("parity", d.config.Parity),
		zap.Int("protocol_version", int(d.config.ProtocolVersion)),
		zap.Int("points", len(d.config.Points)),
	)

	return nil
}

// Start 实现 driver.Driver 接口
func (d *Driver) Start(ctx context.Context, bus *broker.Bus) error {
	if !atomic.CompareAndSwapUint32(&d.state, 0, 1) {
		return fmt.Errorf("dlt645: driver already running")
	}

	d.bus = bus
	d.ctx, d.cancel = context.WithCancel(ctx)

	// 启动连接循环
	d.wg.Add(1)
	go d.connectLoop()

	d.logger.Info("DLT645 driver started (connecting in background)")
	return nil
}

// Stop 实现 driver.Driver 接口
func (d *Driver) Stop(_ context.Context) error {
	if !atomic.CompareAndSwapUint32(&d.state, 1, 2) {
		return nil
	}

	d.logger.Info("stopping DLT645 driver...")
	d.cancel()

	d.wg.Wait()

	if d.client != nil {
		d.client.Close()
	}

	d.logger.Info("DLT645 driver stopped")
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

			select {
			case <-d.ctx.Done():
				return
			case <-time.After(retryInterval):
			}

			retryInterval *= 2
			if retryInterval > maxRetryInterval {
				retryInterval = maxRetryInterval
			}
			continue
		}

		// 连接成功
		retryInterval = d.config.RetryInterval
		atomic.StoreInt64(&d.atomicStats.connectionDuration, time.Now().Unix())
		atomic.AddUint64(&d.atomicStats.reconnectCount, 1)

		// 启动采集循环
		d.wg.Add(1)
		go d.pollLoop()

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

// ─────────────────────────────────────────────────────────────────────────────
// 采集循环
// ─────────────────────────────────────────────────────────────────────────────

// pollLoop 采集循环
func (d *Driver) pollLoop() {
	defer d.wg.Done()
	defer func() {
		atomic.StoreUint32(&d.isConnected, 0)
		d.client.Close()
		d.publishDisconnected()
	}()

	// 记录采集周期
	ticker := time.NewTicker(d.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			if atomic.LoadUint32(&d.isConnected) == 0 {
				return // 连接已断开
			}
			d.pollAllPoints()
		}
	}
}

// pollAllPoints 采集所有测点，一轮完成后批量发布
// 串行采集（DL/T 645 协议是同步问答）
func (d *Driver) pollAllPoints() {
	d.pointMu.RLock()
	points := make([]*PointConfig, 0, len(d.pointMap))
	for _, pt := range d.pointMap {
		points = append(points, pt)
	}
	d.pointMu.RUnlock()

	// 收集本轮采集结果
	var results []*model.PointData

	// 串行采集每个测点
	for _, pt := range points {
		// 解析地址
		addr, err := ParseAddressString(pt.Address)
		if err != nil {
			d.logger.Error("parse address failed",
				zap.String("address", pt.Address),
				zap.Error(err),
			)
			continue
		}

		// 解析数据标识
		dataID, err := ParseDataIDString(pt.DataID, d.config.ProtocolVersion)
		if err != nil {
			d.logger.Error("parse dataID failed",
				zap.String("data_id", pt.DataID),
				zap.Error(err),
			)
			continue
		}

		// 发送请求并接收响应
		frame, err := d.client.SendRequest(addr, dataID)
		if err != nil {
			d.logger.Debug("send request failed",
				zap.String("address", pt.Address),
				zap.String("data_id", pt.DataID),
				zap.Error(err),
			)
			atomic.AddUint64(&d.atomicStats.errCount, 1)
			continue
		}

		atomic.AddUint64(&d.atomicStats.frameReceivedCount, 1)

		// 处理响应
		p, err := d.handler.ProcessFrame(frame)
		if err != nil {
			d.logger.Error("handle frame failed",
				zap.Error(err),
			)
			continue
		}
		if p != nil {
			results = append(results, p)
		}

		// 每测点间隔（缩短，提高采集速度）
		if d.config.QueryIntervalPerPoint > 0 {
			time.Sleep(d.config.QueryIntervalPerPoint)
		}
	}

	// 一轮采集完成后，批量发布
	for _, p := range results {
		d.bus.Publish(p)
	}

	atomic.AddUint64(&d.atomicStats.pollCount, 1)
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
		p.ID = fmt.Sprintf("%s/dlt645/%s", d.config.Name, pt.Name)
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
		"frame_received_count":   atomic.LoadUint64(&d.atomicStats.frameReceivedCount),
		"reconnect_count":        atomic.LoadUint64(&d.atomicStats.reconnectCount),
		"connected":              atomic.LoadUint32(&d.isConnected) == 1,
		"connection_duration": func() time.Duration {
			if ct := atomic.LoadInt64(&d.atomicStats.connectionDuration); ct > 0 {
				return time.Since(time.Unix(ct, 0))
			}
			return 0
		}(),
	}
}