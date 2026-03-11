// internal/exporter/kafka.go - Kafka 北向导出器（框架，需引入 kafka 客户端）
//
// 推荐依赖：github.com/segmentio/kafka-go
// 使用前请执行：go get github.com/segmentio/kafka-go
package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cgn/gateway/internal/model"
	"go.uber.org/zap"
)

// KafkaConfig Kafka 连接配置
type KafkaConfig struct {
	Brokers []string      // 例如 ["192.168.1.100:9092"]
	Topic   string        // 目标 topic
	Async   bool          // true=异步写入（高吞吐）/ false=同步（低丢失）
	Timeout time.Duration
}

// DefaultKafkaConfig 默认配置
var DefaultKafkaConfig = KafkaConfig{
	Brokers: []string{"127.0.0.1:9092"},
	Topic:   "iot.gateway.data",
	Async:   true,
	Timeout: 5 * time.Second,
}

// KafkaMessage Kafka 消息体
type KafkaMessage struct {
	Timestamp int64       `json:"ts"`
	Points    []mqttPoint `json:"points"` // 复用 mqttPoint 结构
}

// KafkaExporter Kafka 北向导出器（骨架实现）
// 实际 writer 字段类型请替换为 *kafka.Writer（segmentio/kafka-go）。
type KafkaExporter struct {
	cfg    KafkaConfig
	batch  BatchConfig
	logger *zap.Logger
	// writer *kafka.Writer  // 取消注释并 import kafka-go 后使用
}

// NewKafkaExporter 创建 Kafka 导出器。
func NewKafkaExporter(logger *zap.Logger, cfg KafkaConfig, batchCfg BatchConfig) *KafkaExporter {
	exp := &KafkaExporter{
		cfg:    cfg,
		batch:  batchCfg,
		logger: logger,
	}
	// ---- 取消注释以启用真实写入 ----
	// exp.writer = kafka.NewWriter(kafka.WriterConfig{
	// 	Brokers:  cfg.Brokers,
	// 	Topic:    cfg.Topic,
	// 	Async:    cfg.Async,
	// 	Balancer: &kafka.LeastBytes{},
	// })
	return exp
}

// Name 实现 Exporter 接口。
func (e *KafkaExporter) Name() string { return "kafka" }

// Run 实现 Exporter 接口。
func (e *KafkaExporter) Run(ctx context.Context, sub <-chan *model.PointData) {
	e.logger.Info("Kafka 导出器已启动", zap.Strings("brokers", e.cfg.Brokers))
	batcher := NewBatcher(e.batch, e.send)
	batcher.Run(ctx, sub)
}

// send 由 Batcher 回调，将一批测点写入 Kafka。
func (e *KafkaExporter) send(batch []*model.PointData) error {
	points := make([]mqttPoint, 0, len(batch))
	for _, p := range batch {
		points = append(points, mqttPoint{
			ID:        p.ID,
			Value:     p.Value,
			Timestamp: p.Timestamp,
			Quality:   p.Quality,
		})
	}

	payload, err := json.Marshal(KafkaMessage{
		Timestamp: time.Now().UnixMilli(),
		Points:    points,
	})
	if err != nil {
		return fmt.Errorf("Kafka JSON 序列化失败: %w", err)
	}

	// ---- 取消注释以启用真实写入 ----
	// msg := kafka.Message{Value: payload}
	// writeCtx, cancel := context.WithTimeout(context.Background(), e.cfg.Timeout)
	// defer cancel()
	// if err := e.writer.WriteMessages(writeCtx, msg); err != nil {
	// 	return fmt.Errorf("Kafka WriteMessages 失败: %w", err)
	// }

	e.logger.Debug("Kafka 批量写入（模拟）",
		zap.Int("count", len(batch)),
		zap.Int("bytes", len(payload)),
	)
	return nil
}

// Close 实现 Exporter 接口。
func (e *KafkaExporter) Close() error {
	// if e.writer != nil { return e.writer.Close() }
	e.logger.Info("Kafka 导出器已关闭")
	return nil
}
