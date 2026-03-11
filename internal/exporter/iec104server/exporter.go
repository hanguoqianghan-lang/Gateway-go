package iec104server

import (
	"context"

	"github.com/gateway/gateway/internal/model"

	"go.uber.org/zap"
)

// IEC104Exporter IEC104 Server 导出器
type IEC104Exporter struct {
	server *Server
	logger *zap.Logger
}

// NewIEC104Exporter 创建导出器
func NewIEC104Exporter(config Config, logger *zap.Logger) (*IEC104Exporter, error) {
	server, err := NewServer(config, logger)
	if err != nil {
		return nil, err
	}

	return &IEC104Exporter{
		server: server,
		logger: logger,
	}, nil
}

// Run 实现 Exporter 接口
// 启动 IEC104 Server 并从订阅通道消费南向数据
func (e *IEC104Exporter) Run(ctx context.Context, sub <-chan *model.PointData) {
	// 启动 Server
	if err := e.server.Start(ctx); err != nil {
		e.logger.Error("start IEC104 server failed", zap.Error(err))
		return
	}

	// 消费南向数据
	for {
		select {
		case <-ctx.Done():
			e.logger.Info("IEC104 exporter stopped by context")
			return
		case data, ok := <-sub:
			if !ok {
				e.logger.Info("IEC104 exporter stopped: channel closed")
				return
			}
			// 转发给 Server 处理
			e.server.OnData(data)
		}
	}
}

// Close 实现 Exporter 接口
func (e *IEC104Exporter) Close() error {
	e.server.Stop()
	return nil
}

// Name 实现 Exporter 接口
func (e *IEC104Exporter) Name() string {
	return "iec104_server"
}

// GetStats 获取统计信息
func (e *IEC104Exporter) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"connections":     e.server.GetConnectionCount(),
		"mapping_count":   e.server.GetMappingCount(),
		"listen_addr":     e.server.config.ListenAddr,
		"max_apdu_length": e.server.config.MaxAPDULength,
	}
}
