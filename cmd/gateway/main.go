// cmd/gateway/main.go - 工业物联网网关程序入口
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gateway/gateway/config"
	cfgloader "github.com/gateway/gateway/internal/config"
	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/driver"
	// 导入驱动包以触发 init() 注册
	_ "github.com/gateway/gateway/internal/driver/iec104"
	_ "github.com/gateway/gateway/internal/driver/modbus"
	"github.com/gateway/gateway/internal/exporter"
	"go.uber.org/zap"
)

func main() {
	// ── 命令行参数 ────────────────────────────────────────────────
	configPath := flag.String("config", "./config/config.yaml", "配置文件路径")
	flag.Parse()

	// ── 日志配置 ─────────────────────────────────────────────────
	// 先创建一个临时的logger用于加载配置
	tempLogger, _ := zap.NewDevelopment()
	defer tempLogger.Sync()

	tempLogger.Info("网关启动", zap.String("config", *configPath))

	// ── 加载配置文件 ───────────────────────────────────────────────
	configLoader := cfgloader.NewLoader(*configPath, tempLogger)
	cfg, err := configLoader.Load()
	if err != nil {
		tempLogger.Fatal("加载配置文件失败", zap.Error(err))
	}

	// 根据配置创建正式的logger
	logger := createLogger(cfg)
	defer logger.Sync()

	logger.Info("配置加载成功",
		zap.String("gateway_name", cfg.Gateway.Name),
		zap.String("version", cfg.Gateway.Version),
		zap.Int("drivers", len(cfg.Drivers)),
	)

	// 打印已注册的驱动类型
	registeredTypes := driver.GetRegisteredTypes()
	logger.Info("已注册的驱动类型",
		zap.Strings("types", registeredTypes),
	)

	// ── 根 Context（平滑退出）────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── 内部总线 ──────────────────────────────────────────────────
	bus := broker.NewBusWithConfig(broker.BusConfig{
		BufferSize:        cfg.Bus.BufferSize,
		DeadbandThreshold: cfg.Bus.DeadbandThreshold,
		SubBufferSize:     4096,
	})

	// ── 驱动管理器（使用工厂模式）─────────────────────────────────
	drvMgr := driver.NewDriverManager(logger)

	// 初始化所有驱动
	logger.Info("开始初始化驱动...")
	if err := drvMgr.InitializeDrivers(ctx, cfg.Drivers); err != nil {
		logger.Error("驱动初始化过程中出现错误", zap.Error(err))
	}

	// 检查是否有驱动成功初始化
	driverCount := drvMgr.GetDriverCount()
	logger.Info("驱动初始化完成", zap.Int("count", driverCount))
	if driverCount == 0 {
		logger.Warn("没有驱动成功初始化，请检查配置")
	}

	// ── 启动所有驱动 ───────────────────────────────────────────────
	// 故障隔离：单个驱动启动失败不影响其他驱动
	logger.Info("开始启动所有驱动...")
	startResult := drvMgr.StartAll(ctx, bus)

	// 记录启动失败的驱动
	if len(startResult.Failed) > 0 {
		logger.Warn("部分驱动启动失败，将在后台重试",
			zap.Int("total", startResult.Total),
			zap.Int("started", startResult.Started),
			zap.Int("failed", len(startResult.Failed)),
			zap.Strings("failed_drivers", startResult.Failed),
		)
	}

	logger.Info("驱动启动完成",
		zap.Int("total", startResult.Total),
		zap.Int("started", startResult.Started),
	)

	// 核心判断：如果没有任何驱动成功启动，则退出
	if startResult.Started == 0 {
		logger.Fatal("所有驱动启动失败，网关无法工作",
			zap.Int("total", startResult.Total),
			zap.Errors("errors", startResult.Errors),
		)
	}

	// ── 北向导出器 ────────────────────────────────────────────────
	logger.Info("初始化北向导出器...")
	exporters := initExporters(cfg, logger)

	// 启动导出器
	logger.Info("启动导出器...")
	for _, exp := range exporters {
		go exp.Run(ctx, bus.Subscribe())
	}

	logger.Info("所有组件启动完毕，开始采集 → 按 Ctrl+C 退出")

	// ── 启动统计日志协程 ──────────────────────────────────────────────
	go statsLogger(ctx, bus, logger, 1*time.Minute)

	// ── 启动 HTTP metrics 接口 ────────────────────────────────────────
	metricsAddr := cfg.Gateway.MetricsAddr
	if metricsAddr == "" {
		metricsAddr = ":8080" // 默认端口
	}
	metricsServer := startMetricsServer(ctx, metricsAddr, bus, logger)

	// ── 等待退出信号 ──────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	logger.Info("收到退出信号，开始优雅关闭", zap.String("signal", sig.String()))

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	cancel() // 通知所有协程退出

	if err := drvMgr.StopAll(shutdownCtx); err != nil {
		logger.Warn("驱动停止时出现错误", zap.Error(err))
	}

	for _, exp := range exporters {
		if err := exp.Close(); err != nil {
			logger.Warn("导出器关闭时出现错误", zap.Error(err))
		}
	}

	// 关闭 HTTP metrics 服务器
	if metricsServer != nil {
		shutdownHTTPCtx, shutdownHTTPCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownHTTPCancel()
		if err := metricsServer.Shutdown(shutdownHTTPCtx); err != nil {
			logger.Warn("HTTP metrics 服务器关闭时出现错误", zap.Error(err))
		}
	}

	logger.Info("网关已安全退出")
}

// createLogger 根据配置创建logger
func createLogger(cfg *config.AppConfig) *zap.Logger {
	var logCfg zap.Config

	switch cfg.Gateway.LogLevel {
	case "debug":
		logCfg = zap.NewDevelopmentConfig()
		logCfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		logCfg = zap.NewDevelopmentConfig()
		logCfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		logCfg = zap.NewDevelopmentConfig()
		logCfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		logCfg = zap.NewDevelopmentConfig()
		logCfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		logCfg = zap.NewDevelopmentConfig()
		logCfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	logger, err := logCfg.Build()
	if err != nil {
		panic(fmt.Sprintf("创建logger失败: %v", err))
	}

	return logger
}

// initExporters 初始化北向导出器
func initExporters(cfg *config.AppConfig, logger *zap.Logger) []exporter.Exporter {
	var exporters []exporter.Exporter

	// MQTT导出器
	if cfg.Exporters.MQTT != nil && cfg.Exporters.MQTT.Enabled {
		mqttCfg := exporter.MQTTConfig{
			Broker:      cfg.Exporters.MQTT.Broker,
			ClientID:    cfg.Exporters.MQTT.ClientID,
			TopicPrefix: cfg.Exporters.MQTT.TopicPrefix,
			QoS:         cfg.Exporters.MQTT.QoS,
			Username:    cfg.Exporters.MQTT.Username,
			Password:    cfg.Exporters.MQTT.Password,
			ConnTimeout: cfg.Exporters.MQTT.ConnTimeout,
		}

		exp := exporter.NewMQTTExporterWithConfig(logger, mqttCfg, exporter.BatchConfig{
			MaxSize:    cfg.Exporters.Batch.MaxSize,
			MaxLatency: cfg.Exporters.Batch.MaxLatency,
		})

		exporters = append(exporters, exp)
		logger.Info("MQTT导出器初始化完成", zap.String("broker", mqttCfg.Broker))
	}

	// Kafka导出器
	if cfg.Exporters.Kafka != nil && cfg.Exporters.Kafka.Enabled {
		kafkaCfg := exporter.KafkaConfig{
			Brokers:      cfg.Exporters.Kafka.Brokers,
			Topic:        cfg.Exporters.Kafka.Topic,
			Async:        cfg.Exporters.Kafka.Async,
			Timeout:      cfg.Exporters.Kafka.Timeout,
			BatchSize:    cfg.Exporters.Kafka.BatchSize,
			BatchTimeout: cfg.Exporters.Kafka.BatchTimeout,
			RequiredAcks: cfg.Exporters.Kafka.Acks,
			Compression:  cfg.Exporters.Kafka.Compression,
		}

		exp := exporter.NewKafkaExporter(logger, kafkaCfg, exporter.BatchConfig{
			MaxSize:    cfg.Exporters.Batch.MaxSize,
			MaxLatency: cfg.Exporters.Batch.MaxLatency,
		})

		exporters = append(exporters, exp)
		logger.Info("Kafka导出器初始化完成",
			zap.Strings("brokers", kafkaCfg.Brokers),
			zap.String("topic", kafkaCfg.Topic))
	}

	return exporters
}

// statsLogger 定期打印总线统计信息到日志
func statsLogger(ctx context.Context, bus *broker.Bus, logger *zap.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastPublished, lastDropped, lastFiltered uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := bus.StatsDetailed()

			// 计算增量
			deltaPublished := stats.Published - lastPublished
			deltaDropped := stats.Dropped - lastDropped
			deltaFiltered := stats.Filtered - lastFiltered

			// 计算速率（每秒）
			ratePublished := float64(deltaPublished) / interval.Seconds()
			rateDropped := float64(deltaDropped) / interval.Seconds()
			rateFiltered := float64(deltaFiltered) / interval.Seconds()

			// 计算丢弃率和过滤率
			total := deltaPublished + deltaDropped + deltaFiltered
			var dropRate, filterRate float64
			if total > 0 {
				dropRate = float64(deltaDropped) / float64(total) * 100
				filterRate = float64(deltaFiltered) / float64(total) * 100
			}

			// 缓冲区使用率
			var bufferUsage float64
			if stats.BufferSize > 0 {
				bufferUsage = float64(stats.BufferUsed) / float64(stats.BufferSize) * 100
			}

			logger.Info("总线统计",
				// 累计值
				zap.Uint64("published_total", stats.Published),
				zap.Uint64("dropped_total", stats.Dropped),
				zap.Uint64("filtered_total", stats.Filtered),

				// 增量值
				zap.Uint64("published_delta", deltaPublished),
				zap.Uint64("dropped_delta", deltaDropped),
				zap.Uint64("filtered_delta", deltaFiltered),

				// 速率
				zap.Float64("published_rate", ratePublished),
				zap.Float64("dropped_rate", rateDropped),
				zap.Float64("filtered_rate", rateFiltered),

				// 百分比
				zap.Float64("drop_rate_pct", dropRate),
				zap.Float64("filter_rate_pct", filterRate),
				zap.Float64("buffer_usage_pct", bufferUsage),

				// 其他信息
				zap.Int("subscribers", stats.SubscriberCount),
				zap.Int("deadband_entries", stats.DeadbandEntries),
			)

			// 更新上次的值
			lastPublished = stats.Published
			lastDropped = stats.Dropped
			lastFiltered = stats.Filtered
		}
	}
}

// startMetricsServer 启动 HTTP metrics 服务器
func startMetricsServer(ctx context.Context, addr string, bus *broker.Bus, logger *zap.Logger) *http.Server {
	mux := http.NewServeMux()

	// /metrics 端点 - 返回 JSON 格式的统计信息
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		stats := bus.StatsDetailed()

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stats); err != nil {
			logger.Error("编码 metrics 响应失败", zap.Error(err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	// /health 健康检查端点
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// /ready 就绪检查端点
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		stats := bus.StatsDetailed()
		w.Header().Set("Content-Type", "application/json")

		// 如果有订阅者，则认为就绪
		if stats.SubscriberCount > 0 {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "not ready"})
		}
	})

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("HTTP metrics 服务器启动", zap.String("addr", addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP metrics 服务器错误", zap.Error(err))
		}
	}()

	return server
}
