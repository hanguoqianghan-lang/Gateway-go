// internal/exporter/batcher.go - 通用批量打包器
//
// 策略："攒够 N 条" OR "超过 T 毫秒" → 立即发送，避免频繁网络 IO。
//
// 内存优化：
//   - batchPool 复用 []*PointData 切片本身，消除每次 flush 后 make([]...) 的堆分配。
//   - flush 完成后将切片 reset（len=0）并归还 pool，下次直接复用底层数组。
//   - 每次 flush 后 PointData 对象由 model.PutPoint 归还 pointPool，全程零新增 heap 对象。
//
// 高频场景内存模型（50k 点/s，batchSize=500，200ms flush）：
//   - 每 200ms 最多触发 20 次 flush（50k/500/5Hz）
//   - batchPool 稳态持有 1 个切片（cap=500），内存 ~4KB，完全在 L1 cache 内
package exporter

import (
	"context"
	"sync"
	"time"

	"github.com/cgn/gateway/internal/model"
)

// BatchHandler 批次处理回调，由具体导出器（MQTT/Kafka）实现。
// 约定：handler 必须同步执行（不能持有 batch 引用异步处理），
// 返回后 Batcher 会立即归还 batch 中的所有 PointData 到 Pool。
type BatchHandler func(batch []*model.PointData) error

// batchSlicePool 复用 []*model.PointData 切片，避免每次 flush 重新 make。
// Pool 中存放的是 *[]*model.PointData（指针），方便 reset 后归还。
var batchSlicePool sync.Pool // New 由 Batcher 初始化时按 MaxSize 懒建

// Batcher 通用批量打包器。
type Batcher struct {
	cfg       BatchConfig
	handler   BatchHandler
	slicePool *sync.Pool // 每个 Batcher 独享，cap 与 MaxSize 一致
}

// NewBatcher 创建 Batcher。
func NewBatcher(cfg BatchConfig, handler BatchHandler) *Batcher {
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = DefaultBatchConfig.MaxSize
	}
	if cfg.MaxLatency <= 0 {
		cfg.MaxLatency = DefaultBatchConfig.MaxLatency
	}
	maxSize := cfg.MaxSize // 闭包捕获，避免逃逸
	return &Batcher{
		cfg:     cfg,
		handler: handler,
		slicePool: &sync.Pool{
			New: func() interface{} {
				// 预分配足够容量，避免 append 触发扩容
				s := make([]*model.PointData, 0, maxSize)
				return &s
			},
		},
	}
}

// getBatch 从 Pool 取出一个空切片。
func (b *Batcher) getBatch() []*model.PointData {
	sp := b.slicePool.Get().(*[]*model.PointData)
	return (*sp)[:0] // 容量保留，长度归零
}

// putBatch 将切片归还 Pool（清空元素引用，防止 PointData 无法被 GC 回收）。
func (b *Batcher) putBatch(batch []*model.PointData) {
	// 清空切片中的指针引用（不清空的话底层数组仍持有 *PointData 引用）
	for i := range batch {
		batch[i] = nil
	}
	batch = batch[:0]
	b.slicePool.Put(&batch)
}

// Run 从 sub 通道消费数据，满足批次条件时调用 handler。
// 阻塞直到 ctx 取消或 sub 关闭。
func (b *Batcher) Run(ctx context.Context, sub <-chan *model.PointData) {
	ticker := time.NewTicker(b.cfg.MaxLatency)
	defer ticker.Stop()

	batch := b.getBatch()

	// flush 将当前 batch 发送出去，然后申请新 batch。
	// PointData 对象归还顺序：handler 返回后立即 PutPoint，与发送结果无关
	// （发送失败也要归还，避免泄漏）。
	flush := func() {
		if len(batch) == 0 {
			return
		}

		// 取出当前 batch，立即申请下一个（减少等待 handler 期间的延迟）
		toSend := batch
		batch = b.getBatch()

		// 调用 handler（同步），无论成功与否都归还 PointData
		b.handler(toSend) //nolint:errcheck // 错误由 handler 内部记录日志

		// 归还 PointData 对象到 pointPool
		for _, p := range toSend {
			model.PutPoint(p)
		}
		// 归还切片本身到 slicePool
		b.putBatch(toSend)
	}

	for {
		select {
		case <-ctx.Done():
			flush() // 退出前尽力发送剩余数据
			b.putBatch(batch)
			return

		case p, ok := <-sub:
			if !ok {
				flush()
				b.putBatch(batch)
				return
			}
			batch = append(batch, p)
			if len(batch) >= b.cfg.MaxSize {
				flush()
				ticker.Reset(b.cfg.MaxLatency) // 重置计时，避免刚 flush 就再触发
			}

		case <-ticker.C:
			flush()
		}
	}
}
