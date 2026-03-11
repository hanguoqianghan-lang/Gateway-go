// internal/exporter/exporter.go - 北向转发接口定义
package exporter

import (
	"context"
	"time"

	"github.com/cgn/gateway/internal/model"
)

// Exporter 北向转发接口。
// 实现类：MQTTExporter、KafkaExporter 等。
type Exporter interface {
	// Run 启动转发循环，从 sub 通道消费数据，直到 ctx 取消。
	Run(ctx context.Context, sub <-chan *model.PointData)

	// Close 释放连接等资源，与 Run 返回后调用。
	Close() error

	// Name 导出器名称，用于日志。
	Name() string
}

// BatchConfig Batcher 配置
type BatchConfig struct {
	// MaxSize 批次最大条数，达到后立即发送
	MaxSize int
	// MaxLatency 最大攒包时延，超过后强制发送（即便未达 MaxSize）
	MaxLatency time.Duration
}

// DefaultBatchConfig 默认批量配置
var DefaultBatchConfig = BatchConfig{
	MaxSize:    500,
	MaxLatency: 200 * time.Millisecond,
}
