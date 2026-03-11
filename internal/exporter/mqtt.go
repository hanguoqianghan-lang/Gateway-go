// internal/exporter/mqtt.go - MQTT 北向导出器
//
// Topic 策略：
//   每批数据按 PointData.ID 中第一段（设备 ID）分组，分别发布到：
//     <TopicPrefix>/<slaveID>
//   例：TopicPrefix="gateway/data"，slaveID="plc-01"
//     → 发布到 "gateway/data/plc-01"
//   MQTTx 订阅 "gateway/data/#" 即可接收所有设备数据。
//
// 依赖：github.com/eclipse/paho.mqtt.golang
package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/cgn/gateway/internal/model"
	"go.uber.org/zap"
)

// MQTTConfig MQTT 连接配置
type MQTTConfig struct {
	Broker      string        // 例如 "tcp://127.0.0.1:1883"
	ClientID    string        // 客户端 ID
	TopicPrefix string        // 发布主题前缀，例如 "gateway/data"；完整 topic = TopicPrefix/<slaveID>
	QoS         byte          // 0 / 1 / 2
	Username    string        // 可选
	Password    string        // 可选
	ConnTimeout time.Duration
}

// DefaultMQTTConfig 默认配置（本地 broker，订阅 gateway/data/# 即可接收）
var DefaultMQTTConfig = MQTTConfig{
	Broker:      "tcp://127.0.0.1:1883",
	ClientID:    "cgn-gateway",
	TopicPrefix: "gateway/data",
	QoS:         1,
	ConnTimeout: 5 * time.Second,
}

// mqttBatch MQTT 批量消息体
type mqttBatch struct {
	Timestamp int64       `json:"ts"`   // 批次打包时间（UnixMilli）
	Points    []mqttPoint `json:"points"`
}

type mqttPoint struct {
	ID        string      `json:"id"`
	Value     interface{} `json:"v"`
	Timestamp int64       `json:"ts"` // 采集时间（UnixNano）
	Quality   uint8       `json:"q"`
}

// MQTTExporter MQTT 北向导出器
type MQTTExporter struct {
	cfg    MQTTConfig
	batch  BatchConfig
	client mqtt.Client
	logger *zap.Logger
}

// NewMQTTExporter 创建 MQTT 导出器（使用 DefaultMQTTConfig）。
func NewMQTTExporter(logger *zap.Logger, batchCfg BatchConfig) *MQTTExporter {
	return NewMQTTExporterWithConfig(logger, DefaultMQTTConfig, batchCfg)
}

// NewMQTTExporterWithConfig 以完整配置创建 MQTT 导出器。
func NewMQTTExporterWithConfig(logger *zap.Logger, cfg MQTTConfig, batchCfg BatchConfig) *MQTTExporter {
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetConnectTimeout(cfg.ConnTimeout).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(30 * time.Second).
		SetOnConnectHandler(func(_ mqtt.Client) {
			logger.Info("MQTT 已连接/重连", zap.String("broker", cfg.Broker))
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			logger.Warn("MQTT 连接断开", zap.Error(err))
		})

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username).SetPassword(cfg.Password)
	}

	return &MQTTExporter{
		cfg:    cfg,
		batch:  batchCfg,
		client: mqtt.NewClient(opts),
		logger: logger,
	}
}

// Name 实现 Exporter 接口。
func (e *MQTTExporter) Name() string { return "mqtt" }

// Run 实现 Exporter 接口，连接 broker 后启动 Batcher 消费循环。
func (e *MQTTExporter) Run(ctx context.Context, sub <-chan *model.PointData) {
	e.logger.Info("MQTT 开始连接", zap.String("broker", e.cfg.Broker))

	if token := e.client.Connect(); token.WaitTimeout(e.cfg.ConnTimeout) && token.Error() != nil {
		e.logger.Warn("MQTT 初始连接失败，将在后台自动重试", zap.Error(token.Error()))
	} else {
		e.logger.Info("MQTT 连接成功", zap.String("broker", e.cfg.Broker))
	}

	batcher := NewBatcher(e.batch, e.send)
	batcher.Run(ctx, sub)
}

// send 由 Batcher 回调，将一批测点按设备分组后分别发布到各自 topic。
//
// Topic 格式：<TopicPrefix>/<slaveID>
// slaveID 从 PointData.ID 的第一段提取（"<slaveID>/modbus/<name>"）。
func (e *MQTTExporter) send(batch []*model.PointData) error {
	e.logger.Debug("MQTT send 被调用", zap.Int("batch_size", len(batch)), zap.Bool("connected", e.client.IsConnected()))

	if !e.client.IsConnected() {
		e.logger.Warn("MQTT 未连接，本批数据丢弃", zap.Int("count", len(batch)))
		return fmt.Errorf("MQTT 未连接，丢弃 %d 条", len(batch))
	}

	// 按 slaveID 分组
	groups := make(map[string][]mqttPoint, 4)
	for _, p := range batch {
		slaveID := slaveIDFromPointID(p.ID)
		groups[slaveID] = append(groups[slaveID], mqttPoint{
			ID:        p.ID,
			Value:     p.Value,
			Timestamp: p.Timestamp,
			Quality:   p.Quality,
		})
	}

	batchTS := time.Now().UnixMilli()
	var lastErr error
	for slaveID, points := range groups {
		topic := e.cfg.TopicPrefix + "/" + slaveID

		payload, err := json.Marshal(mqttBatch{
			Timestamp: batchTS,
			Points:    points,
		})
		if err != nil {
			e.logger.Error("JSON 序列化失败", zap.String("slave", slaveID), zap.Error(err))
			lastErr = err
			continue
		}

		token := e.client.Publish(topic, e.cfg.QoS, false, payload)
		if !token.WaitTimeout(2 * time.Second) {
			e.logger.Error("MQTT Publish 超时", zap.String("topic", topic))
			lastErr = fmt.Errorf("publish timeout: topic=%s", topic)
			continue
		}
		if token.Error() != nil {
			e.logger.Error("MQTT Publish 失败", zap.String("topic", topic), zap.Error(token.Error()))
			lastErr = token.Error()
			continue
		}

		// 输出完整的 payload 用于调试
		e.logger.Info("MQTT 发送成功",
			zap.String("topic", topic),
			zap.Int("points", len(points)),
			zap.Int("bytes", len(payload)),
			zap.String("payload", string(payload)),
		)
	}
	return lastErr
}

// slaveIDFromPointID 从 "<slaveID>/modbus/<name>" 格式中提取第一段作为 slaveID。
// 若格式不匹配，直接返回原始 ID，保证 topic 合法。
func slaveIDFromPointID(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i]
	}
	return id
}

// Close 实现 Exporter 接口，优雅断开 MQTT 连接。
func (e *MQTTExporter) Close() error {
	e.client.Disconnect(500)
	e.logger.Info("MQTT 已断开")
	return nil
}
