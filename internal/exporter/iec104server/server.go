package iec104server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gateway/gateway/internal/model"
	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"

	"go.uber.org/zap"
)

// Server IEC104 Server 实例
type Server struct {
	config         Config
	mappingManager *MappingManager
	asduBuilder    *ASDUBuilder
	logger         *zap.Logger

	// IEC104 Server
	iec104Server *cs104.Server

	// 连接管理
	connections map[asdu.Connect]struct{}
	connMu      sync.RWMutex

	// 数据通道
	dataChan chan *model.PointData

	// 生命周期控制
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewServer 创建 IEC104 Server
func NewServer(config Config, logger *zap.Logger) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	mappingMgr := NewMappingManager()
	if config.PointFile != "" {
		if err := mappingMgr.LoadFromCSV(config.PointFile); err != nil {
			return nil, fmt.Errorf("load point file failed: %w", err)
		}
		logger.Info("point mapping loaded",
			zap.String("file", config.PointFile),
			zap.Int("count", mappingMgr.GetMappingCount()),
		)
	}

	return &Server{
		config:         config,
		mappingManager: mappingMgr,
		asduBuilder:    NewASDUBuilder(config, mappingMgr),
		logger:         logger,
		connections:    make(map[asdu.Connect]struct{}),
		dataChan:       make(chan *model.PointData, config.QueueSize),
	}, nil
}

// Start 启动 Server
func (s *Server) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// 创建 IEC104 Server handler
	handler := &serverHandler{server: s}

	// 创建 IEC104 Server
	s.iec104Server = cs104.NewServer(handler)

	// 设置配置
	cfg := cs104.DefaultConfig()
	s.iec104Server.SetConfig(cfg)

	// 设置连接回调
	s.iec104Server.SetOnConnectionHandler(func(conn asdu.Connect) {
		s.connMu.Lock()
		s.connections[conn] = struct{}{}
		s.connMu.Unlock()

		remoteAddr := "unknown"
		if underlyingConn := conn.UnderlyingConn(); underlyingConn != nil {
			remoteAddr = underlyingConn.RemoteAddr().String()
		}
		s.logger.Info("new connection", zap.String("remote", remoteAddr))
	})

	// 设置断线回调
	s.iec104Server.SetConnectionLostHandler(func(conn asdu.Connect) {
		s.connMu.Lock()
		delete(s.connections, conn)
		s.connMu.Unlock()

		remoteAddr := "unknown"
		if underlyingConn := conn.UnderlyingConn(); underlyingConn != nil {
			remoteAddr = underlyingConn.RemoteAddr().String()
		}
		s.logger.Info("connection lost", zap.String("remote", remoteAddr))
	})

	// 启动数据分发协程
	s.wg.Add(1)
	go s.dispatchLoop()

	// 启动 IEC104 Server（在后台协程）
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.logger.Info("IEC104 server listening", zap.String("addr", s.config.ListenAddr))
		s.iec104Server.ListenAndServer(s.config.ListenAddr)
	}()

	s.logger.Info("IEC104 server started",
		zap.String("addr", s.config.ListenAddr),
		zap.Uint8("max_apdu_length", s.config.MaxAPDULength),
		zap.Uint16("common_address", s.config.CommonAddress),
	)

	return nil
}

// Stop 停止 Server
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}

	// 关闭 IEC104 Server
	if s.iec104Server != nil {
		s.iec104Server.Close()
	}

	// 等待所有协程退出
	s.wg.Wait()

	s.logger.Info("IEC104 server stopped")
}

// dispatchLoop 数据分发循环
func (s *Server) dispatchLoop() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case data := <-s.dataChan:
			// 更新缓存（用于总召响应）
			s.mappingManager.UpdateCache(data)

			// 构建变化上送 ASDU（COT=3）
			asdus, err := s.asduBuilder.BuildBatchASDUs([]*model.PointData{data}, asdu.Spontaneous)
			if err != nil {
				s.logger.Error("build ASDU failed",
					zap.Error(err),
					zap.String("point_id", data.ID),
				)
				continue
			}

			// 广播给所有连接
			s.connMu.RLock()
			for conn := range s.connections {
				for _, a := range asdus {
					if err := conn.Send(a); err != nil {
						remoteAddr := "unknown"
						if underlyingConn := conn.UnderlyingConn(); underlyingConn != nil {
							remoteAddr = underlyingConn.RemoteAddr().String()
						}
						s.logger.Error("send ASDU failed",
							zap.Error(err),
							zap.String("remote", remoteAddr),
						)
					}
				}
			}
			s.connMu.RUnlock()
		}
	}
}

// OnData 接收南向数据
func (s *Server) OnData(data *model.PointData) {
	select {
	case s.dataChan <- data:
	default:
		s.logger.Warn("data channel full, drop data",
			zap.String("point_id", data.ID),
			zap.Int("queue_size", s.config.QueueSize),
		)
	}
}

// GetConnectionCount 获取当前连接数
func (s *Server) GetConnectionCount() int {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return len(s.connections)
}

// GetMappingCount 获取映射点数
func (s *Server) GetMappingCount() int {
	return s.mappingManager.GetMappingCount()
}

// ─────────────────────────────────────────────────────────────────────────────
// serverHandler 实现 cs104.ServerHandlerInterface
// ─────────────────────────────────────────────────────────────────────────────

type serverHandler struct {
	server *Server
}

// InterrogationHandler 总召唤处理
func (h *serverHandler) InterrogationHandler(conn asdu.Connect, asduData *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	remoteAddr := "unknown"
	if underlyingConn := conn.UnderlyingConn(); underlyingConn != nil {
		remoteAddr = underlyingConn.RemoteAddr().String()
	}

	h.server.logger.Info("interrogation request received",
		zap.String("remote", remoteAddr),
		zap.Int("qoi", int(qoi)),
	)

	// 1. 发送总召唤激活确认
	ackASDU := asduData.Reply(asdu.ActivationCon, asdu.CommonAddr(h.server.config.CommonAddress))
	if err := conn.Send(ackASDU); err != nil {
		h.server.logger.Error("send interrogation ack failed", zap.Error(err))
		return err
	}

	// 2. 获取所有缓存数据
	allData := h.server.mappingManager.GetAllCachedData()

	if len(allData) == 0 {
		// 没有数据，直接发送终止
		return h.sendInterrogationTermination(conn, asduData)
	}

	// 3. 批量构建 ASDU（处理 APDU 长度限制）
	asdus, err := h.server.asduBuilder.BuildBatchASDUs(allData, asdu.InterrogatedByStation)
	if err != nil {
		h.server.logger.Error("build interrogation response failed",
			zap.Error(err),
			zap.String("remote", remoteAddr),
		)
		return h.sendInterrogationTermination(conn, asduData)
	}

	// 4. 发送所有 ASDU
	for _, a := range asdus {
		if err := conn.Send(a); err != nil {
			h.server.logger.Error("send interrogation ASDU failed",
				zap.Error(err),
				zap.String("remote", remoteAddr),
			)
		}
	}

	// 5. 发送总召唤终止
	if err := h.sendInterrogationTermination(conn, asduData); err != nil {
		return err
	}

	h.server.logger.Info("interrogation completed",
		zap.String("remote", remoteAddr),
		zap.Int("points", len(allData)),
		zap.Int("asdus", len(asdus)),
	)

	return nil
}

// sendInterrogationTermination 发送总召唤终止
func (h *serverHandler) sendInterrogationTermination(conn asdu.Connect, asduData *asdu.ASDU) error {
	termASDU := asduData.Reply(asdu.ActivationTerm, asdu.CommonAddr(h.server.config.CommonAddress))
	return conn.Send(termASDU)
}

// CounterInterrogationHandler 计数器召唤处理
func (h *serverHandler) CounterInterrogationHandler(conn asdu.Connect, asduData *asdu.ASDU, qcc asdu.QualifierCountCall) error {
	// 暂不支持
	return nil
}

// ReadHandler 读命令处理
func (h *serverHandler) ReadHandler(conn asdu.Connect, asduData *asdu.ASDU, ioa asdu.InfoObjAddr) error {
	// 暂不支持
	return nil
}

// ClockSyncHandler 时钟同步处理
func (h *serverHandler) ClockSyncHandler(conn asdu.Connect, asduData *asdu.ASDU, t time.Time) error {
	remoteAddr := "unknown"
	if underlyingConn := conn.UnderlyingConn(); underlyingConn != nil {
		remoteAddr = underlyingConn.RemoteAddr().String()
	}

	h.server.logger.Info("clock sync request received",
		zap.String("remote", remoteAddr),
		zap.Time("time", t),
	)

	// 发送时钟同步确认
	ackASDU := asduData.Reply(asdu.ActivationCon, asdu.CommonAddr(h.server.config.CommonAddress))
	return conn.Send(ackASDU)
}

// ResetProcessHandler 进程重置处理
func (h *serverHandler) ResetProcessHandler(conn asdu.Connect, asduData *asdu.ASDU, qrp asdu.QualifierOfResetProcessCmd) error {
	// 暂不支持
	return nil
}

// DelayAcquisitionHandler 延迟获取处理
func (h *serverHandler) DelayAcquisitionHandler(conn asdu.Connect, asduData *asdu.ASDU, delay uint16) error {
	// 暂不支持
	return nil
}

// ASDUHandler ASDU 数据处理
func (h *serverHandler) ASDUHandler(conn asdu.Connect, asduData *asdu.ASDU) error {
	// 处理其他 ASDU（如控制命令等）
	remoteAddr := "unknown"
	if underlyingConn := conn.UnderlyingConn(); underlyingConn != nil {
		remoteAddr = underlyingConn.RemoteAddr().String()
	}

	h.server.logger.Debug("received ASDU",
		zap.String("remote", remoteAddr),
		zap.Uint8("type_id", uint8(asduData.Identifier.Type)),
	)

	return nil
}
