// internal/driver/guowang102/driver.go - 国网102驱动核心实现
package guowang102

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/gateway/gateway/internal/broker"
	"github.com/gateway/gateway/internal/driver"
	"github.com/gateway/gateway/internal/model"
)

// ─────────────────────────────────────────────────────────────────────────────
// 配置结构 (由 YAML 解析)
// ─────────────────────────────────────────────────────────────────────────────

// DriverConfig 驱动配置 (对应 YAML 中 drivers.guowang102 部分)
type DriverConfig struct {
	// 网络连接
	Host                string `yaml:"host"`
	Port                int    `yaml:"port"`
	LinkAddress         uint16 `yaml:"link_address"`
	CommonAddress       uint16 `yaml:"common_address"`
	ConnectTimeout      string `yaml:"connect_timeout"`
	ReadTimeout         string `yaml:"read_timeout"`
	WriteTimeout        string `yaml:"write_timeout"`
	KeepAliveInterval   string `yaml:"keepalive_interval"`

	// 协议流程
	LinkStatusInterval     string `yaml:"link_status_interval"`
	BackgroundScanInterval string `yaml:"background_scan_interval"`
	PeriodicReadInterval   string `yaml:"periodic_read_interval"`
	MaxRetry               int    `yaml:"max_retry"`
	RetryInterval          string `yaml:"retry_interval"`
	FrameTimeout           string `yaml:"frame_timeout"`

	// 文件存储
	StorageDir  string `yaml:"storage_dir"`
	MaxFileSize int    `yaml:"max_file_size"`
	FileTimeout string `yaml:"file_timeout"`

	// 日志
	LogLevel string `yaml:"log_level"`
}

// ─────────────────────────────────────────────────────────────────────────────
// 驱动实现
// ─────────────────────────────────────────────────────────────────────────────

// Driver 国网102驱动实现
type Driver struct {
	cfg        *DriverConfig
	logger     *zap.Logger
	client     *Client
	handler    *LinkLayer
	fileMgr    *FileTransferManager
	bus        *broker.Bus
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	stats      DriverStats
	startedAt  time.Time
}

// DriverStats 驱动统计
type DriverStats struct {
	FilesReceived     uint64
	FilesCompleted    uint64
	FilesFailed       uint64
	FilesDuplicated   uint64
	BytesReceived     uint64
	LastFileTime      int64 // UnixNano
	LastConnectTime   int64
	LastDisconnectTime int64
	LinkResets        uint64
}

// NewDriver 创建驱动实例 (供注册机制调用)
func NewDriver(cfg *DriverConfig, logger *zap.Logger) (driver.Driver, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	if logger == nil {
		logger, _ = zap.NewProduction()
	}

	d := &Driver{
		cfg:    cfg,
		logger: logger.Named("guowang102"),
	}
	return d, nil
}

// Init 初始化驱动
func (d *Driver) Init(ctx context.Context) error {
	d.logger.Info("initializing GuoWang102 driver",
		zap.String("host", d.cfg.Host),
		zap.Int("port", d.cfg.Port),
		zap.Uint16("link_addr", d.cfg.LinkAddress),
	)

	// 解析超时配置
	connTimeout := parseDurationOrDefault(d.cfg.ConnectTimeout, 10*time.Second)
	readTimeout := parseDurationOrDefault(d.cfg.ReadTimeout, 30*time.Second)
	writeTimeout := parseDurationOrDefault(d.cfg.WriteTimeout, 10*time.Second)
	keepAlive := parseDurationOrDefault(d.cfg.KeepAliveInterval, 10*time.Second)

	// 创建 TCP 客户端
	clientCfg := ClientConfig{
		Host:                 d.cfg.Host,
		Port:                 d.cfg.Port,
		LinkAddress:          d.cfg.LinkAddress,
		CommonAddress:        d.cfg.CommonAddress,
		ConnectTimeout:       connTimeout,
		ReadTimeout:          readTimeout,
		WriteTimeout:         writeTimeout,
		KeepAliveInterval:    keepAlive,
		ReconnectInterval:    5 * time.Second,
		MaxReconnectInterval: 60 * time.Second,
	}
	d.client = NewClient(clientCfg, d.logger)

	// 创建链路层
	d.handler = NewLinkLayer(d.client, d.logger)
	d.handler.SetMaxRetries(d.cfg.MaxRetry)
	if d.cfg.MaxRetry == 0 {
		d.handler.SetMaxRetries(3)
	}

	// 解析文件传输配置
	fileTimeout := parseDurationOrDefault(d.cfg.FileTimeout, 30*time.Second)
	maxFileSize := d.cfg.MaxFileSize
	if maxFileSize == 0 {
		maxFileSize = 20480 // 512 * 40
	}
	storageDir := d.cfg.StorageDir
	if storageDir == "" {
		storageDir = "./data/guowang102/files"
	}

	// 确保存储目录存在
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		return fmt.Errorf("create storage dir failed: %w", err)
	}

	// 创建文件传输管理器
	ftCfg := FileTransferConfig{
		StorageDir:      storageDir,
		MaxFileSize:     maxFileSize,
		FileTimeout:     fileTimeout,
		CleanupInterval: 60 * time.Second,
		MaxConcurrent:   100,
	}
	d.fileMgr = NewFileTransferManager(d.logger, ftCfg)

	// 设置文件传输回调
	d.fileMgr.SetCallbacks(
		d.onFileComplete,
		d.onFileError,
	)

	// 设置应用层回调
	OnASDUReceived = d.handleASDU

	d.logger.Info("GuoWang102 driver initialized",
		zap.String("storage_dir", storageDir),
		zap.Int("max_file_size", maxFileSize),
		zap.Duration("file_timeout", fileTimeout),
	)

	return nil
}

// Start 启动驱动
func (d *Driver) Start(ctx context.Context, bus *broker.Bus) error {
	d.bus = bus
	d.logger.Info("starting GuoWang102 driver")

	ctx, d.cancel = context.WithCancel(ctx)

	// 1. 连接到子站
	if err := d.client.Connect(); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	d.stats.LastConnectTime = time.Now().UnixNano()

	// 2. 启动链路层后台任务
	d.handler.StartBackgroundTasks(ctx)

	// 3. 启动主循环：接收帧处理 (必须在链路初始化前启动，以便处理链路建立过程中的帧)
	d.wg.Add(1)
	go d.receiveLoop(ctx)

	// 4. 启动链路初始化流程 (复位链路 -> 启动数据传输)
	if err := d.handler.StartLinkInitialization(ctx); err != nil {
		return fmt.Errorf("link initialization failed: %w", err)
	}

	// 5. 等待链路进入运行状态
	if err := d.waitLinkOperational(ctx); err != nil {
		return fmt.Errorf("link not operational: %w", err)
	}

	// 6. 启动定时轮询：召唤2级数据 (FC=11)
	if interval := d.cfg.BackgroundScanInterval; interval != "" {
		if dur, err := time.ParseDuration(interval); err == nil && dur > 0 {
			d.wg.Add(1)
			go d.backgroundScanLoop(ctx, dur)
		}
	}

	// 7. 启动定时轮询：召唤1级数据 (FC=10)
	if interval := d.cfg.PeriodicReadInterval; interval != "" {
		if dur, err := time.ParseDuration(interval); err == nil && dur > 0 {
			d.wg.Add(1)
			go d.periodicReadLoop(ctx, dur)
		}
	}

	// 8. 启动链路状态检查 (FC=9)
	if interval := d.cfg.LinkStatusInterval; interval != "" {
		if dur, err := time.ParseDuration(interval); err == nil && dur > 0 {
			d.wg.Add(1)
			go d.linkStatusLoop(ctx, dur)
		}
	}

	d.startedAt = time.Now()
	d.logger.Info("GuoWang102 driver started")
	return nil
}

// Stop 停止驱动
func (d *Driver) Stop(ctx context.Context) error {
	d.logger.Info("stopping GuoWang102 driver")

	if d.cancel != nil {
		d.cancel()
	}

	d.wg.Wait()

	if d.client != nil {
		d.client.Close()
	}
	d.stats.LastDisconnectTime = time.Now().UnixNano()

	if d.fileMgr != nil {
		d.fileMgr.Close()
	}

	d.logger.Info("GuoWang102 driver stopped")
	return nil
}

// Name 返回驱动名称
func (d *Driver) Name() string {
	return "guowang102"
}

// ─────────────────────────────────────────────────────────────────────────────
// 内部循环
// ─────────────────────────────────────────────────────────────────────────────

// receiveLoop 接收循环：阻塞接收帧并分发处理
func (d *Driver) receiveLoop(ctx context.Context) {
	defer d.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// 接收一帧
			frameData, _, err := d.client.ReceiveFrame()
			if err != nil {
				// 连接断开或超时
				select {
				case <-ctx.Done():
					return
				default:
					d.logger.Warn("receive frame error", zap.Error(err))
					// 触发重连
					if err := d.reconnect(ctx); err != nil {
						d.logger.Error("reconnect failed", zap.Error(err))
					}
					continue
				}
			}

			// 解析帧
			frame, err := ParseFrame(frameData)
			if err != nil {
				d.logger.Warn("parse frame failed", zap.Error(err), zap.String("data", fmt.Sprintf("%X", frameData)))
				continue
			}

			// 分发到链路层处理
			d.handler.HandleFrame(frame)
		}
	}
}

// waitLinkOperational 等待链路进入运行状态
func (d *Driver) waitLinkOperational(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(30 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return errors.New("timeout waiting for link operational")
		case <-ticker.C:
			if d.handler.GetState() == LinkStateOperational {
				d.logger.Info("link operational")
				return nil
			}
		}
	}
}

// reconnect 重连逻辑
func (d *Driver) reconnect(ctx context.Context) error {
	d.logger.Info("reconnecting...")
	d.client.Close()

	// 等待重连间隔
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
	}

	if err := d.client.Connect(); err != nil {
		return err
	}
	d.stats.LastConnectTime = time.Now().UnixNano()

	// 重新初始化链路
	if err := d.handler.StartLinkInitialization(ctx); err != nil {
		return err
	}

	return d.waitLinkOperational(ctx)
}

// backgroundScanLoop 后台扫描循环：定时发送 FC=11 召唤2级数据
func (d *Driver) backgroundScanLoop(ctx context.Context, interval time.Duration) {
	defer d.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 启动时立即执行一次
	d.sendRequestLevel2()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sendRequestLevel2()
		}
	}
}

// periodicReadLoop 定时读取循环：定时发送 FC=10 召唤1级数据
func (d *Driver) periodicReadLoop(ctx context.Context, interval time.Duration) {
	defer d.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sendRequestLevel1()
		}
	}
}

// linkStatusLoop 链路状态检查循环：定时发送 FC=9
func (d *Driver) linkStatusLoop(ctx context.Context, interval time.Duration) {
	defer d.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sendLinkStatus()
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 发送辅助
// ─────────────────────────────────────────────────────────────────────────────

func (d *Driver) sendRequestLevel2() {
	// FC=11 是固定帧命令，不是 I 帧
	// 获取当前 FCB 状态
	d.handler.mu.Lock()
	fcb := d.handler.sendFCB
	d.handler.mu.Unlock()

	frame := BuildRequestLevel2Data(d.cfg.CommonAddress, fcb)
	err := d.client.SendFrame(frame)
	if err != nil {
		d.logger.Warn("send FC=11 failed", zap.Error(err))
	} else {
		d.logger.Debug("sent FC=11 (request level 2 data)")
	}
}

func (d *Driver) sendRequestLevel1() {
	// FC=10 是固定帧命令，不是 I 帧
	// 获取当前 FCB 状态
	d.handler.mu.Lock()
	fcb := d.handler.sendFCB
	d.handler.mu.Unlock()

	frame := BuildRequestLevel1Data(d.cfg.CommonAddress, fcb)
	err := d.client.SendFrame(frame)
	if err != nil {
		d.logger.Warn("send FC=10 failed", zap.Error(err))
	} else {
		d.logger.Debug("sent FC=10 (request level 1 data)")
	}
}

func (d *Driver) sendLinkStatus() {
	frame := BuildRequestLinkStatus(d.cfg.LinkAddress)
	err := d.client.SendFrame(frame)
	if err != nil {
		d.logger.Warn("send FC=9 failed", zap.Error(err))
	} else {
		d.logger.Debug("sent FC=9 (link status request)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 应用层处理
// ─────────────────────────────────────────────────────────────────────────────

// handleASDU 处理接收到的 ASDU
func (d *Driver) handleASDU(asdu *ASDU) {
	// 特殊处理：FC=8/11 响应 (TypeID=0xFF 是虚拟标识)
	if asdu.TypeID == 0xFF {
		d.logger.Debug("received FC response, requesting level 1 data",
			zap.Uint8("fc", asdu.COT))
		// 立即发送 FC=10 召唤 1 级数据
		d.sendRequestLevel1()
		return
	}

	// 验证是否为文件传输类型
	if !IsFileTransferTypeID(asdu.TypeID) {
		d.logger.Debug("received non-file-transfer ASDU",
			zap.Uint8("type_id", asdu.TypeID),
			zap.Uint8("cot", asdu.COT),
		)
		return
	}

	// 处理文件传输
	needLinkAck, needAppAck, err := d.fileMgr.ProcessFileTransferASDU(asdu)
	if err != nil {
		d.logger.Error("process file transfer ASDU failed", zap.Error(err))
		return
	}

	// 链路层确认 (FC=3)
	if needLinkAck {
		d.handler.SendAck()
	}

	// 应用层确认 (COT=0x0A 等)
	if needAppAck {
		d.sendFileReceiveComplete(asdu)
	}
}

// sendFileReceiveComplete 发送文件接收完成确认 (COT=0x0A)
func (d *Driver) sendFileReceiveComplete(asdu *ASDU) {
	ackASDU := BuildFileTransferACK(
		asdu.TypeID,
		asdu.CommonAddr,
		COT_FileRecvComplete, // 0x0A
		asdu.OriginAddr,
	)
	err := d.handler.SendIFrame(ackASDU, true, nil)
	if err != nil {
		d.logger.Error("send file recv complete failed", zap.Error(err))
	} else {
		d.logger.Debug("sent COT=0x0A (file recv complete)",
			zap.Uint8("type_id", asdu.TypeID),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 文件传输回调
// ─────────────────────────────────────────────────────────────────────────────

// onFileComplete 文件传输完成回调
func (d *Driver) onFileComplete(fileName string, data []byte) error {
	d.stats.FilesCompleted++
	d.stats.FilesReceived++
	d.stats.BytesReceived += uint64(len(data))
	d.stats.LastFileTime = time.Now().UnixNano()

	d.logger.Info("file transfer completed",
		zap.String("file", fileName),
		zap.Int("size", len(data)),
		zap.String("path", filepath.Join(d.cfg.StorageDir, fileName)),
	)

	// 发布文件事件到总线 (作为文件测点发布)
	p := model.GetPoint()
	p.ID = fmt.Sprintf("guowang102/%s", fileName)
	p.Value = data // 原始文件内容 ([]byte)
	p.Timestamp = time.Now().UnixNano()
	p.Quality = model.QualityGood

	if !d.bus.Publish(p) {
		d.logger.Warn("failed to publish file event", zap.String("file", fileName))
	}

	return nil
}

// onFileError 文件传输错误回调
func (d *Driver) onFileError(fileName string, err error) {
	d.stats.FilesFailed++
	d.logger.Error("file transfer error",
		zap.String("file", fileName),
		zap.Error(err),
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// 公共接口
// ─────────────────────────────────────────────────────────────────────────────

// GetStats 获取驱动统计信息
func (d *Driver) GetStats() map[string]interface{} {
	clientStats := d.client.GetStats()
	linkStats := d.handler.GetStats()
	fileStats := d.fileMgr.GetStats()

	return map[string]interface{}{
		"driver":          "guowang102",
		"started_at":      d.startedAt.UnixNano(),
		"uptime_sec":      time.Since(d.startedAt).Seconds(),
		"connected":       d.client.IsConnected(),
		"link_state":      d.handler.GetState().String(),

		// 客户端统计
		"tx_frames":       clientStats.TxFrames,
		"rx_frames":       clientStats.RxFrames,
		"tx_bytes":        clientStats.TxBytes,
		"rx_bytes":        clientStats.RxBytes,
		"errors":          clientStats.Errors,
		"reconnects":      clientStats.Reconnects,

		// 链路层统计
		"link_resets":         linkStats.LinkResets,
		"state_transitions":   linkStats.StateTransitions,
		"frames_sent":         linkStats.FramesSent,
		"frames_received":     linkStats.FramesReceived,
		"frames_retried":      linkStats.FramesRetried,
		"frames_timeout":      linkStats.FramesTimeout,
		"acd_triggered":       linkStats.ACDTriggered,
		"dfc_paused":          linkStats.DFCPaused,

		// 文件传输统计
		"files_received":      fileStats.FilesReceived,
		"files_completed":     fileStats.FilesCompleted,
		"files_failed":        fileStats.FilesFailed,
		"files_duplicated":    fileStats.FilesDuplicated,
		"files_timeout":       fileStats.FilesTimeout,
		"bytes_received":      fileStats.BytesReceived,
		"chunks_received":     fileStats.ChunksReceived,
		"chunks_out_of_order": fileStats.ChunksOutOfOrder,

		// 驱动统计
		"driver_files_completed": d.stats.FilesCompleted,
		"driver_files_failed":    d.stats.FilesFailed,
		"driver_bytes_received":  d.stats.BytesReceived,
		"last_file_time":         d.stats.LastFileTime,
		"last_connect_time":      d.stats.LastConnectTime,
		"last_disconnect_time":   d.stats.LastDisconnectTime,
	}
}

// GetActiveTransfers 获取活跃传输列表
func (d *Driver) GetActiveTransfers() []string {
	return d.fileMgr.GetActiveTransfers()
}

// ─────────────────────────────────────────────────────────────────────────────
// 工具函数
// ─────────────────────────────────────────────────────────────────────────────

func parseDurationOrDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}