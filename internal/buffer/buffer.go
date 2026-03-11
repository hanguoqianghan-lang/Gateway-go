// internal/buffer/buffer.go - 离线缓存模块
//
// 支持三种存储类型：
// 1. memory: 纯内存缓存，适合小规模数据
// 2. sqlite: SQLite数据库，适合中等规模数据
// 3. leveldb: LevelDB键值存储，适合大规模数据
package buffer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gateway/gateway/internal/model"
	"go.uber.org/zap"
)

// Buffer 离线缓存接口
type Buffer interface {
	// Store 存储测点数据
	Store(point *model.PointData) error
	// BatchStore 批量存储测点数据
	BatchStore(points []*model.PointData) error
	// Retrieve 获取并删除缓存数据
	Retrieve(limit int) ([]*model.PointData, error)
	// Count 获取缓存数据数量
	Count() (int64, error)
	// Clear 清空缓存
	Clear() error
	// Close 关闭缓存
	Close() error
}

// Config 缓存配置
type Config struct {
	Type         string        // memory, sqlite, leveldb
	Path         string        // 存储路径
	MaxMemoryMB  int           // 最大内存大小（MB）
	FlushInterval time.Duration // 刷盘间隔
	RetryInterval time.Duration // 重试间隔
}

// memoryBuffer 内存缓存实现
type memoryBuffer struct {
	items  []*model.PointData
	mu     sync.RWMutex
	logger *zap.Logger
	maxSize int
}

// NewMemoryBuffer 创建内存缓存
func NewMemoryBuffer(maxSizeMB int, logger *zap.Logger) Buffer {
	maxSize := maxSizeMB * 1024 * 1024 / 100 // 估算每个PointData约100字节
	return &memoryBuffer{
		items:  make([]*model.PointData, 0, maxSize),
		logger: logger,
		maxSize: maxSize,
	}
}

func (b *memoryBuffer) Store(point *model.PointData) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 复制数据避免外部修改
	p := &model.PointData{
		ID:        point.ID,
		Value:     point.Value,
		Timestamp: point.Timestamp,
		Quality:   point.Quality,
	}

	b.items = append(b.items, p)

	// 检查是否超过最大容量
	if len(b.items) > b.maxSize {
		// 删除最旧的数据
		b.items = b.items[1:]
		b.logger.Warn("内存缓存已满，删除最旧数据",
			zap.Int("current_size", len(b.items)),
			zap.Int("max_size", b.maxSize),
		)
	}

	return nil
}

func (b *memoryBuffer) BatchStore(points []*model.PointData) error {
	for _, p := range points {
		if err := b.Store(p); err != nil {
			return err
		}
	}
	return nil
}

func (b *memoryBuffer) Retrieve(limit int) ([]*model.PointData, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.items) == 0 {
		return nil, nil
	}

	// 获取最旧的数据
	var result []*model.PointData
	if limit > 0 && limit < len(b.items) {
		result = b.items[:limit]
		b.items = b.items[limit:]
	} else {
		result = b.items
		b.items = b.items[:0]
	}

	return result, nil
}

func (b *memoryBuffer) Count() (int64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return int64(len(b.items)), nil
}

func (b *memoryBuffer) Clear() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = b.items[:0]
	return nil
}

func (b *memoryBuffer) Close() error {
	return b.Clear()
}

// Manager 缓存管理器
type Manager struct {
	buffer Buffer
	cfg    Config
	logger *zap.Logger

	// 统计
	storeCount   int64
	retrieveCount int64
}

// NewManager 创建缓存管理器
func NewManager(cfg Config, logger *zap.Logger) (*Manager, error) {
	var buf Buffer
	var err error

	switch cfg.Type {
	case "memory":
		buf = NewMemoryBuffer(cfg.MaxMemoryMB, logger)
		logger.Info("使用内存缓存",
			zap.Int("max_size_mb", cfg.MaxMemoryMB),
		)
	case "sqlite":
		// TODO: 实现SQLite缓存
		err = fmt.Errorf("SQLite缓存暂未实现")
	case "leveldb":
		// TODO: 实现LevelDB缓存
		err = fmt.Errorf("LevelDB缓存暂未实现")
	default:
		err = fmt.Errorf("不支持的缓存类型: %s", cfg.Type)
	}

	if err != nil {
		return nil, err
	}

	return &Manager{
		buffer: buf,
		cfg:    cfg,
		logger: logger,
	}, nil
}

// Store 存储测点数据
func (m *Manager) Store(point *model.PointData) error {
	if err := m.buffer.Store(point); err != nil {
		return err
	}
	m.storeCount++
	return nil
}

// BatchStore 批量存储测点数据
func (m *Manager) BatchStore(points []*model.PointData) error {
	if err := m.buffer.BatchStore(points); err != nil {
		return err
	}
	m.storeCount += int64(len(points))
	return nil
}

// Retrieve 获取并删除缓存数据
func (m *Manager) Retrieve(limit int) ([]*model.PointData, error) {
	points, err := m.buffer.Retrieve(limit)
	if err != nil {
		return nil, err
	}
	if len(points) > 0 {
		m.retrieveCount++
	}
	return points, nil
}

// Count 获取缓存数据数量
func (m *Manager) Count() (int64, error) {
	return m.buffer.Count()
}

// Clear 清空缓存
func (m *Manager) Clear() error {
	return m.buffer.Clear()
}

// Close 关闭缓存
func (m *Manager) Close() error {
	return m.buffer.Close()
}

// StartRetryLoop 启动重试循环
func (m *Manager) StartRetryLoop(ctx context.Context, sendFunc func([]*model.PointData) error) {
	if m.cfg.RetryInterval == 0 {
		m.logger.Info("重试间隔未配置，跳过重试循环")
		return
	}

	ticker := time.NewTicker(m.cfg.RetryInterval)
	defer ticker.Stop()

	m.logger.Info("启动缓存重试循环",
		zap.Duration("interval", m.cfg.RetryInterval),
	)

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("缓存重试循环退出")
			return
		case <-ticker.C:
			m.retrySend(sendFunc)
		}
	}
}

// retrySend 重试发送缓存数据
func (m *Manager) retrySend(sendFunc func([]*model.PointData) error) {
	count, err := m.Count()
	if err != nil {
		m.logger.Error("获取缓存数量失败", zap.Error(err))
		return
	}

	if count == 0 {
		return
	}

	m.logger.Info("开始重试发送缓存数据", zap.Int64("count", count))

	// 每次最多发送1000条
	limit := 1000
	for {
		points, err := m.Retrieve(limit)
		if err != nil {
			m.logger.Error("获取缓存数据失败", zap.Error(err))
			return
		}

		if len(points) == 0 {
			break
		}

		// 设置质量码为离线数据
		for _, p := range points {
			p.Quality = model.QualityOffline
		}

		if err := sendFunc(points); err != nil {
			// 发送失败，重新存回缓存
			m.logger.Warn("重试发送失败，数据重新存入缓存",
				zap.Error(err),
				zap.Int("points", len(points)),
			)
			if err := m.BatchStore(points); err != nil {
				m.logger.Error("重新存入缓存失败", zap.Error(err))
			}
			return
		}

		m.logger.Info("重试发送成功",
			zap.Int("points", len(points)),
			zap.Int64("remaining", count-int64(len(points))),
		)
	}
}

// Stats 获取统计信息
func (m *Manager) Stats() (int64, int64, int64) {
	count, _ := m.Count()
	return m.storeCount, m.retrieveCount, count
}
