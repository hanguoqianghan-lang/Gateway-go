// internal/exporter/kafka.go - Kafka 北向导出器
//
// 依赖：github.com/segmentio/kafka-go
package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gateway/gateway/internal/model"
	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// KafkaConfig Kafka 连接配置
type KafkaConfig struct {
	Brokers       []string      // 例如 ["192.168.1.100:9092"]
	Topic         string        // 目标 topic
	Async         bool          // true=异步写入（高吞吐）/ false=同步（低丢失）
	Timeout       time.Duration // 写入超时
	BatchSize     int           // 批量大小
	BatchTimeout  time.Duration // 批量超时
	RequiredAcks  int           // 确认级别：0=不确认，1=leader确认，-1=all确认
	Compression   string        // 压缩类型：none, gzip, snappy, lz4, zstd
}

// DefaultKafkaConfig 默认配置
var DefaultKafkaConfig = KafkaConfig{
	Brokers:       []string{"127.0.0.1:9092"},
	Topic:         "iot.gateway.data",
	Async:         true,
	Timeout:       5 * time.Second,
	BatchSize:     100,
	BatchTimeout:  10 * time.Millisecond,
	RequiredAcks:  1,
	Compression:   "none",
}

// KafkaMessage Kafka 消息体
type KafkaMessage struct {
	Timestamp int64       `json:"ts"`
	Points    []mqttPoint `json:"points"` // 复用 mqttPoint 结构
}

// KafkaExporter Kafka 北向导出器
type KafkaExporter struct {
	cfg    KafkaConfig
	batch  BatchConfig
	logger *zap.Logger
	writer *kafka.Writer
}

// NewKafkaExporter 创建 Kafka 导出器
func NewKafkaExporter(logger *zap.Logger, cfg KafkaConfig, batchCfg BatchConfig) *KafkaExporter {
	// 设置默认值
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultKafkaConfig.BatchSize
	}
	if cfg.BatchTimeout == 0 {
		cfg.BatchTimeout = DefaultKafkaConfig.BatchTimeout
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultKafkaConfig.Timeout
	}
	if cfg.RequiredAcks == 0 {
		cfg.RequiredAcks = DefaultKafkaConfig.RequiredAcks
	}
	if cfg.Compression == "" {
		cfg.Compression = DefaultKafkaConfig.Compression
	}

	// 创建 Kafka Writer
	writer := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        cfg.Topic,
		Balancer:     &kafka.LeastBytes{}, // 负载均衡策略
		Async:        cfg.Async,
		BatchSize:    cfg.BatchSize,
		BatchTimeout: cfg.BatchTimeout,
		WriteTimeout: cfg.Timeout,
		RequiredAcks: kafka.RequiredAcks(cfg.RequiredAcks),
		Compression:  getCompression(cfg.Compression),
	}

	logger.Info("Kafka Writer 初始化成功",
		zap.Strings("brokers", cfg.Brokers),
		zap.String("topic", cfg.Topic),
		zap.Bool("async", cfg.Async))

	return &KafkaExporter{
		cfg:    cfg,
		batch:  batchCfg,
		logger: logger,
		writer: writer,
	}
}

// getCompression 获取压缩类型
func getCompression(compression string) kafka.Compression {
	switch strings.ToLower(compression) {
	case "gzip":
		return kafka.Gzip
	case "snappy":
		return kafka.Snappy
	case "lz4":
		return kafka.Lz4
	case "zstd":
		return kafka.Zstd
	default:
		return kafka.Compression(0) // none
	}
}

// Name 实现 Exporter 接口
func (e *KafkaExporter) Name() string { return "kafka" }

// Run 实现 Exporter 接口
func (e *KafkaExporter) Run(ctx context.Context, sub <-chan *model.PointData) {
	e.logger.Info("Kafka 导出器已启动",
		zap.Strings("brokers", e.cfg.Brokers),
		zap.String("topic", e.cfg.Topic))
	batcher := NewBatcher(e.batch, e.send)
	batcher.Run(ctx, sub)
}

// send 由 Batcher 回调，将一批测点写入 Kafka
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

	// 创建 Kafka 消息
	// 使用时间戳作为 Key，保证消息有序性
	msg := kafka.Message{
		Key:   []byte(time.Now().Format(time.RFC3339Nano)),
		Value: payload,
		Time:  time.Now(),
	}

	// 写入 Kafka
	writeCtx, cancel := context.WithTimeout(context.Background(), e.cfg.Timeout)
	defer cancel()

	if err := e.writer.WriteMessages(writeCtx, msg); err != nil {
		e.logger.Error("Kafka 写入失败",
			zap.Error(err),
			zap.Int("points", len(batch)),
			zap.Int("bytes", len(payload)))
		return fmt.Errorf("Kafka WriteMessages 失败: %w", err)
	}

	e.logger.Info("Kafka 写入成功",
		zap.String("topic", e.cfg.Topic),
		zap.Int("points", len(batch)),
		zap.Int("bytes", len(payload)))

	return nil
}

// Close 实现 Exporter 接口
func (e *KafkaExporter) Close() error {
	if e.writer != nil {
		if err := e.writer.Close(); err != nil {
			e.logger.Error("Kafka Writer 关闭失败", zap.Error(err))
			return err
		}
	}
	e.logger.Info("Kafka 导出器已关闭")
	return nil
}
