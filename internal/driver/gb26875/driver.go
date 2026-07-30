// internal/driver/gb26875/driver.go - GB/T 26875.3 驱动核心
//
// 角色：监控中心（TCP Server）
// 网关监听端口，等待传输装置（用户信息传输装置）主动连接。
// 接收上行数据 → 解析 → 转换为 PointData 发布到 bus → 回复确认帧。
// 同时支持主动下发：时钟同步(类型90)、初始化(89)、查岗(91) 等。
package gb26875

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/model"
	"go.uber.org/zap"
)

// Driver GB/T 26875.3 南向驱动
type Driver struct {
	cfg    Config
	logger *zap.Logger

	// TCP Server
	listener net.Listener

	// 已建立的传输装置连接（以源地址字符串为 key）
	connMu    sync.RWMutex
	conns     map[string]*Connection // key = "800D00000000"
	connCount int32                  // 原子计数

	// 点表索引（O(1) 查找）
	pointMap map[uint64]*PointConfig // key = (uint64(MessageType)<<40) | (uint64(SystemAddr)<<32) | (uint64(ComponentType)<<24) | ComponentAddrRaw
	pointMu  sync.RWMutex

	// 通用点表（仅按 MessageType + SystemType 匹配，如系统状态类）
	wildPoints []*PointConfig

	// 上下文与协程
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// 状态：0=未启动 1=运行中 2=已停止
	state uint32

	// 统计
	atomicStats struct {
		framesReceived  uint64
		framesParsed    uint64
		framesRejected  uint64
		ackSent         uint64
		denySent        uint64
		pointsPublished uint64
		connections     uint64
		disconnects     uint64
	}

	// 业务流水号自增（监控中心→装置）
	seqNo uint32

	bus *broker.Bus

	// 死区跟踪（key=pointID → last value/time）
	lastValues map[string]float64
	lastTimes   map[string]int64
}

// New 创建驱动实例（不启动）
func New(cfg Config, logger *zap.Logger) *Driver {
	cfg.fillDefaults()
	return &Driver{
		cfg:    cfg,
		logger: logger.With(zap.String("driver", "gb26875"), zap.String("name", cfg.Name)),
		conns:      make(map[string]*Connection),
		lastValues: make(map[string]float64),
		lastTimes:  make(map[string]int64),
	}
}

// Name 实现 driver.Driver 接口
func (d *Driver) Name() string { return "gb26875" }

// Init 校验配置 + 构建点表索引
func (d *Driver) Init(_ context.Context) error {
	if d.cfg.Name == "" {
		return fmt.Errorf("gb26875: 缺少 Name 字段")
	}
	if d.cfg.Port == 0 {
		return fmt.Errorf("gb26875: 缺少 Port 字段")
	}

	// 构建点表索引
	d.pointMap = make(map[uint64]*PointConfig, len(d.cfg.Points))
	for i := range d.cfg.Points {
		pt := &d.cfg.Points[i]
		if pt.Name == "" {
			return fmt.Errorf("gb26875: point[%d] 缺少 Name 字段", i)
		}

		// 部件地址 → uint32（小端解释）
		var raw uint32
		if pt.ComponentAddr != "" {
			b, err := ParseComponentAddrString(pt.ComponentAddr)
			if err != nil {
				return fmt.Errorf("gb26875: point[%d] ComponentAddr 解析失败: %w", i, err)
			}
			raw = uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
		}

		// 复合 key：type(8) | sysaddr(8) | comptype(8) | component_addr(32)
		key := uint64(pt.MessageType)<<40 | uint64(pt.SystemAddress)<<32 |
			uint64(pt.ComponentType)<<24 | uint64(raw)

		// 同 key 多测点：用链表/后续追加 — 这里用 map+append 处理多个测点匹配同一 info
		if existing, exists := d.pointMap[key]; exists {
			d.logger.Warn("点表 key 重复，将覆盖先前的配置",
				zap.String("old_name", existing.Name),
				zap.String("new_name", pt.Name),
				zap.Uint64("key", key),
			)
		}
		d.pointMap[key] = pt

		// DeviceAddress 为空且 ComponentAddr 为空的点：归为"通配"点（按 MessageType+SystemType 匹配）
		if pt.DeviceAddress == "" && pt.ComponentAddr == "" {
			d.wildPoints = append(d.wildPoints, pt)
		}

		d.logger.Debug("点表索引构建",
			zap.String("name", pt.Name),
			zap.Uint8("type", pt.MessageType),
			zap.Uint8("sys", pt.SystemAddress),
			zap.Uint8("comp_type", pt.ComponentType),
			zap.String("comp_addr", pt.ComponentAddr),
			zap.Uint64("key", key),
		)
	}

	d.logger.Info("GB/T 26875.3 驱动初始化完成",
		zap.String("host", d.cfg.Host),
		zap.Int("port", d.cfg.Port),
		zap.Int("points", len(d.cfg.Points)),
		zap.Int("wild_points", len(d.wildPoints)),
	)
	return nil
}

// Start 启动 TCP 监听（非阻塞）
func (d *Driver) Start(ctx context.Context, bus *broker.Bus) error {
	if !atomic.CompareAndSwapUint32(&d.state, 0, 1) {
		return fmt.Errorf("gb26875: 驱动已在运行")
	}

	d.bus = bus
	d.ctx, d.cancel = context.WithCancel(ctx)

	addr := fmt.Sprintf("%s:%d", d.cfg.Host, d.cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		atomic.StoreUint32(&d.state, 0)
		return fmt.Errorf("gb26875: 监听 %s 失败: %w", addr, err)
	}
	d.listener = listener

	d.logger.Info("GB/T 26875.3 驱动已启动",
		zap.String("addr", addr),
		zap.Int("max_connections", d.cfg.MaxConnections),
	)

	// 启动 accept loop
	d.wg.Add(1)
	go d.acceptLoop()

	// 时钟同步（可选）
	if d.cfg.ClockSyncInterval > 0 {
		d.wg.Add(1)
		go d.clockSyncLoop()
	}

	return nil
}

// acceptLoop 接受新 TCP 连接
func (d *Driver) acceptLoop() {
	defer d.wg.Done()

	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.ctx.Done():
				return
			default:
			}
			d.logger.Warn("accept 失败", zap.Error(err))
			// 检查是否 listener 已关闭
			if isClosedErr(err) {
				return
			}
			// 短暂退避后继续
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		// 并发连接数限制
		if int(atomic.LoadInt32(&d.connCount)) >= d.cfg.MaxConnections {
			d.logger.Warn("连接数已达上限，拒绝新连接",
				zap.Int("current", int(atomic.LoadInt32(&d.connCount))),
				zap.Int("max", d.cfg.MaxConnections),
				zap.String("remote", conn.RemoteAddr().String()),
			)
			conn.Close()
			continue
		}

		atomic.AddUint64(&d.atomicStats.connections, 1)
		atomic.AddInt32(&d.connCount, 1)
		d.wg.Add(1)
		go d.handleNewConnection(conn)
	}
}

// handleNewConnection 处理新连接（运行传输装置注册 → 接收循环）
func (d *Driver) handleNewConnection(raw net.Conn) {
	defer d.wg.Done()
	defer atomic.AddInt32(&d.connCount, -1)
	defer atomic.AddUint64(&d.atomicStats.disconnects, 1)

	c := newConnection(raw, d)

	// 注册（按源地址，但首条报文前不知道源地址，先以 remote addr 占位）
	d.registerConnection(c)

	defer func() {
		d.unregisterConnection(c)
		c.Close()
	}()

	// 接收循环
	c.recvLoop(d.ctx)
}

// registerConnection 注册连接
func (d *Driver) registerConnection(c *Connection) {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	key := c.key()
	d.conns[key] = c
	d.logger.Info("传输装置已注册",
		zap.String("key", key),
		zap.String("remote", c.remote),
	)
}

// reRegisterConnection 重新注册（识别 srcAddr 后按地址重 key）
func (d *Driver) reRegisterConnection(c *Connection, newKey string) {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	if old, ok := d.conns[c.id]; ok && old == c {
		delete(d.conns, c.id)
	}
	if _, exists := d.conns[newKey]; !exists {
		d.conns[newKey] = c
	}
	d.logger.Info("传输装置已重注册（按源地址）",
		zap.String("old_key", c.id),
		zap.String("new_key", newKey),
	)
}

// unregisterConnection 注销连接
func (d *Driver) unregisterConnection(c *Connection) {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	key := c.key()
	if _, ok := d.conns[key]; ok {
		delete(d.conns, key)
		d.logger.Info("传输装置已注销",
			zap.String("key", key),
			zap.String("remote", c.remote),
		)

		// 发布断线质量戳
		d.publishDisconnected(key)
	}
}

// Stop 实现 driver.Driver 接口
func (d *Driver) Stop(_ context.Context) error {
	if !atomic.CompareAndSwapUint32(&d.state, 1, 2) {
		return nil
	}

	d.logger.Info("正在停止 GB/T 26875.3 驱动...")
	d.cancel()

	if d.listener != nil {
		d.listener.Close()
	}

	// 关闭所有连接
	d.connMu.Lock()
	for _, c := range d.conns {
		c.Close()
	}
	d.connMu.Unlock()

	d.wg.Wait()
	d.logger.Info("GB/T 26875.3 驱动已完全停止")
	return nil
}

// publishDisconnected 发布断线质量戳
func (d *Driver) publishDisconnected(devAddr string) {
	if d.bus == nil {
		return
	}
	ts := time.Now().UnixNano()

	d.pointMu.RLock()
	defer d.pointMu.RUnlock()

	for _, pt := range d.pointMap {
		// 只为匹配此装置的点发布
		if pt.DeviceAddress != "" && pt.DeviceAddress != devAddr {
			continue
		}
		p := model.GetPoint()
		p.ID = d.pointID(pt)
		p.Value = nil
		p.Timestamp = ts
		p.Quality = model.QualityNotConnected
		d.bus.Publish(p)
	}
}

// nextSeqNo 获取下一个业务流水号（原子递增）
func (d *Driver) nextSeqNo() uint16 {
	v := atomic.AddUint32(&d.seqNo, 1)
	return uint16(v)
}

// nowTimeLabel 当前时间 BCD
func (d *Driver) nowTimeLabel() TimeLabel {
	now := time.Now()
	return FormatTimeLabel(
		now.Year()%100,
		int(now.Month()),
		now.Day(),
		now.Hour(),
		now.Minute(),
		now.Second(),
	)
}

// localAddrBytes 本机源地址（下行报文的 src）
func (d *Driver) localAddrBytes() [6]byte {
	if d.cfg.LocalAddress == "" {
		return [6]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	}
	a, err := ParseAddrString(d.cfg.LocalAddress)
	if err != nil {
		return [6]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	}
	return a
}

// pointID 构造 PointData.ID：<驱动名>/gb26875/<Name> 或带装置前缀
func (d *Driver) pointID(pt *PointConfig) string {
	if pt.DeviceAddress != "" {
		return fmt.Sprintf("%s/gb26875/%s/%s", d.cfg.Name, pt.DeviceAddress, pt.Name)
	}
	return fmt.Sprintf("%s/gb26875/%s", d.cfg.Name, pt.Name)
}

// clockSyncLoop 时钟同步定时器
func (d *Driver) clockSyncLoop() {
	defer d.wg.Done()

	t := time.NewTicker(d.cfg.ClockSyncInterval)
	defer t.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
			d.broadcastClockSync()
		}
	}
}

// broadcastClockSync 向所有连接广播时钟同步命令（类型 90）
func (d *Driver) broadcastClockSync() {
	d.connMu.RLock()
	defer d.connMu.RUnlock()

	tl := d.nowTimeLabel()
	src := d.localAddrBytes()

	for key, c := range d.conns {
		// 解析目标装置地址
		dst, _ := ParseAddrString(key)

		// ADU：类型90 + 数目1 + 6字节BCD时间
		adu := []byte{TypeSyncClock, 0x01}
		adu = append(adu, tl[:]...)

		seqNo := d.nextSeqNo()
		frame := BuildControlFrame(seqNo, d.cfg.Version, d.cfg.UserVersion, tl, src, dst, adu)

		if err := c.sendBytes(frame); err != nil {
			d.logger.Warn("时钟同步发送失败",
				zap.String("key", key),
				zap.Error(err),
			)
		} else {
			d.logger.Info("时钟同步已发送",
				zap.String("key", key),
				zap.Uint16("seq", seqNo),
				zap.String("time", tl.String()),
			)
		}
	}
}

// Stats 返回运行统计
func (d *Driver) Stats() map[string]any {
	return map[string]any{
		"frames_received":  atomic.LoadUint64(&d.atomicStats.framesReceived),
		"frames_parsed":    atomic.LoadUint64(&d.atomicStats.framesParsed),
		"frames_rejected":  atomic.LoadUint64(&d.atomicStats.framesRejected),
		"ack_sent":         atomic.LoadUint64(&d.atomicStats.ackSent),
		"deny_sent":        atomic.LoadUint64(&d.atomicStats.denySent),
		"points_published": atomic.LoadUint64(&d.atomicStats.pointsPublished),
		"connections":      atomic.LoadUint64(&d.atomicStats.connections),
		"disconnects":      atomic.LoadUint64(&d.atomicStats.disconnects),
		"active_sessions":  atomic.LoadInt32(&d.connCount),
	}
}

// PointMapForTest 返回点表映射（仅用于测试）
func (d *Driver) PointMapForTest() map[uint64]*PointConfig {
	d.pointMu.RLock()
	defer d.pointMu.RUnlock()
	m := make(map[uint64]*PointConfig, len(d.pointMap))
	for k, v := range d.pointMap {
		m[k] = v
	}
	return m
}

// isClosedErr 判断是否为连接已关闭错误
func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	if err.Error() == "use of closed network connection" {
		return true
	}
	if err.Error() == "EOF" {
		return true
	}
	return false
}
