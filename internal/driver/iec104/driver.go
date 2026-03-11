// internal/driver/iec104/driver.go - IEC 60870-5-104 驱动（基于 wendy512/iec104 库）
//
// 重构说明：
//   1. 使用 github.com/wendy512/iec104 纯Go库
//   2. 实现高效的 map[uint32]*PointConfig 点表索引 (O(1) 查找)
//   3. 使用 Worker Pool 处理高并发 ASDU 报文
//   4. 支持带时标和不带时标的报文类型
//   5. 实现 GI 防风暴和时钟同步机制
//   6. 配合 sync.Pool 减少内存分配
package iec104

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/model"
	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
	"github.com/wendy512/iec104/client"
	"go.uber.org/zap"
)

// 点表映射（CA+IOA -> PointConfig）
// 使用 map[uint32] 实现 O(1) 时间复杂度的查找
type pointMapping struct {
	config        PointConfig
	lastValue     float64
	lastTimestamp int64
}

// iec104Handler 实现 client.ASDUCall 接口
type iec104Handler struct {
	driver *Driver
}

// Driver IEC 104 南向驱动（基于 wendy512/iec104 库）
type Driver struct {
	cfg    Config
	bus    *broker.Bus
	logger *zap.Logger

	// IEC104 客户端
	client *client.Client

	// 点表映射（高效索引，key = (CA << 24) | IOA）
	pointMap   map[uint32]*pointMapping
	pointMutex sync.RWMutex

	// 运行状态
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	state  uint32 // 0=未启动 1=运行中 2=已停止

	// 连接状态
	isConnected uint32 // 0=断开 1=已连接

	// 统计信息
	atomicStats struct {
		pollCount          uint64
		errCount           uint64
		giCount            uint64
		clockSyncCount     uint64
		lastGITime         int64
		lastClockSyncTime  int64
		asduReceivedCount  uint64
		connectionDuration int64
		reconnectCount     uint64
	}

	// 上次系统测点更新时间
	lastMetricsUpdate time.Time

	// Worker Pool
	asduWorkers  int
	asduChan     chan *asdu.ASDU
	asduWorkerWg sync.WaitGroup

	// GI防风暴
	giStaggeredDelay time.Duration
}

// New 创建 IEC104 驱动实例
func New(cfg Config, logger *zap.Logger) *Driver {
	cfg.fillDefaults()
	return &Driver{
		cfg:             cfg,
		logger:          logger.With(zap.String("driver", "iec104")),
		pointMap:        make(map[uint32]*pointMapping),
		asduWorkers:     10,
		asduChan:        make(chan *asdu.ASDU, 5000), // 大缓冲区防止GI风暴阻塞
		giStaggeredDelay: cfg.GIStaggeredDelay,
	}
}

// Name 实现 driver.Driver 接口
func (d *Driver) Name() string { return "iec104" }

// Init 校验配置，构建点表映射
func (d *Driver) Init(_ context.Context) error {
	if d.cfg.Name == "" {
		return fmt.Errorf("iec104: 缺少 Name 字段")
	}
	if d.cfg.Host == "" {
		return fmt.Errorf("iec104: 缺少 Host 字段")
	}

	// 构建点表映射（预编译，O(1)查找）
	d.pointMutex.Lock()
	defer d.pointMutex.Unlock()

	for i, pt := range d.cfg.Points {
		if pt.Name == "" {
			return fmt.Errorf("iec104: point[%d] 缺少 Name 字段", i)
		}

		// 构建复合键：(CA << 24) | IOA
		// CA占用高8位，IOA占用低24位
		key := (uint32(pt.CA) << 24) | uint32(pt.IOA)
		d.pointMap[key] = &pointMapping{
			config: pt,
		}

		// 调试日志：记录点表映射
		d.logger.Debug("点表映射",
			zap.String("name", pt.Name),
			zap.Uint8("ca", pt.CA),
			zap.Uint32("ioa", pt.IOA),
			zap.Uint32("key", key),
		)
	}

	d.logger.Info("IEC104 驱动初始化完成",
		zap.String("host", d.cfg.Host),
		zap.Int("port", d.cfg.Port),
		zap.Uint8("common_address", d.cfg.CommonAddress),
		zap.Int("points", len(d.cfg.Points)),
	)

	return nil
}

// Start 实现 driver.Driver 接口，启动连接并监听数据
// 重要：此方法启动后台连接协程后立即返回 nil，不阻塞等待连接成功。
// 连接失败、断线重连等逻辑都在后台协程中处理。
func (d *Driver) Start(ctx context.Context, bus *broker.Bus) error {
	if !atomic.CompareAndSwapUint32(&d.state, 0, 1) {
		return fmt.Errorf("iec104: 驱动已在运行中")
	}

	d.bus = bus
	d.ctx, d.cancel = context.WithCancel(ctx)

	// 创建IEC104客户端设置
	settings := client.NewSettings()
	settings.Host = d.cfg.Host
	settings.Port = d.cfg.Port
	settings.AutoConnect = true
	settings.ReconnectInterval = d.cfg.ReconnectInterval

	// 设置连接超时
	if settings.Cfg104 == nil {
		settings.Cfg104 = &cs104.Config{}
	}
	settings.Cfg104.ConnectTimeout0 = d.cfg.Timeout

	// 创建IEC104客户端
	handler := &iec104Handler{driver: d}
	d.client = client.New(settings, handler)

	// 设置连接成功回调
	d.client.SetOnConnectHandler(func(c *client.Client) {
		d.logger.Info("IEC104 连接成功",
			zap.String("host", d.cfg.Host),
			zap.Int("port", d.cfg.Port),
			zap.Uint64("reconnect_count", atomic.LoadUint64(&d.atomicStats.reconnectCount)),
		)
		atomic.StoreInt64(&d.atomicStats.connectionDuration, time.Now().Unix())
		atomic.StoreUint32(&d.isConnected, 1)
		atomic.AddUint64(&d.atomicStats.reconnectCount, 1)
	})

	// 设置Server Active回调（链路激活）
	d.client.SetServerActiveHandler(func(c *client.Client) {
		d.logger.Info("IEC104 Server Active - 链路已激活")

		// 启动ASDU处理Worker Pool
		d.startASDUWorkers()

		// 链路激活后，立即发送总召唤命令（TypeID 100）
		// 这是 IEC 60870-5-104 标准要求的行为
		d.logger.Info("链路激活，立即发送总召唤命令")
		if err := d.sendGeneralInterrogation(); err != nil {
			d.logger.Error("初始总召唤失败", zap.Error(err))
		}

		// 启动总召唤定时器（如果启用，且 GIInterval > 0）
		if d.cfg.GIInterval > 0 {
			d.wg.Add(1)
			go d.generalInterrogationLoop()
			d.logger.Info("总召唤定时器已启动", zap.Duration("interval", d.cfg.GIInterval))
		}

		// 启动时钟同步定时器（如果启用）
		if d.cfg.ClockSyncInterval > 0 {
			d.wg.Add(1)
			go d.clockSyncLoop()
			d.logger.Info("时钟同步定时器已启动", zap.Duration("interval", d.cfg.ClockSyncInterval))
		}
	})

	// 设置断线回调
	d.client.SetConnectionLostHandler(func(c *client.Client) {
		d.logger.Warn("IEC104 连接断开",
			zap.String("host", d.cfg.Host),
			zap.Int("port", d.cfg.Port),
		)
		atomic.StoreUint32(&d.isConnected, 0)

		// 发布断线质量戳
		d.publishDisconnected()
	})

	// 启动后台连接协程（非阻塞）
	d.wg.Add(1)
	go d.connectLoop()

	d.logger.Info("IEC104 驱动已启动（后台连接中）")
	return nil
}

// connectLoop 后台连接协程，负责初始连接和断线重连
func (d *Driver) connectLoop() {
	defer d.wg.Done()

	// 初始连接
	d.tryConnect()

	// 监听上下文取消
	<-d.ctx.Done()
}

// tryConnect 尝试连接，失败时记录日志但不退出
func (d *Driver) tryConnect() {
	if d.client == nil {
		return
	}

	if err := d.client.Connect(); err != nil {
		d.logger.Warn("IEC104 连接失败，将在后台重试",
			zap.String("host", d.cfg.Host),
			zap.Int("port", d.cfg.Port),
			zap.Error(err),
		)
		atomic.AddUint64(&d.atomicStats.errCount, 1)
		// 不返回错误，AutoConnect 会自动重试
	}
}

// Stop 实现 driver.Driver 接口
func (d *Driver) Stop(_ context.Context) error {
	if !atomic.CompareAndSwapUint32(&d.state, 1, 2) {
		return nil // 未启动或已停止，直接返回
	}

	d.logger.Info("正在停止 IEC104 驱动...")
	d.cancel()

	// 关闭ASDU通道
	close(d.asduChan)
	d.asduWorkerWg.Wait()

	d.wg.Wait()

	// 关闭IEC104客户端
	if d.client != nil {
		if err := d.client.Close(); err != nil {
			d.logger.Error("关闭IEC104客户端失败", zap.Error(err))
		}
		d.client = nil
	}

	d.logger.Info("IEC104 驱动已完全停止")
	return nil
}

// startASDUWorkers 启动ASDU处理Worker Pool
func (d *Driver) startASDUWorkers() {
	for i := 0; i < d.asduWorkers; i++ {
		d.asduWorkerWg.Add(1)
		go d.asduWorker(i)
	}
}

// asduWorker ASDU处理Worker
func (d *Driver) asduWorker(workerID int) {
	defer d.asduWorkerWg.Done()

	d.logger.Debug("ASDU Worker已启动", zap.Int("worker_id", workerID))

	for {
		select {
		case <-d.ctx.Done():
			return
		case asduData, ok := <-d.asduChan:
			if !ok {
				return
			}

			// 处理ASDU数据
			d.processASDU(asduData)
		}
	}
}

// processASDU 处理ASDU数据
func (d *Driver) processASDU(asduData *asdu.ASDU) {
	// 解析公共地址
	ca := uint8(asduData.CommonAddr)

	// 根据类型ID处理不同的信息对象
	typeID := asduData.Type

	// 调试日志：记录收到的 ASDU
	d.logger.Debug("收到ASDU",
		zap.Uint8("type_id", uint8(typeID)),
		zap.Uint8("ca", ca),
		zap.Int("common_addr", int(asduData.CommonAddr)),
	)

	switch typeID {
	case asdu.M_SP_NA_1, asdu.M_SP_TA_1:
		// 单点遥信
		for _, p := range asduData.GetSinglePoint() {
			d.logger.Debug("单点遥信",
				zap.Uint8("ca", ca),
				zap.Uint32("ioa", uint32(p.Ioa)),
				zap.Bool("value", p.Value),
			)
			d.processSinglePoint(ca, uint32(p.Ioa), p.Value, p.Qds, p.Time)
		}

	case asdu.M_DP_NA_1, asdu.M_DP_TA_1, asdu.M_DP_TB_1:
		// 双点遥信
		for _, p := range asduData.GetDoublePoint() {
			d.processDoublePoint(ca, uint32(p.Ioa), p.Value, p.Qds, p.Time)
		}

	case asdu.M_ME_NC_1, asdu.M_ME_TC_1, asdu.M_ME_TF_1:
		// 短浮点数
		for _, p := range asduData.GetMeasuredValueFloat() {
			d.logger.Debug("短浮点数遥测",
				zap.Uint8("ca", ca),
				zap.Uint32("ioa", uint32(p.Ioa)),
				zap.Float32("value", p.Value),
			)
			d.processMeasuredValueFloat(ca, uint32(p.Ioa), p.Value, p.Qds, p.Time)
		}

	case asdu.M_ME_NA_1, asdu.M_ME_TA_1, asdu.M_ME_TD_1, asdu.M_ME_ND_1:
		// 归一化值
		for _, p := range asduData.GetMeasuredValueNormal() {
			d.processMeasuredValueNormal(ca, uint32(p.Ioa), p.Value, p.Qds, p.Time)
		}

	case asdu.M_ME_NB_1, asdu.M_ME_TB_1, asdu.M_ME_TE_1:
		// 标度化值
		for _, p := range asduData.GetMeasuredValueScaled() {
			d.processMeasuredValueScaled(ca, uint32(p.Ioa), p.Value, p.Qds, p.Time)
		}

	case asdu.M_ST_NA_1, asdu.M_ST_TA_1, asdu.M_ST_TB_1:
		// 步位置信息
		for _, p := range asduData.GetStepPosition() {
			d.processStepPosition(ca, uint32(p.Ioa), int32(p.Value.Val), p.Qds, p.Time)
		}

	case asdu.M_BO_NA_1, asdu.M_BO_TA_1, asdu.M_BO_TB_1:
		// 32位比特串
		for _, p := range asduData.GetBitString32() {
			d.processBitString32(ca, uint32(p.Ioa), p.Value, p.Qds, p.Time)
		}

	case asdu.M_IT_NA_1, asdu.M_IT_TA_1, asdu.M_IT_TB_1:
		// 累计量
		for _, p := range asduData.GetIntegratedTotals() {
			d.processIntegratedTotals(ca, uint32(p.Ioa), uint32(p.Value.CounterReading), p.Value.IsInvalid, p.Time)
		}

	default:
		// 不支持的类型，跳过
		d.logger.Debug("不支持的ASDU类型",
			zap.Uint8("type_id", uint8(typeID)),
			zap.Uint8("ca", ca),
		)
	}

	atomic.AddUint64(&d.atomicStats.asduReceivedCount, 1)

	// 如果启用了系统测点，定期更新
	if d.cfg.EnableSystemMetrics && time.Since(d.lastMetricsUpdate) > time.Second {
		d.publishSystemMetrics()
	}
}

// processSinglePoint 处理单点遥信
func (d *Driver) processSinglePoint(ca uint8, ioa uint32, value bool, qds asdu.QualityDescriptor, timestamp time.Time) {
	d.publishPointData(ca, ioa, func() float64 {
		if value {
			return 1.0
		}
		return 0.0
	}, qds, timestamp)
}

// processDoublePoint 处理双点遥信
func (d *Driver) processDoublePoint(ca uint8, ioa uint32, value asdu.DoublePoint, qds asdu.QualityDescriptor, timestamp time.Time) {
	d.publishPointData(ca, ioa, func() float64 {
		return float64(value)
	}, qds, timestamp)
}

// processMeasuredValueFloat 处理短浮点数
func (d *Driver) processMeasuredValueFloat(ca uint8, ioa uint32, value float32, qds asdu.QualityDescriptor, timestamp time.Time) {
	d.publishPointData(ca, ioa, func() float64 {
		return float64(value)
	}, qds, timestamp)
}

// processMeasuredValueNormal 处理归一化值
func (d *Driver) processMeasuredValueNormal(ca uint8, ioa uint32, value asdu.Normalize, qds asdu.QualityDescriptor, timestamp time.Time) {
	d.publishPointData(ca, ioa, func() float64 {
		return value.Float64() * 100.0 // 归一化值转换为百分比
	}, qds, timestamp)
}

// processMeasuredValueScaled 处理标度化值
func (d *Driver) processMeasuredValueScaled(ca uint8, ioa uint32, value int16, qds asdu.QualityDescriptor, timestamp time.Time) {
	d.publishPointData(ca, ioa, func() float64 {
		return float64(value)
	}, qds, timestamp)
}

// processStepPosition 处理步位置信息
func (d *Driver) processStepPosition(ca uint8, ioa uint32, value int32, qds asdu.QualityDescriptor, timestamp time.Time) {
	d.publishPointData(ca, ioa, func() float64 {
		return float64(value)
	}, qds, timestamp)
}

// processBitString32 处理32位比特串
func (d *Driver) processBitString32(ca uint8, ioa uint32, value uint32, qds asdu.QualityDescriptor, timestamp time.Time) {
	d.publishPointData(ca, ioa, func() float64 {
		return float64(value)
	}, qds, timestamp)
}

// processIntegratedTotals 处理累计量
func (d *Driver) processIntegratedTotals(ca uint8, ioa uint32, value uint32, isInvalid bool, timestamp time.Time) {
	// 累计量使用特殊的 QualityDescriptor
	var qds asdu.QualityDescriptor
	if isInvalid {
		qds = asdu.QDSInvalid
	}
	d.publishPointData(ca, ioa, func() float64 {
		return float64(value)
	}, qds, timestamp)
}

// publishPointData 发布点数据
// timestamp: ASDU 报文中的 CP56Time2a 时标，如果为零值则使用当前时间
// qds: IEC104 质量描述符
func (d *Driver) publishPointData(ca uint8, ioa uint32, getValue func() float64, qds asdu.QualityDescriptor, timestamp time.Time) {
	// 构建点表key：key = (CA << 24) | IOA
	key := (uint32(ca) << 24) | ioa

	// O(1)查找点表配置
	d.pointMutex.RLock()
	ptMapping, ok := d.pointMap[key]
	d.pointMutex.RUnlock()

	if !ok {
		// 未配置的点，记录调试日志
		d.logger.Debug("点表未匹配，跳过",
			zap.Uint8("ca", ca),
			zap.Uint32("ioa", ioa),
			zap.Uint32("key", key),
		)
		return
	}

	// 获取原始值
	value := getValue()

	// 应用 Scale 和 Offset
	scaledValue := value*ptMapping.config.Scale + ptMapping.config.Offset

	// 确定时间戳
	// 重要：如果 ASDU 报文带有时标（CP56Time2a），必须使用该时标
	// 只有当 timestamp.IsZero() 时才使用本地时间
	var timestampNs int64
	if timestamp.IsZero() {
		// 报文无时标，使用本地时间
		timestampNs = time.Now().UnixNano()
	} else {
		// 使用报文中的 CP56Time2a 时标
		timestampNs = timestamp.UnixNano()
	}

	// 死区过滤
	if ptMapping.config.DeadbandValue > 0 {
		if d.shouldFilter(ptMapping, scaledValue, timestampNs) {
			return // 被死区过滤，不发布
		}
	}

	// 映射质量码
	mappedQuality := d.mapQuality(qds)

	// 使用 sync.Pool 获取 PointData
	p := model.GetPoint()
	p.ID = fmt.Sprintf("%s/iec104/%s", d.cfg.Name, ptMapping.config.Name)
	p.Value = scaledValue
	p.Timestamp = timestampNs
	p.Quality = mappedQuality

	// 发布数据
	d.bus.Publish(p)

	// 调试日志
	d.logger.Debug("发布测点数据",
		zap.String("id", p.ID),
		zap.Float64("value", scaledValue),
	)

	// 更新统计
	atomic.AddUint64(&d.atomicStats.pollCount, 1)
}

// shouldFilter 判断是否应该被死区过滤
func (d *Driver) shouldFilter(ptMapping *pointMapping, value float64, timestamp int64) bool {
	// 第一次采集，不过滤
	if ptMapping.lastTimestamp == 0 {
		ptMapping.lastValue = value
		ptMapping.lastTimestamp = timestamp
		return false
	}

	// 时间间隔小于1秒，不过滤（避免死区导致长时间不更新）
	if timestamp-ptMapping.lastTimestamp < int64(time.Second) {
		ptMapping.lastValue = value
		ptMapping.lastTimestamp = timestamp
		return false
	}

	threshold := ptMapping.config.DeadbandValue
	if ptMapping.config.DeadbandType == DeadbandPercent {
		// 百分比死区
		threshold = math.Abs(ptMapping.lastValue) * ptMapping.config.DeadbandValue / 100.0
	}

	// 计算变化量
	delta := math.Abs(value - ptMapping.lastValue)
	if delta < threshold {
		return true // 变化量小于阈值，过滤
	}

	// 更新上一次的值
	ptMapping.lastValue = value
	ptMapping.lastTimestamp = timestamp
	return false
}

// mapQuality 映射 IEC104 质量码到增强质量码
// IEC104 质量描述符 (QualityDescriptor) 定义：
// - QDSInvalid (bit 4): 无效 - 数据被错误获取
// - QDSNotTopical (bit 3): 非当前 - 最近更新失败
// - QDSSubstituted (bit 2): 被取代 - 由操作员输入而非自动源
// - QDSBlocked (bit 1): 被封锁 - 值被阻止传输
// - QDSOverflow (bit 0): 溢出 - 值超出预定义范围
func (d *Driver) mapQuality(qds asdu.QualityDescriptor) uint8 {
	// 按优先级检查质量位（从最严重到最轻微）
	
	// IV=1: 数据无效（最高优先级）
	if qds&asdu.QDSInvalid != 0 {
		return model.QualityBad
	}
	
	// OV=1: 溢出
	if qds&asdu.QDSOverflow != 0 {
		return model.QualityBad
	}
	
	// NT=1: 非当前值
	if qds&asdu.QDSNotTopical != 0 {
		return model.QualityLastKnownValid
	}
	
	// SB=1: 被取代
	if qds&asdu.QDSSubstituted != 0 {
		return model.QualityUncertain
	}
	
	// BL=1: 被封锁
	if qds&asdu.QDSBlocked != 0 {
		return model.QualityUncertain
	}

	// QDSGood: 数据正常
	return model.QualityGood
}

// publishDisconnected 对所有测点发布 QualityNotConnected 质量戳
// 供北向系统感知设备离线状态
func (d *Driver) publishDisconnected() {
	if d.bus == nil {
		return
	}

	ts := time.Now().UnixNano()
	d.pointMutex.RLock()
	defer d.pointMutex.RUnlock()

	for _, ptMapping := range d.pointMap {
		p := model.GetPoint()
		p.ID = fmt.Sprintf("%s/iec104/%s", d.cfg.Name, ptMapping.config.Name)
		p.Value = nil
		p.Timestamp = ts
		p.Quality = model.QualityNotConnected
		d.bus.Publish(p)
	}

	d.logger.Info("已发布断线质量戳",
		zap.Int("points", len(d.pointMap)),
	)
}

// sendGeneralInterrogation 发送总召唤命令
func (d *Driver) sendGeneralInterrogation() error {
	if d.client == nil {
		d.logger.Warn("IEC104 客户端未初始化，无法发送总召唤命令")
		return nil
	}

	if err := d.client.SendInterrogationCmd(uint16(d.cfg.CommonAddress)); err != nil {
		d.logger.Error("发送总召唤命令失败", zap.Error(err))
		atomic.AddUint64(&d.atomicStats.errCount, 1)
		return err
	}

	atomic.AddUint64(&d.atomicStats.giCount, 1)
	atomic.StoreInt64(&d.atomicStats.lastGITime, time.Now().Unix())

	d.logger.Info("已发送总召唤命令",
		zap.Uint8("ca", d.cfg.CommonAddress),
		zap.Uint64("count", atomic.LoadUint64(&d.atomicStats.giCount)),
	)

	return nil
}

// sendClockSync 发送时钟同步命令
func (d *Driver) sendClockSync() error {
	if d.client == nil {
		d.logger.Warn("IEC104 客户端未初始化，无法发送时钟同步命令")
		return nil
	}

	if err := d.client.SendClockSynchronizationCmd(uint16(d.cfg.CommonAddress), time.Now()); err != nil {
		d.logger.Error("发送时钟同步命令失败", zap.Error(err))
		atomic.AddUint64(&d.atomicStats.errCount, 1)
		return err
	}

	atomic.AddUint64(&d.atomicStats.clockSyncCount, 1)
	atomic.StoreInt64(&d.atomicStats.lastClockSyncTime, time.Now().Unix())

	d.logger.Info("已发送时钟同步命令",
		zap.Uint8("ca", d.cfg.CommonAddress),
		zap.Uint64("count", atomic.LoadUint64(&d.atomicStats.clockSyncCount)),
	)

	return nil
}

// generalInterrogationLoop 总召唤定时器循环
// 每 GIInterval 发送一次总召唤命令，确保数据同步
func (d *Driver) generalInterrogationLoop() {
	defer d.wg.Done()

	ticker := time.NewTicker(d.cfg.GIInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// 检查连接状态
			if atomic.LoadUint32(&d.isConnected) == 0 {
				d.logger.Debug("连接断开，跳过定时总召唤")
				continue
			}

			// 添加随机延迟，防止多设备同时发送 GI 导致风暴
			if d.giStaggeredDelay > 0 {
				delay := time.Duration(rand.Int63n(int64(d.giStaggeredDelay)))
				d.logger.Debug("GI 随机延迟", zap.Duration("delay", delay))
				time.Sleep(delay)
			}

			d.sendGeneralInterrogation()
		}
	}
}

// clockSyncLoop 时钟同步定时器循环
func (d *Driver) clockSyncLoop() {
	defer d.wg.Done()

	ticker := time.NewTicker(d.cfg.ClockSyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.sendClockSync()
		}
	}
}

// publishSystemMetrics 发布系统测点（延迟、丢包率等）
func (d *Driver) publishSystemMetrics() {
	if !d.cfg.EnableSystemMetrics {
		return
	}

	d.lastMetricsUpdate = time.Now()

	// 计算丢包率
	pollCount := atomic.LoadUint64(&d.atomicStats.pollCount)
	errCount := atomic.LoadUint64(&d.atomicStats.errCount)
	var packetLossRate float64
	if pollCount > 0 {
		packetLossRate = float64(errCount) / float64(pollCount) * 100.0
	}

	// 发布连接状态测点
	statusPoint := model.GetPoint()
	statusPoint.ID = fmt.Sprintf("$%s/status", d.cfg.Name)
	if d.client != nil && d.client.IsConnected() {
		statusPoint.Value = 1.0 // 1=连接，0=断开
	} else {
		statusPoint.Value = 0.0
	}
	statusPoint.Timestamp = time.Now().UnixNano()
	statusPoint.Quality = model.QualityGood
	d.bus.Publish(statusPoint)

	// 发布丢包率测点
	lossPoint := model.GetPoint()
	lossPoint.ID = fmt.Sprintf("$%s/packet_loss_rate", d.cfg.Name)
	lossPoint.Value = packetLossRate
	lossPoint.Timestamp = time.Now().UnixNano()
	lossPoint.Quality = model.QualityGood
	d.bus.Publish(lossPoint)

	// 发布总召唤次数测点
	giPoint := model.GetPoint()
	giPoint.ID = fmt.Sprintf("$%s/gi_count", d.cfg.Name)
	giPoint.Value = float64(atomic.LoadUint64(&d.atomicStats.giCount))
	giPoint.Timestamp = time.Now().UnixNano()
	giPoint.Quality = model.QualityGood
	d.bus.Publish(giPoint)

	// 发布ASDU接收次数测点
	asduPoint := model.GetPoint()
	asduPoint.ID = fmt.Sprintf("$%s/asdu_count", d.cfg.Name)
	asduPoint.Value = float64(atomic.LoadUint64(&d.atomicStats.asduReceivedCount))
	asduPoint.Timestamp = time.Now().UnixNano()
	asduPoint.Quality = model.QualityGood
	d.bus.Publish(asduPoint)
}

// Stats 返回运行统计信息
func (d *Driver) Stats() map[string]interface{} {
	return map[string]interface{}{
		"poll_count":           atomic.LoadUint64(&d.atomicStats.pollCount),
		"err_count":            atomic.LoadUint64(&d.atomicStats.errCount),
		"gi_count":             atomic.LoadUint64(&d.atomicStats.giCount),
		"clock_sync_count":     atomic.LoadUint64(&d.atomicStats.clockSyncCount),
		"asdu_received_count":  atomic.LoadUint64(&d.atomicStats.asduReceivedCount),
		"reconnect_count":      atomic.LoadUint64(&d.atomicStats.reconnectCount),
		"last_gi_time":         atomic.LoadInt64(&d.atomicStats.lastGITime),
		"last_clock_sync_time": atomic.LoadInt64(&d.atomicStats.lastClockSyncTime),
		"connected":            atomic.LoadUint32(&d.isConnected) == 1,
		"connection_duration": func() time.Duration {
			if ct := atomic.LoadInt64(&d.atomicStats.connectionDuration); ct > 0 {
				return time.Since(time.Unix(ct, 0))
			}
			return 0
		}(),
	}
}

// ── iec104Handler 实现 client.ASDUCall 接口 ─────────────────────────────

// OnInterrogation 总召唤回复
func (h *iec104Handler) OnInterrogation(packet *asdu.ASDU) error {
	addr, value := packet.GetInterrogationCmd()
	h.driver.logger.Debug("收到总召唤回复",
		zap.Uint("ca", uint(addr)),
		zap.Uint8("value", uint8(value)),
	)
	return nil
}

// OnCounterInterrogation 总计数器回复
func (h *iec104Handler) OnCounterInterrogation(packet *asdu.ASDU) error {
	return nil
}

// OnRead 读定值回复
func (h *iec104Handler) OnRead(packet *asdu.ASDU) error {
	return nil
}

// OnTestCommand 测试下发回复
func (h *iec104Handler) OnTestCommand(packet *asdu.ASDU) error {
	return nil
}

// OnClockSync 时钟同步回复
func (h *iec104Handler) OnClockSync(packet *asdu.ASDU) error {
	return nil
}

// OnResetProcess 进程重置回复
func (h *iec104Handler) OnResetProcess(packet *asdu.ASDU) error {
	return nil
}

// OnDelayAcquisition 延迟获取回复
func (h *iec104Handler) OnDelayAcquisition(packet *asdu.ASDU) error {
	return nil
}

// OnASDU 数据回复或控制回复
func (h *iec104Handler) OnASDU(packet *asdu.ASDU) error {
	// 将ASDU数据发送到Worker Pool处理
	select {
	case h.driver.asduChan <- packet:
		return nil
	case <-h.driver.ctx.Done():
		return h.driver.ctx.Err()
	}
}
