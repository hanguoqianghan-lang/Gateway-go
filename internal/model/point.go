// internal/model/point.go - 统一测点数据模型
package model

import "sync"

// Quality 测点数据质量码（参考 IEC 61968 / OPC-UA）
const (
	QualityGood         uint8 = 0x00 // 数据良好
	QualityUncertain    uint8 = 0x40 // 数据不确定
	QualityBad          uint8 = 0x80 // 数据无效
	QualityNotConnected   uint8 = 0xC0 // 设备未连接
		QualityCommFail       uint8 = 0xC1 // 通信失败
		QualityTimeout        uint8 = 0xC2 // 通信超时
		QualityConfigError    uint8 = 0xC3 // 配置错误
		QualityDeviceError    uint8 = 0xC4 // 设备错误
		QualityOffline        uint8 = 0xC5 // 离线数据（从缓存恢复）
		QualityLastKnownValid uint8 = 0xC8 // 最后已知有效值
)

// PointData 统一测点数据结构。
//
// 生命周期规则（必须严格遵守，否则 Pool 复用会产生数据污染）：
//
//	获取：p := model.GetPoint()
//	填充：p.ID = ...; p.Value = ...; p.Timestamp = ...; p.Quality = ...
//	发布：bus.Publish(p)      ← 所有权转移给 Bus，调用方不得再访问 p
//	归还：由 Bus / Batcher 在消费完毕后调用 model.PutPoint(p)
//
// Value 支持的底层类型：bool / int64 / float64 / string / []byte
// 驱动层应避免将可变 slice 赋给 Value（会产生逃逸且难以复用）。
type PointData struct {
	ID        string      // 全局唯一测点标识，格式：<设备ID>/<协议>/<地址>
	Value     interface{} // 原始测量值
	Timestamp int64       // Unix 纳秒时间戳（time.Now().UnixNano()）
	Quality   uint8       // 数据质量码
}

// Reset 将 PointData 所有字段清零，切断对外部对象的引用，
// 防止旧数据在 Pool 中保留引用导致 GC 无法回收。
// PutPoint 内部会调用此方法，外部无需手动调用。
func (p *PointData) Reset() {
	p.ID = ""
	p.Value = nil // 断开 interface 内部指针，允许 GC 回收 Value 指向的对象
	p.Timestamp = 0
	p.Quality = 0
}

// CopyTo 将自身浅拷贝到 dst。
// 用于 Bus.dispatch 广播：从 Pool 取出 dst 后调用本方法填充，
// 比 *dst = *src 语义更清晰，且未来可在此处加深拷贝逻辑。
func (p *PointData) CopyTo(dst *PointData) {
	dst.ID = p.ID
	dst.Value = p.Value
	dst.Timestamp = p.Timestamp
	dst.Quality = p.Quality
}

// ─── sync.Pool ────────────────────────────────────────────────────────────────
//
// 设计目标：5 万测点高频采集场景下，PointData 的分配/释放开销几乎为零。
//
// 关键数据（以 50000 点/s、每点 1 次 GC 分配对比）：
//   - 无 Pool：~50000 次 heap alloc/s，GC STW 随内存压力上升
//   - 有 Pool：稳态下约 0 次新分配，GC 压力趋近于 0
//
// 注意：sync.Pool 在每次 GC 时会清空未被引用的对象，
// 因此 Pool 只能减少分配频率，不能完全消除（GC 后第一批仍需新建）。
var pointPool = sync.Pool{
	New: func() interface{} {
		return &PointData{}
	},
}

// GetPoint 从 Pool 中取出一个已清零的 PointData。
// 调用方必须填充所有需要的字段后再使用。
func GetPoint() *PointData {
	return pointPool.Get().(*PointData)
}

// PutPoint 将 PointData 归还 Pool，供下次 GetPoint 复用。
//
// 使用约定：
//   - 调用后调用方不得再读写 p 的任何字段
//   - 不要对同一个 p 调用两次 PutPoint（double-free）
//   - 不要归还栈上的 PointData 地址（只归还通过 GetPoint 获取的堆对象）
func PutPoint(p *PointData) {
	p.Reset() // 清零所有字段，切断外部引用
	pointPool.Put(p)
}
