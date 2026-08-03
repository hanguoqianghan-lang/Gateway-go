// internal/driver/guowang102/file_transfer.go - 国网102规约 文件传输状态机
package guowang102

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// 文件传输状态定义
// ─────────────────────────────────────────────────────────────────────────────

// FileTransferState 文件传输状态
type FileTransferState int

const (
	FileStateIdle FileTransferState = iota       // 空闲
	FileStateReceiving                          // 接收中
	FileStateComplete                           // 完成
	FileStateError                              // 错误
	FileStateDuplicate                          // 重复文件
)

func (s FileTransferState) String() string {
	switch s {
	case FileStateIdle:
		return "Idle"
	case FileStateReceiving:
		return "Receiving"
	case FileStateComplete:
		return "Complete"
	case FileStateError:
		return "Error"
	case FileStateDuplicate:
		return "Duplicate"
	default:
		return "Unknown"
	}
}

// FileTransferContext 文件传输上下文
type FileTransferContext struct {
	FileName       string            // 文件名 (去除填充后)
	ExpectedSize   uint32            // 预期文件大小 (来自最后一帧或首帧)
	ReceivedSize   uint32            // 已接收字节数
	Chunks         map[uint16][]byte // 帧序号 -> 数据块
	LastSeqNum     uint16            // 最后接收的序号
	LastCOT        uint8             // 最后收到的 COT
	StartTime      time.Time         // 开始接收时间
	LastActiveTime time.Time         // 最后活动时间
	State          FileTransferState // 当前状态
	ErrorMsg       string            // 错误信息
	mu             sync.Mutex
}

// FileTransferManager 文件传输管理器
type FileTransferManager struct {
	logger       *zap.Logger
	storageDir   string
	maxFileSize  int
	fileTimeout  time.Duration
	cleanupInterval time.Duration

	// 并发控制
	contexts     map[string]*FileTransferContext
	ctxMu        sync.RWMutex

	// 回调
	onFileComplete func(fileName string, data []byte) error
	onFileError    func(fileName string, err error)

	// 统计
	stats FileTransferStats

	// 生命周期
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// FileTransferStats 文件传输统计
type FileTransferStats struct {
	FilesReceived     uint64
	FilesCompleted    uint64
	FilesFailed       uint64
	FilesDuplicated   uint64
	FilesTimeout      uint64
	BytesReceived     uint64
	ChunksReceived    uint64
	ChunksOutOfOrder  uint64
}

// FileTransferConfig 文件传输配置
type FileTransferConfig struct {
	StorageDir      string        // 存储目录
	MaxFileSize     int           // 最大文件大小 (默认 20480 = 512*40)
	FileTimeout     time.Duration // 单文件接收超时 (默认 30s)
	CleanupInterval time.Duration // 清理间隔 (默认 60s)
	MaxConcurrent   int           // 最大并发文件数 (默认 100)
}

// DefaultFileTransferConfig 默认配置
func DefaultFileTransferConfig() FileTransferConfig {
	return FileTransferConfig{
		StorageDir:      "./data/guowang102/files",
		MaxFileSize:     20480, // 512 * 40
		FileTimeout:     30 * time.Second,
		CleanupInterval: 60 * time.Second,
		MaxConcurrent:   100,
	}
}

// NewFileTransferManager 创建文件传输管理器
func NewFileTransferManager(logger *zap.Logger, cfg FileTransferConfig) *FileTransferManager {
	ctx, cancel := context.WithCancel(context.Background())
	ftm := &FileTransferManager{
		logger:          logger.Named("filetransfer"),
		storageDir:      cfg.StorageDir,
		maxFileSize:     cfg.MaxFileSize,
		fileTimeout:     cfg.FileTimeout,
		cleanupInterval: cfg.CleanupInterval,
		contexts:        make(map[string]*FileTransferContext),
		ctx:             ctx,
		cancel:          cancel,
	}

	// 确保存储目录存在
	os.MkdirAll(cfg.StorageDir, 0755)

	// 启动清理协程
	ftm.wg.Add(1)
	go ftm.cleanupLoop()

	return ftm
}

// SetCallbacks 设置回调
func (ftm *FileTransferManager) SetCallbacks(
	onComplete func(fileName string, data []byte) error,
	onError func(fileName string, err error),
) {
	ftm.onFileComplete = onComplete
	ftm.onFileError = onError
}

// ─────────────────────────────────────────────────────────────────────────────
// 核心处理流程
// ─────────────────────────────────────────────────────────────────────────────

// ProcessFileTransferASDU 处理文件传输 ASDU
// 返回: (是否需要链路层确认, 是否需要应用层确认, 错误)
func (ftm *FileTransferManager) ProcessFileTransferASDU(asdu *ASDU) (needLinkAck, needAppAck bool, err error) {
	// 验证是文件传输类型
	if !IsFileTransferTypeID(asdu.TypeID) {
		return false, false, nil // 非文件传输，忽略
	}

	// 解析文件名和内容
	fileName, content, _, err := ParseFileTransferASDU(asdu)
	if err != nil {
		atomic.AddUint64(&ftm.stats.FilesFailed, 1)
		return false, false, fmt.Errorf("parse file transfer ASDU failed: %w", err)
	}

	if fileName == "" {
		return false, false, errors.New("empty file name")
	}

	// 文件大小检查
	if len(content) > ftm.maxFileSize {
		atomic.AddUint64(&ftm.stats.FilesFailed, 1)
		return false, false, fmt.Errorf("file chunk too large: %d > %d", len(content), ftm.maxFileSize)
	}

	// 获取或创建传输上下文
	ctx := ftm.getOrCreateContext(fileName)
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	// 状态检查
	switch ctx.State {
	case FileStateComplete:
		// 已完成的文件，可能是重复传输
		if asdu.COT == COT_FileNotLastFrame || asdu.COT == COT_FileLastFrame {
			atomic.AddUint64(&ftm.stats.FilesDuplicated, 1)
			ctx.State = FileStateDuplicate
			ctx.ErrorMsg = "duplicate file received"
			ftm.logger.Warn("duplicate file received", zap.String("file", fileName))
			// 需要应用层确认重复 (COT=0x0D/0x0E)
			return true, true, nil
		}
	case FileStateError, FileStateDuplicate:
		return false, false, nil
	}

	// 更新活动时间
	ctx.LastActiveTime = time.Now()

	// 处理不同 COT
	switch asdu.COT {
	case COT_FileNotLastFrame: // 0x08 - 非最后一帧
		return ftm.handleNotLastFrame(ctx, fileName, content, asdu)

	case COT_FileLastFrame: // 0x07 - 最后一帧
		return ftm.handleLastFrame(ctx, fileName, content, asdu)

	case COT_FileRecvComplete: // 0x0A - 主站确认接收完成 (下行)
		// 这是主站发送的确认，不应到达这里
		ftm.logger.Warn("unexpected COT_FileRecvComplete from uplink", zap.String("file", fileName))
		return false, false, nil

	case COT_FileLenMatch: // 0x0B - 子站确认长度匹配
		// 子站响应，不处理
		return false, false, nil

	case COT_FileLenMismatch: // 0x0C - 子站长度不匹配，准备重传
		ftm.logger.Warn("file length mismatch reported by remote", zap.String("file", fileName))
		ctx.State = FileStateError
		ctx.ErrorMsg = "length mismatch"
		return false, false, nil

	case COT_FileDuplicate: // 0x0D - 主站检测到重复
		ctx.State = FileStateDuplicate
		ctx.ErrorMsg = "duplicate detected by master"
		return false, false, nil

	case COT_FileDupConfirmed: // 0x0E - 子站确认重复
		return false, false, nil

	case COT_FileTooLong: // 0x0F - 文件过长
		ctx.State = FileStateError
		ctx.ErrorMsg = "file too long"
		return false, false, nil

	case COT_FileLongConfirmed: // 0x10 - 子站确认过长
		return false, false, nil

	case COT_FileNameInvalid: // 0x11 - 文件名无效
		ctx.State = FileStateError
		ctx.ErrorMsg = "invalid file name"
		return false, false, nil

	case COT_FileNameConfirmed: // 0x12 - 子站确认文件名无效
		return false, false, nil

	case COT_FrameTooLong: // 0x13 - 单帧过长
		ctx.State = FileStateError
		ctx.ErrorMsg = "frame too long"
		return false, false, nil

	case COT_FrameLongConfirmed: // 0x14 - 子站确认单帧过长
		return false, false, nil

	default:
		ftm.logger.Warn("unknown COT for file transfer",
			zap.String("file", fileName),
			zap.String("cot", COTString(asdu.COT)),
		)
		return false, false, nil
	}
}

// handleNotLastFrame 处理非最后一帧 (COT=0x08)
func (ftm *FileTransferManager) handleNotLastFrame(ctx *FileTransferContext, fileName string, content []byte, asdu *ASDU) (bool, bool, error) {
	// 追加数据块
	seqNum := asdu.Sequence // 假设 ASDU 中有序号字段，或使用内部计数
	if seqNum == 0 {
		seqNum = ctx.LastSeqNum + 1
	}

	// 检查乱序
	if seqNum <= ctx.LastSeqNum && ctx.LastSeqNum > 0 {
		atomic.AddUint64(&ftm.stats.ChunksOutOfOrder, 1)
		ftm.logger.Warn("out of order chunk",
			zap.String("file", fileName),
			zap.Uint16("expected", ctx.LastSeqNum+1),
			zap.Uint16("received", seqNum),
		)
	}

	ctx.Chunks[seqNum] = content
	ctx.LastSeqNum = seqNum
	ctx.ReceivedSize += uint32(len(content))
	ctx.State = FileStateReceiving

	// 大小检查
	if ctx.ReceivedSize > uint32(ftm.maxFileSize) {
		ctx.State = FileStateError
		ctx.ErrorMsg = "file size exceeded"
		atomic.AddUint64(&ftm.stats.FilesFailed, 1)
		return true, true, fmt.Errorf("file size exceeded: %d > %d", ctx.ReceivedSize, ftm.maxFileSize)
	}

	atomic.AddUint64(&ftm.stats.ChunksReceived, 1)
	atomic.AddUint64(&ftm.stats.BytesReceived, uint64(len(content)))

	// 每帧需要链路层确认 (FC=3)
	return true, false, nil
}

// handleLastFrame 处理最后一帧 (COT=0x07)
func (ftm *FileTransferManager) handleLastFrame(ctx *FileTransferContext, fileName string, content []byte, asdu *ASDU) (bool, bool, error) {
	seqNum := asdu.Sequence
	if seqNum == 0 {
		seqNum = ctx.LastSeqNum + 1
	}
	ctx.Chunks[seqNum] = content
	ctx.ReceivedSize += uint32(len(content))
	ctx.LastSeqNum = seqNum

	// 重组完整文件
	completeData, err := ctx.reassembleFile()
	if err != nil {
		ctx.State = FileStateError
		ctx.ErrorMsg = err.Error()
		atomic.AddUint64(&ftm.stats.FilesFailed, 1)
		return true, true, err
	}

	// 验证文件大小 (如果有预期大小)
	if ctx.ExpectedSize > 0 && uint32(len(completeData)) != ctx.ExpectedSize {
		ctx.State = FileStateError
		ctx.ErrorMsg = fmt.Sprintf("size mismatch: expected %d, got %d", ctx.ExpectedSize, len(completeData))
		atomic.AddUint64(&ftm.stats.FilesFailed, 1)
		return true, true, fmt.Errorf("file size mismatch")
	}

	// 保存文件
	savePath := filepath.Join(ftm.storageDir, fileName)
	if err := os.WriteFile(savePath, completeData, 0644); err != nil {
		ctx.State = FileStateError
		ctx.ErrorMsg = "save failed: " + err.Error()
		atomic.AddUint64(&ftm.stats.FilesFailed, 1)
		return true, true, fmt.Errorf("save file failed: %w", err)
	}

	ctx.State = FileStateComplete
	atomic.AddUint64(&ftm.stats.FilesCompleted, 1)
	atomic.AddUint64(&ftm.stats.FilesReceived, 1)

	ftm.logger.Info("file transfer completed",
		zap.String("file", fileName),
		zap.Int("size", len(completeData)),
		zap.String("path", savePath),
		zap.Duration("duration", time.Since(ctx.StartTime)),
	)

	// 触发完成回调
	if ftm.onFileComplete != nil {
		if err := ftm.onFileComplete(fileName, completeData); err != nil {
			ftm.logger.Error("file complete callback error", zap.Error(err))
		}
	}

	// 最后一帧需要链路层确认 + 应用层确认 (COT=0x0A)
	return true, true, nil
}

// reassembleFile 重组文件数据 (按序号排序拼接)
func (ctx *FileTransferContext) reassembleFile() ([]byte, error) {
	if len(ctx.Chunks) == 0 {
		return nil, errors.New("no chunks to reassemble")
	}

	// 计算总大小
	totalSize := 0
	for _, chunk := range ctx.Chunks {
		totalSize += len(chunk)
	}

	// 预分配
	result := make([]byte, 0, totalSize)

	// 按序号排序拼接
	for i := uint16(1); ; i++ {
		chunk, ok := ctx.Chunks[i]
		if !ok {
			break
		}
		result = append(result, chunk...)
	}

	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 上下文管理
// ─────────────────────────────────────────────────────────────────────────────

func (ftm *FileTransferManager) getOrCreateContext(fileName string) *FileTransferContext {
	ftm.ctxMu.RLock()
	ctx, exists := ftm.contexts[fileName]
	ftm.ctxMu.RUnlock()

	if exists {
		return ctx
	}

	ftm.ctxMu.Lock()
	defer ftm.ctxMu.Unlock()

	// 双重检查
	if ctx, exists = ftm.contexts[fileName]; exists {
		return ctx
	}

	// 检查并发限制
	if len(ftm.contexts) >= 1000 { // 硬限制
		// 清理最旧的已完成上下文
		ftm.cleanupCompletedLocked()
	}

	ctx = &FileTransferContext{
		FileName:       fileName,
		Chunks:         make(map[uint16][]byte),
		StartTime:      time.Now(),
		LastActiveTime: time.Now(),
		State:          FileStateIdle,
	}
	ftm.contexts[fileName] = ctx
	return ctx
}

// ─────────────────────────────────────────────────────────────────────────────
// 清理循环
// ─────────────────────────────────────────────────────────────────────────────

func (ftm *FileTransferManager) cleanupLoop() {
	defer ftm.wg.Done()

	ticker := time.NewTicker(ftm.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ftm.ctx.Done():
			return
		case <-ticker.C:
			ftm.cleanupExpired()
		}
	}
}

func (ftm *FileTransferManager) cleanupExpired() {
	now := time.Now()
	ftm.ctxMu.Lock()
	defer ftm.ctxMu.Unlock()

	for fileName, ctx := range ftm.contexts {
		ctx.mu.Lock()

		shouldRemove := false

		switch ctx.State {
		case FileStateComplete, FileStateError, FileStateDuplicate:
			// 完成/错误/重复状态保留 5 分钟
			if now.Sub(ctx.LastActiveTime) > 5*time.Minute {
				shouldRemove = true
			}
		case FileStateReceiving:
			// 接收中超时
			if now.Sub(ctx.LastActiveTime) > ftm.fileTimeout {
				ctx.State = FileStateError
				ctx.ErrorMsg = "transfer timeout"
				atomic.AddUint64(&ftm.stats.FilesTimeout, 1)
				atomic.AddUint64(&ftm.stats.FilesFailed, 1)
				ftm.logger.Warn("file transfer timeout",
					zap.String("file", fileName),
					zap.Duration("elapsed", now.Sub(ctx.StartTime)),
				)
				if ftm.onFileError != nil {
					ftm.onFileError(fileName, errors.New("transfer timeout"))
				}
				shouldRemove = false // 保留错误状态供诊断
			}
		}

		if shouldRemove {
			delete(ftm.contexts, fileName)
			ftm.logger.Debug("cleaned up file context", zap.String("file", fileName))
		}

		ctx.mu.Unlock()
	}
}

func (ftm *FileTransferManager) cleanupCompletedLocked() {
	now := time.Now()
	for fileName, ctx := range ftm.contexts {
		ctx.mu.Lock()
		if (ctx.State == FileStateComplete || ctx.State == FileStateError || ctx.State == FileStateDuplicate) &&
			now.Sub(ctx.LastActiveTime) > time.Minute {
			delete(ftm.contexts, fileName)
		}
		ctx.mu.Unlock()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 公共接口
// ─────────────────────────────────────────────────────────────────────────────

// GetStats 获取统计信息
func (ftm *FileTransferManager) GetStats() FileTransferStats {
	return FileTransferStats{
		FilesReceived:    atomic.LoadUint64(&ftm.stats.FilesReceived),
		FilesCompleted:   atomic.LoadUint64(&ftm.stats.FilesCompleted),
		FilesFailed:      atomic.LoadUint64(&ftm.stats.FilesFailed),
		FilesDuplicated:  atomic.LoadUint64(&ftm.stats.FilesDuplicated),
		FilesTimeout:     atomic.LoadUint64(&ftm.stats.FilesTimeout),
		BytesReceived:    atomic.LoadUint64(&ftm.stats.BytesReceived),
		ChunksReceived:   atomic.LoadUint64(&ftm.stats.ChunksReceived),
		ChunksOutOfOrder: atomic.LoadUint64(&ftm.stats.ChunksOutOfOrder),
	}
}

// GetActiveTransfers 获取活跃传输列表
func (ftm *FileTransferManager) GetActiveTransfers() []string {
	ftm.ctxMu.RLock()
	defer ftm.ctxMu.RUnlock()

	var active []string
	for name, ctx := range ftm.contexts {
		ctx.mu.Lock()
		if ctx.State == FileStateReceiving {
			active = append(active, name)
		}
		ctx.mu.Unlock()
	}
	return active
}

// Close 关闭管理器
func (ftm *FileTransferManager) Close() error {
	ftm.cancel()
	ftm.wg.Wait()
	return nil
}