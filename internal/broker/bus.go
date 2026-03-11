// internal/broker/bus.go - 内部异步事件总线
//
// 内存优化要点（面向 RK3568J 4G 内存 / 50000 点高频场景）：
//
//  1. PointData 全程通过 model.GetPoint / PutPoint 走 sync.Pool，零堆分配。
//  2. 单订阅者路径：直接转发原始指针，无需复制，彻底消除 dispatch 分支的 Get/Put。
//  3. 多订阅者路径：每条消息仅复制 (N-1) 份，最后一份直接转发原始对象。
//  4. DeadbandFilter：lastFloat map 设最大条目数上限，避免 50k 测点把 map 撑大后
//     长期占用内存（超限后 LRU 淘汰最旧条目）。
//  5. Publish 非阻塞：背压时立即 PutPoint 归还 Pool，驱动侧永不阻塞。
package broker

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/cgn/gateway/internal/model"
)

// ─── DeadbandFilter ───────────────────────────────────────────────────────────

const deadbandMaxEntries = 65536 // 最多缓存 64k 个测点的上次值，约占 ~3 MB

// deadbandEntry 记录测点上次发布的值及其访问顺序（用于 LRU 淘汰）。
type deadbandEntry struct {
	value float64
	seq   uint64 // 访问时间序号，越小越旧
}

// DeadbandFilter 死区过滤器。
// 并发安全，每个测点独立维护上一次发布的数值。
// 当测点数量超过 deadbandMaxEntries 时，淘汰最久未更新的条目，防止内存无限增长。
type DeadbandFilter struct {
	mu      sync.Mutex
	entries map[string]*deadbandEntry
	clock   uint64 // 单调递增序号
}

func newDeadbandFilter() *DeadbandFilter {
	return &DeadbandFilter{
		entries: make(map[string]*deadbandEntry, 1024),
	}
}

// Pass 返回 true 表示数据应当通过（变化超出死区），false 表示抑制。
// threshold <= 0 时关闭死区过滤，所有数据直通。
func (f *DeadbandFilter) Pass(p *model.PointData, threshold float64) bool {
	if threshold <= 0 {
		return true
	}

	// 将 Value 转为 float64；非数值类型直接通过
	var cur float64
	switch v := p.Value.(type) {
	case float64:
		cur = v
	case float32:
		cur = float64(v)
	case int64:
		cur = float64(v)
	case int32:
		cur = float64(v)
	case int:
		cur = float64(v)
	default:
		return true
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.clock++
	seq := f.clock

	entry, ok := f.entries[p.ID]
	if !ok {
		// 新测点：先检查是否需要淘汰
		if len(f.entries) >= deadbandMaxEntries {
			f.evictOldest()
		}
		f.entries[p.ID] = &deadbandEntry{value: cur, seq: seq}
		return true
	}

	if math.Abs(cur-entry.value) >= threshold {
		entry.value = cur
		entry.seq = seq
		return true
	}
	// 即便未通过死区，也更新访问序号（视为"活跃"测点，不优先淘汰）
	entry.seq = seq
	return false
}

// evictOldest 淘汰 seq 最小（最久未更新）的条目。
// 调用方须持有 f.mu。
func (f *DeadbandFilter) evictOldest() {
	var oldestKey string
	var oldestSeq uint64 = ^uint64(0) // max uint64
	for k, e := range f.entries {
		if e.seq < oldestSeq {
			oldestSeq = e.seq
			oldestKey = k
		}
	}
	if oldestKey != "" {
		delete(f.entries, oldestKey)
	}
}

// ─── Bus ──────────────────────────────────────────────────────────────────────

// Bus 内部异步事件总线。
type Bus struct {
	ch        chan *model.PointData
	deadband  *DeadbandFilter
	threshold float64

	// 原子统计计数器（8 字节对齐，ARM64 安全）
	published uint64
	dropped   uint64
	filtered  uint64

	once      sync.Once
	closeOnce sync.Once
	subs      []chan *model.PointData
	subMu     sync.RWMutex

	// 缓冲区大小（用于统计）
	bufferSize int
}

// BusConfig 总线配置
type BusConfig struct {
	// BufferSize 主通道缓冲区大小（建议 4096~16384）
	// 50k 点 / 100ms 采集周期 → 峰值 5000 条/批，推荐 ≥ 8192
	BufferSize int
	// DeadbandThreshold 死区阈值，0 = 禁用
	DeadbandThreshold float64
	// SubBufferSize 每个订阅者通道缓冲区
	SubBufferSize int
}

// DefaultBusConfig 默认配置
var DefaultBusConfig = BusConfig{
	BufferSize:        8192, // 提升至 8192，覆盖 50k 点突发
	DeadbandThreshold: 0,
	SubBufferSize:     4096,
}

// NewBus 创建总线，bufferSize 为主通道缓冲区大小。
func NewBus(bufferSize int) *Bus {
	cfg := DefaultBusConfig
	cfg.BufferSize = bufferSize
	return NewBusWithConfig(cfg)
}

// NewBusWithConfig 以完整配置创建总线。
func NewBusWithConfig(cfg BusConfig) *Bus {
	b := &Bus{
		ch:         make(chan *model.PointData, cfg.BufferSize),
		deadband:   newDeadbandFilter(),
		threshold:  cfg.DeadbandThreshold,
		bufferSize: cfg.BufferSize,
	}
	b.once.Do(func() { go b.dispatch(cfg.SubBufferSize) })
	return b
}

// Publish 向总线发布一条测点数据（非阻塞）。
//
// 调用约定：
//   - 调用方通过 model.GetPoint() 获取对象并填充后传入
//   - 一旦调用 Publish，对象所有权转移给 Bus，调用方不得再访问
//   - Bus 会在消费完毕（过滤/背压丢弃/广播完成）后自动归还 Pool
func (b *Bus) Publish(p *model.PointData) bool {
	if !b.deadband.Pass(p, b.threshold) {
		atomic.AddUint64(&b.filtered, 1)
		model.PutPoint(p)
		return false
	}

	select {
	case b.ch <- p:
		atomic.AddUint64(&b.published, 1)
		return true
	default:
		atomic.AddUint64(&b.dropped, 1)
		model.PutPoint(p)
		return false
	}
}

// Subscribe 注册订阅者，返回只读通道。每个北向导出器调用一次。
func (b *Bus) Subscribe() <-chan *model.PointData {
	b.subMu.Lock()
	ch := make(chan *model.PointData, DefaultBusConfig.SubBufferSize)
	b.subs = append(b.subs, ch)
	b.subMu.Unlock()
	return ch
}

// dispatch 内部分发协程：将主通道中的消息广播给所有订阅者。
//
// 内存优化策略：
//   - 零订阅者：直接归还 Pool
//   - 单订阅者：直接转发原始指针，完全零拷贝零分配
//   - 多订阅者（N 个）：前 N-1 个订阅者各获得一份 Pool 复制，
//     最后一个订阅者直接接收原始指针，减少 1 次 Get+Put 开销
func (b *Bus) dispatch(_ int) {
	for p := range b.ch {
		b.subMu.RLock()
		subs := b.subs // 快照，避免持锁广播
		b.subMu.RUnlock()

		n := len(subs)
		switch n {
		case 0:
			// 无消费者，直接归还
			model.PutPoint(p)

		case 1:
			// ★ 单订阅者零拷贝路径：最常见场景（仅 MQTT 或仅 Kafka）
			select {
			case subs[0] <- p:
			default:
				model.PutPoint(p) // 订阅者满，背压丢弃
			}

		default:
			// 多订阅者：前 n-1 个发复制品，最后一个发原始
			for i := 0; i < n-1; i++ {
				cp := model.GetPoint()
				p.CopyTo(cp)
				select {
				case subs[i] <- cp:
				default:
					model.PutPoint(cp)
				}
			}
			// 最后一个订阅者直接拿原始对象，省一次 Get/Put
			select {
			case subs[n-1] <- p:
			default:
				model.PutPoint(p)
			}
		}
	}

	// 总线关闭，通知所有订阅者
	b.subMu.Lock()
	for _, sub := range b.subs {
		close(sub)
	}
	b.subMu.Unlock()
}

// Close 关闭总线（不再接收新数据，dispatch 协程会自然退出）。
func (b *Bus) Close() {
	b.closeOnce.Do(func() { close(b.ch) })
}

// BusStats 总线统计信息结构体
type BusStats struct {
	// Published 成功发布的消息总数
	Published uint64 `json:"published"`
	// Dropped 因缓冲区满而丢弃的消息总数
	Dropped uint64 `json:"dropped"`
	// Filtered 因死区过滤而拦截的消息总数
	Filtered uint64 `json:"filtered"`
	// BufferSize 主通道缓冲区大小
	BufferSize int `json:"buffer_size"`
	// BufferUsed 当前缓冲区使用量
	BufferUsed int `json:"buffer_used"`
	// SubscriberCount 订阅者数量
	SubscriberCount int `json:"subscriber_count"`
	// DeadbandThreshold 死区阈值
	DeadbandThreshold float64 `json:"deadband_threshold"`
	// DeadbandEntries 死区过滤器缓存的测点数量
	DeadbandEntries int `json:"deadband_entries"`
}

// Stats 返回统计快照（兼容旧接口）。
func (b *Bus) Stats() (published, dropped, filtered uint64) {
	return atomic.LoadUint64(&b.published),
		atomic.LoadUint64(&b.dropped),
		atomic.LoadUint64(&b.filtered)
}

// StatsDetailed 返回详细的统计信息结构体。
func (b *Bus) StatsDetailed() BusStats {
	b.subMu.RLock()
	subCount := len(b.subs)
	b.subMu.RUnlock()

	b.deadband.mu.Lock()
	deadbandEntries := len(b.deadband.entries)
	b.deadband.mu.Unlock()

	return BusStats{
		Published:         atomic.LoadUint64(&b.published),
		Dropped:           atomic.LoadUint64(&b.dropped),
		Filtered:          atomic.LoadUint64(&b.filtered),
		BufferSize:        b.bufferSize,
		BufferUsed:        len(b.ch),
		SubscriberCount:   subCount,
		DeadbandThreshold: b.threshold,
		DeadbandEntries:   deadbandEntries,
	}
}

// ResetStats 重置统计计数器（用于测试或定期重置）。
func (b *Bus) ResetStats() {
	atomic.StoreUint64(&b.published, 0)
	atomic.StoreUint64(&b.dropped, 0)
	atomic.StoreUint64(&b.filtered, 0)
}
