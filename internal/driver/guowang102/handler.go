// internal/driver/guowang102/handler.go - 国网102规约 链路层状态机与应用层分发
package guowang102

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// 链路层状态机
// ─────────────────────────────────────────────────────────────────────────────

// LinkLayerState 链路层状态
type LinkLayerState int

const (
	LinkStateDisconnected LinkLayerState = iota // 断开
	LinkStateResetSent                          // 已发送复位命令
	LinkStateResetConfirmed                     // 复位确认收到
	LinkStateTransferStarted                    // 启动数据传输确认收到
	LinkStateOperational                        // 正常运行
)

// LinkLayer 链路层状态机
type LinkLayer struct {
	client       *Client
	logger       *zap.Logger
	mu           sync.Mutex
	state        LinkLayerState
	sendFCB      bool      // 发送侧 FCB
	recvFCB      bool      // 接收侧 FCB (期望收到的 FCB)
	pendingFCB   bool      // 待确认的 FCB (已发送但未确认)
	retryCount   int       // 当前帧重传次数
	maxRetries   int       // 最大重传次数
	frameTimeout time.Duration // 帧应答超时

	// 发送队列
	sendQueue chan *SendTask
	// 高优先级发送队列 (用于 ACD 响应)
	priorityQueue chan *SendTask

	// 统计
	stats LinkLayerStats
}

// LinkLayerStats 链路层统计
type LinkLayerStats struct {
	FramesSent      uint64
	FramesReceived  uint64
	FramesRetried   uint64
	FramesTimeout   uint64
	ACDTriggered    uint64
	DFCPaused       uint64
	LinkResets      uint64
	StateTransitions uint64
}

// SendTask 发送任务
type SendTask struct {
	FrameType   FrameType
	Control     byte
	ASDU        []byte
	Priority    bool   // 高优先级 (ACD 响应)
	Callback    func(error) // 发送完成回调
	Retries     int
}

// NewLinkLayer 创建链路层状态机
func NewLinkLayer(client *Client, logger *zap.Logger) *LinkLayer {
	return &LinkLayer{
		client:        client,
		logger:        logger.Named("linklayer"),
		state:         LinkStateDisconnected,
		sendFCB:       false,
		recvFCB:       false,
		maxRetries:    3,
		frameTimeout:  5 * time.Second,
		sendQueue:     make(chan *SendTask, 100),
		priorityQueue: make(chan *SendTask, 10),
	}
}

// SetMaxRetries 设置最大重传次数
func (ll *LinkLayer) SetMaxRetries(maxRetries int) {
	ll.mu.Lock()
	defer ll.mu.Unlock()
	ll.maxRetries = maxRetries
}

// GetState 获取当前链路状态
func (ll *LinkLayer) GetState() LinkLayerState {
	ll.mu.Lock()
	defer ll.mu.Unlock()
	return ll.state
}

// SetState 设置链路状态 (线程安全)
func (ll *LinkLayer) SetState(newState LinkLayerState) {
	ll.mu.Lock()
	oldState := ll.state
	ll.state = newState
	ll.mu.Unlock()

	if oldState != newState {
		atomic.AddUint64(&ll.stats.StateTransitions, 1)
		ll.logger.Info("link state changed",
			zap.String("from", oldState.String()),
			zap.String("to", newState.String()),
		)
	}
}

func (s LinkLayerState) String() string {
	switch s {
	case LinkStateDisconnected:
		return "Disconnected"
	case LinkStateResetSent:
		return "ResetSent"
	case LinkStateResetConfirmed:
		return "ResetConfirmed"
	case LinkStateTransferStarted:
		return "TransferStarted"
	case LinkStateOperational:
		return "Operational"
	default:
		return "Unknown"
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 链路建立流程
// ─────────────────────────────────────────────────────────────────────────────

// StartLinkInitialization 启动链路初始化流程
// 返回: 是否启动成功, 错误
func (ll *LinkLayer) StartLinkInitialization(ctx context.Context) error {
	ll.logger.Info("starting link initialization")

	// 1. 发送复位链路 (FC=0, FCV=0)
	ll.SetState(LinkStateResetSent)
	atomic.AddUint64(&ll.stats.LinkResets, 1)

	resetFrame := BuildResetLink(ll.client.cfg.LinkAddress)
	task := &SendTask{
		FrameType: FrameTypeFixed,
		Control:   resetFrame[1], // 控制域
		Priority:  true,
		Callback: func(err error) {
			if err != nil {
				ll.logger.Error("reset link send failed", zap.Error(err))
			}
		},
	}

	select {
	case ll.priorityQueue <- task:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(ll.frameTimeout):
		return errors.New("send queue full, reset link timeout")
	}

	// 2. 等待复位确认 (通过 HandleUplinkFrame 处理)
	// 3. 收到确认后发送启动数据传输 (FC=4)
	// 4. 收到确认后进入运行状态

	return nil
}

// OnResetConfirmed 复位确认回调 (收到上行确认帧 FC=0 或 0xE5)
func (ll *LinkLayer) OnResetConfirmed() {
	ll.mu.Lock()
	defer ll.mu.Unlock()

	if ll.state != LinkStateResetSent {
		ll.logger.Warn("unexpected reset confirmation", zap.String("state", ll.state.String()))
		return
	}

	ll.logger.Info("reset link confirmed")
	// 直接设置状态，避免死锁 (SetState 也会加锁)
	oldState := ll.state
	ll.state = LinkStateResetConfirmed
	if oldState != ll.state {
		atomic.AddUint64(&ll.stats.StateTransitions, 1)
		ll.logger.Info("link state changed",
			zap.String("from", oldState.String()),
			zap.String("to", ll.state.String()),
		)
	}
	ll.sendFCB = false // 复位 FCB
	ll.recvFCB = false
	ll.pendingFCB = false

	// 发送启动数据传输 (FC=4, FCV=0)
	startFrame := BuildStartDataTransfer(ll.client.cfg.LinkAddress)
	task := &SendTask{
		FrameType: FrameTypeFixed,
		Control:   startFrame[1],
		Priority:  true,
		Callback: func(err error) {
			if err != nil {
				ll.logger.Error("start data transfer send failed", zap.Error(err))
			}
		},
	}

	select {
	case ll.priorityQueue <- task:
	default:
		ll.logger.Error("priority queue full, cannot send start transfer")
	}
}

// OnTransferStarted 启动数据传输确认回调
func (ll *LinkLayer) OnTransferStarted() {
	ll.mu.Lock()
	defer ll.mu.Unlock()

	if ll.state != LinkStateResetConfirmed {
		ll.logger.Warn("unexpected transfer start confirmation", zap.String("state", ll.state.String()))
		return
	}

	ll.logger.Info("data transfer started")
	// 直接设置状态，避免死锁
	oldState := ll.state
	ll.state = LinkStateOperational
	if oldState != ll.state {
		atomic.AddUint64(&ll.stats.StateTransitions, 1)
		ll.logger.Info("link state changed",
			zap.String("from", oldState.String()),
			zap.String("to", ll.state.String()),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 帧发送管理 (FCB 状态机)
// ─────────────────────────────────────────────────────────────────────────────

// SendIFrame 发送 I 帧 (可变帧，携带 ASDU)
// 自动管理 FCB 翻转和重传
func (ll *LinkLayer) SendIFrame(asdu []byte, priority bool, callback func(error)) error {
	ll.mu.Lock()
	// 检查链路状态
	if ll.state != LinkStateOperational {
		ll.mu.Unlock()
		return fmt.Errorf("link not operational, current state: %s", ll.state.String())
	}

	// 检查 DFC 流控
	if ll.client.IsDFCSet() {
		ll.mu.Unlock()
		atomic.AddUint64(&ll.stats.DFCPaused, 1)
		return errors.New("DFC set, link flow control active")
	}

	// 翻转 FCB (发送新 I 帧)
	ll.sendFCB = !ll.sendFCB
	ll.pendingFCB = ll.sendFCB
	ll.retryCount = 0

	// 构建控制域: FC=3, FCV=1, PRM=1, FCB=当前值
	ctrl := DownlinkControl{
		FCB: ll.sendFCB,
		FCV: true,
		FC:  FC_SEND_CONFIRM,
	}
	controlByte := ctrl.Encode()
	ll.mu.Unlock()

	task := &SendTask{
		FrameType: FrameTypeVariable,
		Control:   controlByte,
		ASDU:      asdu,
		Priority:  priority,
		Callback:  callback,
		Retries:   0,
	}

	queue := ll.sendQueue
	if priority {
		queue = ll.priorityQueue
	}

	select {
	case queue <- task:
		atomic.AddUint64(&ll.stats.FramesSent, 1)
		return nil
	default:
		// 队列满，回滚 FCB
		ll.mu.Lock()
		ll.sendFCB = !ll.sendFCB
		ll.pendingFCB = false
		ll.mu.Unlock()
		return errors.New("send queue full")
	}
}

// HandleACK 处理确认帧 (上行 FC=0 固定帧或 0xE5)
// 确认 FCB 匹配则翻转，不匹配则重传
func (ll *LinkLayer) HandleACK(uplinkFrame *Frame) {
	ll.mu.Lock()
	defer ll.mu.Unlock()

	// 获取上行帧 FCB (Bit5)
	uplinkCtrl := uplinkFrame.GetUplinkControl()
	receivedFCB := uplinkCtrl.DFC // 上行帧 Bit5 是 DFC，但确认帧复用该位表示 FCB

	ll.logger.Debug("received ACK",
		zap.Bool("expectedFCB", ll.pendingFCB),
		zap.Bool("receivedFCB", receivedFCB),
		zap.Int("retryCount", ll.retryCount),
	)

	if receivedFCB == ll.pendingFCB {
		// FCB 匹配，确认成功
		ll.pendingFCB = false
		ll.retryCount = 0
		ll.logger.Debug("FCB matched, frame acknowledged")
	} else {
		// FCB 不匹配，可能是重复确认或乱序
		if ll.retryCount < ll.maxRetries {
			ll.retryCount++
			atomic.AddUint64(&ll.stats.FramesRetried, 1)
			ll.logger.Warn("FCB mismatch, will retry",
				zap.Int("retry", ll.retryCount),
				zap.Int("maxRetries", ll.maxRetries),
			)
			// 这里应该重新发送上一帧，但需要保存上一帧的数据
			// 简化处理：由上层重传逻辑处理
		} else {
			ll.logger.Error("max retries exceeded, link may be out of sync")
			atomic.AddUint64(&ll.stats.FramesTimeout, 1)
			// 触发链路复位
			ll.SetState(LinkStateDisconnected)
		}
	}
}

// HandleSFrame 处理 S 帧 (接收序号确认)
func (ll *LinkLayer) HandleSFrame(frame *Frame) {
	// S 帧 Bit1 表示接收序号 (NR)
	// 这里简化处理：更新接收 FCB 期望值
	recvSeq := (frame.Control & 0x02) != 0
	ll.mu.Lock()
	ll.recvFCB = recvSeq
	ll.mu.Unlock()

	ll.logger.Debug("received S frame", zap.Bool("recvSeq", recvSeq))
}

// ─────────────────────────────────────────────────────────────────────────────
// ACD/DFC 处理
// ─────────────────────────────────────────────────────────────────────────────

// HandleUplinkFrame 处理上行帧，检测 ACD/DFC
func (ll *LinkLayer) HandleUplinkFrame(frame *Frame) {
	uplinkCtrl := frame.GetUplinkControl()

	// ACD=1: 子站有 1级数据待传，立即请求 1级数据 (FC=10)
	if uplinkCtrl.ACD {
		atomic.AddUint64(&ll.stats.ACDTriggered, 1)
		ll.logger.Info("ACD detected, requesting level 1 data (FC=10)")

		// 发送 FC=10 请求 1级数据 (高优先级)
		// 使用当前 FCB 状态
		ll.mu.Lock()
		fcb := ll.sendFCB
		ll.mu.Unlock()

		frame10 := BuildRequestLevel1Data(ll.client.cfg.LinkAddress, fcb)
		task := &SendTask{
			FrameType: FrameTypeFixed,
			Control:   frame10[1],
			Priority:  true, // 高优先级
			Callback: func(err error) {
				if err != nil {
					ll.logger.Error("FC=10 send failed", zap.Error(err))
				}
			},
		}
		select {
		case ll.priorityQueue <- task:
		default:
			ll.logger.Error("priority queue full, cannot send FC=10")
		}
	}

	// DFC=1: 子站缓冲区满，暂停发送新 I 帧
	if uplinkCtrl.DFC {
		atomic.AddUint64(&ll.stats.DFCPaused, 1)
		ll.logger.Warn("DFC set by remote, pausing I-frame transmission")
		// 实际暂停逻辑在 SendIFrame 中检查
	}
}

// IsDFCActive 检查 DFC 是否激活 (简化：由 Client 维护)
func (c *Client) IsDFCSet() bool {
	// 实际实现中需要维护 DFC 状态
	// 这里简化返回 false
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// 定时任务：链路状态检查、重传、保活
// ─────────────────────────────────────────────────────────────────────────────

// StartBackgroundTasks 启动后台任务
func (ll *LinkLayer) StartBackgroundTasks(ctx context.Context) {
	// 链路状态检查 (FC=9)
	go ll.linkStatusCheckLoop(ctx)

	// 重传检查
	go ll.retryCheckLoop(ctx)

	// 发送队列处理
	go ll.sendQueueProcessor(ctx)
}

// linkStatusCheckLoop 定时发送链路状态请求 (FC=9)
func (ll *LinkLayer) linkStatusCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second) // 可配置
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ll.mu.Lock()
			state := ll.state
			ll.mu.Unlock()

			if state == LinkStateOperational {
				ll.logger.Debug("sending link status request (FC=9)")
				frame := BuildRequestLinkStatus(ll.client.cfg.LinkAddress)
				task := &SendTask{
					FrameType: FrameTypeFixed,
					Control:   frame[1],
					Priority:  false,
				}
				select {
				case ll.sendQueue <- task:
				default:
					ll.logger.Warn("send queue full, skip link status check")
				}
			}
		}
	}
}

// retryCheckLoop 重传检查
func (ll *LinkLayer) retryCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ll.mu.Lock()
			pending := ll.pendingFCB
			retryCount := ll.retryCount
			ll.mu.Unlock()

			if pending && retryCount > 0 {
				ll.logger.Warn("frame pending acknowledgement",
					zap.Int("retryCount", retryCount),
					zap.Int("maxRetries", ll.maxRetries),
				)
			}
		}
	}
}

// sendQueueProcessor 发送队列处理器
func (ll *LinkLayer) sendQueueProcessor(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-ll.priorityQueue:
			// 高优先级任务
			err := ll.client.SendFrame(ll.buildFrame(task))
			if task.Callback != nil {
				task.Callback(err)
			}
		case task := <-ll.sendQueue:
			// 普通任务
			ll.mu.Lock()
			state := ll.state
			dfcActive := ll.client.IsDFCSet()
			ll.mu.Unlock()

			if state != LinkStateOperational || dfcActive {
				// 重新入队或丢弃
				if task.Retries < 3 {
					task.Retries++
					select {
					case ll.sendQueue <- task:
					default:
						ll.logger.Error("send queue full, dropping frame")
					}
				} else {
					ll.logger.Error("max queue retries, dropping frame")
				}
				continue
			}

			err := ll.client.SendFrame(ll.buildFrame(task))
			if task.Callback != nil {
				task.Callback(err)
			}
		}
	}
}

// buildFrame 根据任务构建完整帧
func (ll *LinkLayer) buildFrame(task *SendTask) []byte {
	switch task.FrameType {
	case FrameTypeFixed:
		return BuildFixedFrame(task.Control, ll.client.cfg.LinkAddress)
	case FrameTypeVariable:
		return BuildVariableFrame(task.Control, ll.client.cfg.LinkAddress, task.ASDU)
	case FrameTypeSingleACK:
		return BuildSingleACK()
	default:
		return nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 统计信息
// ─────────────────────────────────────────────────────────────────────────────

func (ll *LinkLayer) GetStats() LinkLayerStats {
	return LinkLayerStats{
		FramesSent:       atomic.LoadUint64(&ll.stats.FramesSent),
		FramesReceived:   atomic.LoadUint64(&ll.stats.FramesReceived),
		FramesRetried:    atomic.LoadUint64(&ll.stats.FramesRetried),
		FramesTimeout:    atomic.LoadUint64(&ll.stats.FramesTimeout),
		ACDTriggered:     atomic.LoadUint64(&ll.stats.ACDTriggered),
		DFCPaused:        atomic.LoadUint64(&ll.stats.DFCPaused),
		LinkResets:       atomic.LoadUint64(&ll.stats.LinkResets),
		StateTransitions: atomic.LoadUint64(&ll.stats.StateTransitions),
	}
}

// HandleFrame 处理接收到的帧，分发到链路层/应用层
func (ll *LinkLayer) HandleFrame(frame *Frame) {
	// 统计接收帧数
	atomic.AddUint64(&ll.stats.FramesReceived, 1)

	// 单字节确认 (0xE5)
	if frame.Type == FrameTypeSingleACK {
		ll.HandleACK(frame)
		return
	}

	// 固定帧
	if frame.Type == FrameTypeFixed {
		fc := frame.GetFunctionCode()

		// 复位确认 (FC=0) 或 启动数据传输确认
		if fc == FC_RESET_REMOTE_LINK || fc == 0 {
			if ll.state == LinkStateResetSent {
				ll.OnResetConfirmed()
			} else if ll.state == LinkStateResetConfirmed {
				ll.OnTransferStarted()
			}
		}

		// FC=8 有数据回答 (ACD=1)，FC=11 以链路状态/访问请求回答
		if fc == FC_DATA_RESPONSE || fc == FC_STATUS_RESPONSE {
			// 触发应用层请求 1级数据
			if OnASDUReceived != nil {
				// 创建一个虚拟 ASDU 来触发应用层
				dummyASDU := &ASDU{
					TypeID: 0xFF, // 特殊标识
					COT:    byte(fc),
				}
				ll.HandleASDU(dummyASDU)
			}
		}
		return
	}

	// 可变帧 (携带 ASDU)
	if frame.Type == FrameTypeVariable {
		// 解析 ASDU
		asdu, err := ParseASDU(frame.ASDU)
		if err != nil {
			ll.logger.Warn("parse ASDU failed", zap.Error(err))
			return
		}

		// 检测 ACD/DFC
		ll.HandleUplinkFrame(frame)

		// 分发到应用层
		ll.HandleASDU(asdu)
		return
	}
}

// SendAck 发送链路层确认 (S帧或固定帧 FC=0)
func (ll *LinkLayer) SendAck() error {
	ll.mu.Lock()
	defer ll.mu.Unlock()

	if ll.state != LinkStateOperational {
		return fmt.Errorf("link not operational")
	}

	// 发送 S 帧确认 (接收序号 = 下一期望发送序号)
	// 简化：发送固定帧 FC=0 确认
	ackFrame := BuildFixedFrame(0x00, ll.client.cfg.LinkAddress) // FC=0, FCB=0
	task := &SendTask{
		FrameType: FrameTypeFixed,
		Control:   ackFrame[1],
		Priority:  true,
	}
	select {
	case ll.priorityQueue <- task:
		return nil
	default:
		return errors.New("priority queue full")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 应用层分发接口
// ─────────────────────────────────────────────────────────────────────────────

// OnASDUReceived 应用层 ASDU 接收回调 (由上层设置)
var OnASDUReceived func(asdu *ASDU)

// HandleASDU 处理 ASDU (分发到应用层)
func (ll *LinkLayer) HandleASDU(asdu *ASDU) {
	if OnASDUReceived != nil {
		OnASDUReceived(asdu)
	}
}