// internal/exporter/kafka.go - Kafka 北向导出器
//
// 依赖：github.com/segmentio/kafka-go
package exporter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gateway/gateway/config"
	"github.com/gateway/gateway/internal/model"
	jsoniter "github.com/json-iterator/go"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"
	"go.uber.org/zap"
)

// 使用 jsoniter 提升序列化性能
var fastJson = jsoniter.ConfigFastest

// KafkaExporter Kafka 北向导出器
type KafkaExporter struct {
	cfg    config.KafkaExporterConfig
	logger *zap.Logger
	writer *kafka.Writer
}

// NewKafkaExporter 创建 Kafka 导出器
func NewKafkaExporter(logger *zap.Logger, cfg config.KafkaExporterConfig) (*KafkaExporter, error) {
	// 设置默认值
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	if cfg.BatchTimeout == 0 {
		cfg.BatchTimeout = 10 * time.Millisecond
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Acks == 0 {
		cfg.Acks = 1
	}

	// 1. 构建 Transport 以支持 SASL/TLS
	transport := &kafka.Transport{}

	// 配置 TLS
	if cfg.TLS != nil && cfg.TLS.Enabled {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: cfg.TLS.SkipVerify,
		}

		// 加载客户端证书
		if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("加载 Kafka TLS 证书失败: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}

		// 加载 CA 证书
		if cfg.TLS.CAFile != "" {
			caCert, err := os.ReadFile(cfg.TLS.CAFile)
			if err != nil {
				return nil, fmt.Errorf("加载 Kafka CA 证书失败: %w", err)
			}
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(caCert)
			tlsConfig.RootCAs = caCertPool
		}

		transport.TLS = tlsConfig
	}

	// 配置 SASL
	if cfg.SASL != nil && cfg.SASL.Enabled {
		var mechanism sasl.Mechanism
		var err error

		switch strings.ToUpper(cfg.SASL.Mechanism) {
		case "PLAIN":
			mechanism = plain.Mechanism{
				Username: cfg.SASL.User,
				Password: cfg.SASL.Password,
			}
		case "SCRAM-SHA-256":
			mechanism, err = scram.Mechanism(scram.SHA256, cfg.SASL.User, cfg.SASL.Password)
		case "SCRAM-SHA-512":
			mechanism, err = scram.Mechanism(scram.SHA512, cfg.SASL.User, cfg.SASL.Password)
		default:
			return nil, fmt.Errorf("不支持的 Kafka SASL 机制: %s", cfg.SASL.Mechanism)
		}

		if err != nil {
			return nil, fmt.Errorf("初始化 Kafka SASL 失败: %w", err)
		}
		transport.SASL = mechanism
	}

	// 2. 创建 Kafka Writer
	writer := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        cfg.Topic,
		Balancer:     &kafka.Hash{}, // 基于 Key 进行哈希路由，确保同个测点数据有序进入相同 Partition
		Async:        cfg.Async,
		BatchSize:    cfg.BatchSize,
		BatchTimeout: cfg.BatchTimeout,
		WriteTimeout: cfg.Timeout,
		RequiredAcks: kafka.RequiredAcks(cfg.Acks),
		Compression:  getCompression(cfg.Compression),
		Transport:    transport,
		Completion: func(messages []kafka.Message, err error) {
			// 3. Async 模式下的 Error 捕获回调
			if err != nil {
				logger.Error("Kafka 异步写入失败",
					zap.Error(err),
					zap.Int("failed_messages", len(messages)),
					zap.String("topic", cfg.Topic))
				// TODO: 此处可对接 StorageConfig 实现明确失败消息的 sqlite 暂存补发机制
			}
            // 写入成功时的 debug 返回
			if logger.Core().Enabled(zap.DebugLevel) {
				logger.Debug("Kafka 异步写入批次完成", zap.Int("messages", len(messages)))
			}
		},
	}

	logger.Info("Kafka Writer 初始化成功",
		zap.Strings("brokers", cfg.Brokers),
		zap.String("topic", cfg.Topic),
		zap.Bool("async", cfg.Async),
		zap.Bool("tls_enabled", cfg.TLS != nil && cfg.TLS.Enabled),
		zap.Bool("sasl_enabled", cfg.SASL != nil && cfg.SASL.Enabled))

	return &KafkaExporter{
		cfg:    cfg,
		logger: logger,
		writer: writer,
	}, nil
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

// Run 实现 Exporter 接口，直接消费 sub，不再依赖冗余的外层 Batcher
func (e *KafkaExporter) Run(ctx context.Context, sub <-chan *model.PointData) {
	e.logger.Info("Kafka 导出器已启动",
		zap.Strings("brokers", e.cfg.Brokers),
		zap.String("topic", e.cfg.Topic))

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("Kafka 导出器收到退出信号")
			return
		case p, ok := <-sub:
			if !ok {
				return
			}
			e.sendPoint(ctx, p)
		}
	}
}

// sendPoint 将单个点位打包并发送给 Kafka Writer（复用底层微批机制）
func (e *KafkaExporter) sendPoint(ctx context.Context, p *model.PointData) {
	// 构建单条输出点位
	point := mqttPoint{
		ID:        p.ID,
		Value:     p.Value,
		Timestamp: p.Timestamp,
		Quality:   p.Quality,
	}

	// 使用高性能 JIT JSON 库代替 encoding/json
	payload, err := fastJson.Marshal(point)

	if err != nil {
		e.logger.Error("Kafka JSON 序列化失败", zap.Error(err), zap.String("point_id", point.ID))
        model.PutPoint(p) // 报错也要归还点位对象
		return
	}
    model.PutPoint(p) // 序列化完毕后尽早归还内存池，防止对象逃逸累计

	// 构造 Message：将点位 ID 作为 Key，严格保障同一测点路由至同一个 Partition
	msg := kafka.Message{
		Key:   []byte(point.ID),
		Value: payload,
		Time:  time.Now(),
	}

	// 在 Async 模式下，WriteMessages 基本上是非阻塞直接入队的，所以用入参的 ctx 即可
	err = e.writer.WriteMessages(ctx, msg)
	if err != nil {
		e.logger.Error("Kafka 提交消息到发送队列失败",
			zap.Error(err),
			zap.String("point_id", point.ID))
	}
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
