package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const (
	cardkitThrottle              = 500 * time.Millisecond
	feishuCardJSONSoftLimitBytes = 2_800_000
)

type feishuStream struct {
	mu                      sync.Mutex
	ioMu                    sync.Mutex
	cardKit                 cardKitClient
	taskCards               *taskCardRegistry
	cardID                  string
	title                   string
	sequence                int
	lastUpdate              time.Time
	lastContent             string
	lastSummary             string
	collapsible             bool
	closed                  bool
	throttle                time.Duration
	now                     func() time.Time
	pendingCtx              context.Context
	pendingText             string
	hasPending              bool
	pendingTimer            *time.Timer
	pendingGeneration       uint64
	pendingPresentation     platform.StreamPresentation
	hasPendingPresentation  bool
	presentationTimer       *time.Timer
	cardJSONSoftLimitBytes  int
	preserveTerminalContent bool
	inlineActiveStatus      bool
	preservedApprovals      []string
	terminal                *platform.TerminalCheckpoint
	terminalDelivered       bool
}

type feishuStreamUpdateOp struct {
	content       string
	summary       string
	detailsSeq    int
	summarySeq    int
	streamSeq     int
	taskCardJSON  string
	taskUpdateSeq int
}

func (s *feishuStream) PreflightPresentation(p platform.StreamPresentation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	opts := cardOptions{Status: cardStatusThinking, Title: s.title, Summary: p.Summary, Content: p.Details, InlineActiveStatus: s.inlineActiveStatus, Collapsible: s.collapsible, Expanded: true}
	if s.taskCards != nil {
		if snap, ok := s.taskCards.snapshot(s.cardID); ok {
			opts = snap
			opts.Summary = p.Summary
			opts.Content = p.Details
		}
	}
	opts.Collapsible = true
	opts.Expanded = true
	raw, err := buildCardV2(opts)
	if err != nil {
		return err
	}
	limit := s.cardJSONSoftLimitBytes
	if limit <= 0 {
		limit = feishuCardJSONSoftLimitBytes
	}
	if len([]byte(raw)) > limit {
		return fmt.Errorf("%w: rendered=%d bytes soft_limit=%d bytes", platform.ErrStreamContentTooLarge, len([]byte(raw)), limit)
	}
	return nil
}

func (s *feishuStream) UpdatePresentation(ctx context.Context, p platform.StreamPresentation) error {
	if err := s.PreflightPresentation(p); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if delay := s.throttleDelay(s.now()); delay > 0 {
		s.pendingPresentation, s.hasPendingPresentation = p, true
		if s.presentationTimer == nil {
			s.presentationTimer = time.AfterFunc(delay, func() { s.flushPresentation() })
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.updatePresentationNow(ctx, p)
}

func (s *feishuStream) flushPresentation() {
	s.mu.Lock()
	if s.closed || !s.hasPendingPresentation {
		s.presentationTimer = nil
		s.mu.Unlock()
		return
	}
	p := s.pendingPresentation
	s.hasPendingPresentation = false
	s.presentationTimer = nil
	s.mu.Unlock()
	if err := s.updatePresentationNow(context.Background(), p); err != nil {
		log.Printf("[feishu] failed to flush presentation: %v", err)
	}
}

func (s *feishuStream) updatePresentationNow(ctx context.Context, p platform.StreamPresentation) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	op := feishuStreamUpdateOp{summary: p.Summary, content: p.Details}
	if !s.collapsible {
		opts := cardOptions{
			Status: cardStatusThinking, Title: s.title, Summary: p.Summary, Content: p.Details,
			Collapsible: true, Expanded: true, InlineActiveStatus: s.inlineActiveStatus,
			Approvals: append([]string(nil), s.preservedApprovals...),
		}
		if s.taskCards != nil {
			if snapshot, sequence, ok := s.taskCards.enableStructuredPresentationWithSequence(s.cardID, p.Summary, p.Details); ok {
				opts = snapshot
				op.taskUpdateSeq = sequence
				s.sequence = sequence
			}
		}
		if op.taskUpdateSeq == 0 {
			op.taskUpdateSeq = s.nextSequence()
		}
		cardJSON, err := buildCardV2(opts)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		op.taskCardJSON = cardJSON
	} else if s.taskCards != nil {
		_, a, b, ok := s.taskCards.updatePresentationWithSequences(s.cardID, p.Summary, p.Details)
		if ok {
			op.summarySeq, op.detailsSeq = a, b
			s.sequence = b
		}
	}
	s.lastSummary = p.Summary
	s.lastContent = p.Details
	s.lastUpdate = s.now()
	s.mu.Unlock()
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	if op.taskCardJSON != "" {
		err := s.cardKit.UpdateCard(ctx, s.cardID, op.taskCardJSON, op.taskUpdateSeq)
		if ignored := ignoreCardKitUpdateError(err); ignored != nil {
			return ignored
		}
		if err != nil {
			log.Printf("[feishu] ignored non-fatal structured layout update error: %v", err)
		}
		s.mu.Lock()
		s.collapsible = true
		s.mu.Unlock()
		return nil
	}
	if op.summarySeq == 0 {
		op.summarySeq = s.nextSequence()
		op.detailsSeq = s.nextSequence()
	}
	if err := s.streamComponentWithRetry(ctx, cardProgressSummaryID, op.summary, op.summarySeq); err != nil {
		return err
	}
	return s.streamComponentWithRetry(ctx, cardMainContentID, op.content, op.detailsSeq)
}

func (s *feishuStream) streamComponentWithRetry(ctx context.Context, elementID, content string, sequence int) error {
	err := s.cardKit.StreamContent(ctx, s.cardID, elementID, content, sequence)
	if shouldReenableStreaming(err) {
		s.mu.Lock()
		enable, retry := s.nextSequence(), s.nextSequence()
		s.mu.Unlock()
		if e := s.cardKit.SetStreaming(ctx, s.cardID, true, enable); e != nil {
			return ignoreCardKitUpdateError(e)
		}
		err = s.cardKit.StreamContent(ctx, s.cardID, elementID, content, retry)
	}
	return err
}

type feishuStreamTerminalOp struct {
	CardID           string `json:"card_id"`
	Status           string `json:"status,omitempty"`
	DisableSeq       int    `json:"disable_sequence"`
	DisableOperation string `json:"disable_operation"`
	UpdateSeq        int    `json:"update_sequence"`
	UpdateOperation  string `json:"update_operation"`
	CardJSON         string `json:"card_json"`
}

const feishuTerminalCheckpointKind = "feishu.cardkit.terminal.v1"
const feishuSupersedeCheckpointKind = "feishu.cardkit.supersede.v1"
const feishuStreamReferenceKind = "feishu.cardkit.stream.v1"

type feishuStreamReferencePayload struct {
	CardID      string   `json:"card_id"`
	Title       string   `json:"title"`
	Sequence    int      `json:"sequence"`
	Content     string   `json:"content,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Details     string   `json:"details,omitempty"`
	Collapsible bool     `json:"collapsible,omitempty"`
	Approvals   []string `json:"approvals,omitempty"`
}

const defaultSupersededTaskCardNotice = "已在新位置继续展示；后续结构化进展将更新到新卡片，最终结果会另发独立结果卡片。"

// openCardKitStream 创建并发送 CardKit 卡片，然后开启流式模式。
func (r *Replier) openCardKitStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	return r.openCardKitStreamWithMode(ctx, opts, false)
}

func (r *Replier) openTaskCardKitStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	return r.openCardKitStreamWithMode(ctx, opts, true)
}

func (r *Replier) openCardKitStreamWithMode(ctx context.Context, opts platform.StreamOptions, trackTask bool) (platform.Stream, error) {
	initialSummary, initialDetails := "", opts.InitialContent
	if opts.InitialPresentation != nil {
		initialSummary, initialDetails = opts.InitialPresentation.Summary, opts.InitialPresentation.Details
	}
	cardJSON, err := buildCardV2(cardOptions{
		Status:  cardStatusThinking,
		Title:   opts.Title,
		Content: initialDetails, Summary: initialSummary,
		Collapsible: trackTask && opts.InitialPresentation != nil, Expanded: true,
		InlineActiveStatus: trackTask,
	})
	if err != nil {
		return nil, err
	}
	if len([]byte(cardJSON)) > feishuCardJSONSoftLimitBytes {
		return nil, fmt.Errorf("%w: rendered=%d bytes soft_limit=%d bytes", platform.ErrStreamContentTooLarge, len([]byte(cardJSON)), feishuCardJSONSoftLimitBytes)
	}
	cardID, err := r.cardKit.CreateCard(ctx, cardJSON)
	if err != nil {
		return nil, err
	}
	if err := r.sendCard(ctx, r.openID, cardID); err != nil {
		return nil, err
	}
	stream := &feishuStream{
		cardKit:     r.cardKit,
		taskCards:   r.taskCards,
		cardID:      cardID,
		title:       opts.Title,
		throttle:    cardkitThrottle,
		now:         time.Now,
		lastContent: initialDetails, lastSummary: initialSummary,
		collapsible:             trackTask && opts.InitialPresentation != nil,
		cardJSONSoftLimitBytes:  feishuCardJSONSoftLimitBytes,
		preserveTerminalContent: trackTask,
		inlineActiveStatus:      trackTask,
	}
	if err := stream.cardKit.SetStreaming(ctx, stream.cardID, true, stream.nextSequence()); err != nil {
		return nil, err
	}
	if trackTask {
		r.setCurrentTaskCardID(cardID)
		if r.taskCards != nil {
			r.taskCards.recordWithSequence(cardID, cardOptions{
				Status:  cardStatusThinking,
				Title:   opts.Title,
				Content: initialDetails, Summary: initialSummary, Collapsible: stream.collapsible, Expanded: true,
				InlineActiveStatus: trackTask,
			}, stream.sequence)
			if stream.collapsible {
				boundOpts, ok := r.taskCards.snapshot(cardID)
				if !ok {
					return nil, fmt.Errorf("task card state is unavailable after creation")
				}
				boundCardJSON, buildErr := buildCardV2(boundOpts)
				if buildErr != nil {
					return nil, buildErr
				}
				if len([]byte(boundCardJSON)) > feishuCardJSONSoftLimitBytes {
					return nil, fmt.Errorf("%w: rendered=%d bytes soft_limit=%d bytes", platform.ErrStreamContentTooLarge, len([]byte(boundCardJSON)), feishuCardJSONSoftLimitBytes)
				}
				if updateErr := stream.cardKit.UpdateCard(ctx, cardID, boundCardJSON, stream.nextSequence()); updateErr != nil {
					return nil, updateErr
				}
			}
		}
	}
	return stream, nil
}

// PreflightUpdate 使用将要提交的完整卡片 JSON 做保守字节上限检查。
func (s *feishuStream) PreflightUpdate(content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	opts := cardOptions{
		Status: cardStatusThinking, Title: s.title, Content: content,
		InlineActiveStatus: s.inlineActiveStatus,
	}
	if s.taskCards != nil {
		if snapshot, ok := s.taskCards.snapshot(s.cardID); ok {
			snapshot.Content = content
			opts = snapshot
		}
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		return err
	}
	limit := s.cardJSONSoftLimitBytes
	if limit <= 0 {
		limit = feishuCardJSONSoftLimitBytes
	}
	if len([]byte(cardJSON)) > limit {
		return fmt.Errorf("%w: rendered=%d bytes soft_limit=%d bytes", platform.ErrStreamContentTooLarge, len([]byte(cardJSON)), limit)
	}
	return nil
}

// Update 节流更新主内容组件，触发飞书打字机效果。
func (s *feishuStream) Update(ctx context.Context, content string) error {
	if err := s.PreflightUpdate(content); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if content == s.lastContent {
		if s.hasPending {
			s.cancelPendingUpdate()
		}
		s.mu.Unlock()
		return nil
	}
	now := s.now()
	if delay := s.throttleDelay(now); delay > 0 {
		s.queuePendingUpdate(ctx, content, delay)
		s.mu.Unlock()
		return nil
	}
	s.cancelPendingUpdate()
	op, err := s.prepareUpdateNowLocked(content, now)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.runUpdateNow(ctx, op)
}

func (s *feishuStream) prepareUpdateNowLocked(content string, now time.Time) (feishuStreamUpdateOp, error) {
	op := feishuStreamUpdateOp{content: content}
	if s.collapsible {
		op.streamSeq = s.nextSequence()
		s.lastUpdate = now
		s.lastContent = content
		if s.taskCards != nil {
			s.taskCards.updateContent(s.cardID, content)
		}
		return op, nil
	}
	if s.taskCards != nil {
		opts, sequence, ok := s.taskCards.updateContentWithSequence(s.cardID, content)
		if ok {
			cardJSON, err := buildCardV2(opts)
			if err != nil {
				return feishuStreamUpdateOp{}, err
			}
			op.taskCardJSON = cardJSON
			op.taskUpdateSeq = sequence
			s.sequence = sequence
		} else {
			op.streamSeq = s.nextSequence()
		}
	} else {
		op.streamSeq = s.nextSequence()
	}
	s.lastUpdate = now
	s.lastContent = content
	return op, nil
}

func (s *feishuStream) runUpdateNow(ctx context.Context, op feishuStreamUpdateOp) error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil
	}
	if op.taskCardJSON != "" {
		err := s.cardKit.UpdateCard(ctx, s.cardID, op.taskCardJSON, op.taskUpdateSeq)
		if ignored := ignoreCardKitUpdateError(err); ignored != nil {
			return ignored
		}
		if err != nil {
			log.Printf("[feishu] ignored non-fatal task card update error: %v", err)
		}
		return nil
	}
	if s.collapsible {
		return s.cardKit.StreamContent(ctx, s.cardID, cardMainContentID, op.content, op.streamSeq)
	}
	err := s.cardKit.StreamContent(ctx, s.cardID, cardMainContentID, op.content, op.streamSeq)
	if shouldReenableStreaming(err) {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil
		}
		enableSeq := s.nextSequence()
		retrySeq := s.nextSequence()
		s.mu.Unlock()
		if enableErr := s.cardKit.SetStreaming(ctx, s.cardID, true, enableSeq); enableErr != nil {
			return ignoreCardKitUpdateError(enableErr)
		}
		err = s.cardKit.StreamContent(ctx, s.cardID, cardMainContentID, op.content, retrySeq)
	}
	if ignored := ignoreCardKitUpdateError(err); ignored != nil {
		return ignored
	}
	if err != nil {
		log.Printf("[feishu] ignored non-fatal card stream update error: %v", err)
	}
	return nil
}

func (s *feishuStream) throttleDelay(now time.Time) time.Duration {
	if s.throttle <= 0 || s.lastUpdate.IsZero() {
		return 0
	}
	elapsed := now.Sub(s.lastUpdate)
	if elapsed >= s.throttle {
		return 0
	}
	if elapsed < 0 {
		return s.throttle
	}
	return s.throttle - elapsed
}

func (s *feishuStream) queuePendingUpdate(ctx context.Context, content string, delay time.Duration) {
	s.pendingCtx = ctx
	s.pendingText = content
	s.hasPending = true
	if s.pendingTimer != nil {
		return
	}
	s.schedulePendingUpdate(delay)
}

func (s *feishuStream) schedulePendingUpdate(delay time.Duration) {
	s.pendingGeneration++
	generation := s.pendingGeneration
	s.pendingTimer = time.AfterFunc(delay, func() {
		s.flushPendingUpdate(generation)
	})
}

func (s *feishuStream) flushPendingUpdate(generation uint64) {
	s.mu.Lock()
	if s.closed || generation != s.pendingGeneration || s.pendingTimer == nil {
		s.mu.Unlock()
		return
	}
	s.pendingTimer = nil
	if !s.hasPending {
		s.mu.Unlock()
		return
	}
	now := s.now()
	if delay := s.throttleDelay(now); delay > 0 {
		s.schedulePendingUpdate(delay)
		s.mu.Unlock()
		return
	}
	ctx := s.pendingCtx
	if ctx == nil {
		ctx = context.Background()
	}
	content := s.pendingText
	s.pendingCtx = nil
	s.pendingText = ""
	s.hasPending = false
	op, err := s.prepareUpdateNowLocked(content, now)
	s.mu.Unlock()
	if err != nil {
		log.Printf("[feishu] failed to build latest throttled card update: %v", err)
		return
	}
	if err := s.runUpdateNow(ctx, op); err != nil {
		log.Printf("[feishu] failed to flush latest throttled card update: %v", err)
	}
}

func (s *feishuStream) cancelPendingUpdate() {
	s.pendingGeneration++
	if s.pendingTimer != nil {
		s.pendingTimer.Stop()
	}
	s.pendingTimer = nil
	s.pendingCtx = nil
	s.pendingText = ""
	s.hasPending = false
}

func (s *feishuStream) cancelPendingPresentation() {
	if s.presentationTimer != nil {
		s.presentationTimer.Stop()
	}
	s.presentationTimer = nil
	s.pendingPresentation = platform.StreamPresentation{}
	s.hasPendingPresentation = false
}

// Complete 关闭流式并全量更新为完成卡片。
func (s *feishuStream) Complete(ctx context.Context, finalContent string) error {
	checkpoint, err := s.PrepareTerminalWithState(finalContent, platform.StreamTerminalCompleted)
	if err != nil || checkpoint.Kind == "" {
		return err
	}
	return s.deliverPreparedTerminal(ctx, checkpoint)
}

// DurableReference 导出仍在进行中的 CardKit 卡片，供新进程生成同卡终态。
func (s *feishuStream) DurableReference() (platform.DurableStreamReference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.terminal != nil || s.terminalDelivered {
		return platform.DurableStreamReference{}, fmt.Errorf("Feishu stream is already terminal")
	}
	if s.cardID == "" || s.title == "" || s.sequence <= 0 {
		return platform.DurableStreamReference{}, fmt.Errorf("Feishu stream reference is incomplete")
	}
	summary := strings.TrimSpace(s.lastSummary)
	details := trimTaskStreamThinkingIndicator(s.lastContent)
	collapsible := s.collapsible
	approvals := append([]string(nil), s.preservedApprovals...)
	if s.taskCards != nil {
		if snapshot, sequence, ok := s.taskCards.snapshotWithSequence(s.cardID); ok {
			summary = strings.TrimSpace(snapshot.Summary)
			details = trimTaskStreamThinkingIndicator(snapshot.Content)
			collapsible = snapshot.Collapsible
			approvals = append([]string(nil), snapshot.Approvals...)
			if sequence > s.sequence {
				s.sequence = sequence
			}
		}
	}
	if s.hasPending && strings.TrimSpace(s.pendingText) != "" {
		details = trimTaskStreamThinkingIndicator(s.pendingText)
	}
	if s.hasPendingPresentation {
		summary = strings.TrimSpace(s.pendingPresentation.Summary)
		details = trimTaskStreamThinkingIndicator(s.pendingPresentation.Details)
	}
	summary, details = normalizeFeishuReferencePresentation(summary, details, details)
	payload, err := json.Marshal(feishuStreamReferencePayload{
		CardID: s.cardID, Title: s.title, Sequence: s.sequence,
		Content: details, Summary: summary, Details: details, Collapsible: collapsible,
		Approvals: approvals,
	})
	if err != nil {
		return platform.DurableStreamReference{}, err
	}
	return platform.DurableStreamReference{Kind: feishuStreamReferenceKind, Payload: payload}, nil
}

// SetDurableReferenceChangeHandler 绑定审批等旁路状态变化的持久化刷新通知。
func (s *feishuStream) SetDurableReferenceChangeHandler(handler func()) {
	s.mu.Lock()
	taskCards := s.taskCards
	cardID := s.cardID
	closed := s.closed
	s.mu.Unlock()
	if taskCards == nil {
		return
	}
	if closed {
		handler = nil
	}
	taskCards.setDurableReferenceChangeHandler(cardID, handler)
}

// Fail 关闭流式并全量更新为失败卡片。
func (s *feishuStream) Fail(ctx context.Context, errText string) error {
	checkpoint, err := s.PrepareTerminalWithState(errText, platform.StreamTerminalFailed)
	if err != nil || checkpoint.Kind == "" {
		return err
	}
	return s.deliverPreparedTerminal(ctx, checkpoint)
}

// Stop 关闭流式并全量更新为用户主动停止卡片。
func (s *feishuStream) Stop(ctx context.Context, finalContent string) error {
	checkpoint, err := s.PrepareTerminalWithState(finalContent, platform.StreamTerminalStopped)
	if err != nil || checkpoint.Kind == "" {
		return err
	}
	return s.deliverPreparedTerminal(ctx, checkpoint)
}

// PrepareTerminalFromReference 在新进程中恢复卡片定位与序列，并生成同卡终态操作。
func (r *Replier) PrepareTerminalFromReference(reference platform.DurableStreamReference, finalContent string, failed bool) (platform.TerminalCheckpoint, error) {
	state := platform.StreamTerminalCompleted
	if failed {
		state = platform.StreamTerminalFailed
	}
	return r.PrepareTerminalFromReferenceWithState(reference, finalContent, state)
}

// PrepareTerminalFromReferenceWithState 在新进程中恢复原卡并保留停止终态样式。
func (r *Replier) PrepareTerminalFromReferenceWithState(reference platform.DurableStreamReference, finalContent string, state platform.StreamTerminalState) (platform.TerminalCheckpoint, error) {
	payload, err := decodeFeishuStreamReference(reference)
	if err != nil {
		return platform.TerminalCheckpoint{}, err
	}
	summary, details := normalizeFeishuReferencePresentation(payload.Summary, payload.Details, payload.Content)
	stream := &feishuStream{
		cardKit: r.cardKit, taskCards: r.taskCards,
		cardID: payload.CardID, title: payload.Title, sequence: payload.Sequence,
		lastContent: details, lastSummary: summary, collapsible: payload.Collapsible,
		preserveTerminalContent: true, inlineActiveStatus: true,
		preservedApprovals: append([]string(nil), payload.Approvals...),
		now:                time.Now,
	}
	if r.taskCards != nil {
		r.taskCards.recordWithSequence(payload.CardID, cardOptions{
			Status: cardStatusThinking, Title: payload.Title, Summary: summary, Content: details,
			Collapsible: payload.Collapsible, Expanded: true,
			Approvals: payload.Approvals, InlineActiveStatus: true,
		}, payload.Sequence)
	}
	// 恢复引用已经携带最后一次安全进度快照；finalContent 属于独立结果消息，不得写回任务卡。
	return stream.PrepareTerminalWithState("", state)
}

// PrepareSupersedeFromReference 根据已持久化的旧卡引用生成可幂等重放的收敛操作。
func (r *Replier) PrepareSupersedeFromReference(reference platform.DurableStreamReference, notice string, operationID string) (platform.SupersedeCheckpoint, error) {
	return prepareFeishuSupersedeFromReference(reference, notice, operationID)
}

// PrepareDetachFromReference 生成“解除当前窗口同步”的可重放卡片冻结操作。
func (r *Replier) PrepareDetachFromReference(reference platform.DurableStreamReference, notice string, operationID string) (platform.SupersedeCheckpoint, error) {
	return prepareFeishuDetachFromReference(reference, notice, operationID)
}

// Supersede 保留为没有 outbox 编排时的兼容路径。
func (s *feishuStream) Supersede(ctx context.Context, notice string) error {
	reference, err := s.DurableReference()
	if err != nil {
		s.mu.Lock()
		alreadyClosed := s.closed || s.terminal != nil || s.terminalDelivered
		s.mu.Unlock()
		if alreadyClosed {
			return nil
		}
		return err
	}
	checkpoint, err := prepareFeishuSupersedeFromReference(reference, notice, uuid.NewString())
	if err != nil {
		return err
	}
	return s.DeliverPreparedSupersede(ctx, checkpoint)
}

// Detach 冻结当前窗口的任务卡，但不把共享任务标记为终态或迁移到新卡。
func (s *feishuStream) Detach(ctx context.Context, notice string) error {
	reference, err := s.DurableReference()
	if err != nil {
		s.mu.Lock()
		alreadyClosed := s.closed || s.terminal != nil || s.terminalDelivered
		s.mu.Unlock()
		if alreadyClosed {
			return nil
		}
		return err
	}
	checkpoint, err := prepareFeishuDetachFromReference(reference, notice, uuid.NewString())
	if err != nil {
		return err
	}
	return s.DeliverPreparedSupersede(ctx, checkpoint)
}

// DeliverPreparedSupersede 先冻结旧 stream，再投递已经持久化的收敛操作。
func (s *feishuStream) DeliverPreparedSupersede(ctx context.Context, checkpoint platform.SupersedeCheckpoint) error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()

	s.mu.Lock()
	if s.terminal != nil || s.terminalDelivered {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancelPendingUpdate()
	s.cancelPendingPresentation()
	taskCards, cardID := s.taskCards, s.cardID
	s.mu.Unlock()

	if taskCards != nil {
		status := cardStatusSuperseded
		var op feishuStreamTerminalOp
		if json.Unmarshal(checkpoint.Payload, &op) == nil && op.Status == cardStatusDetached {
			status = cardStatusDetached
		}
		taskCards.updateAndSnapshot(cardID, status, "", false)
		taskCards.setDurableReferenceChangeHandler(cardID, nil)
	}
	return deliverFeishuSupersedeCheckpoint(ctx, s.cardKit, checkpoint)
}

func (s *feishuStream) prepareSupersedeUpdate(notice string) (feishuStreamTerminalOp, error) {
	reference, err := s.DurableReference()
	if err != nil {
		return feishuStreamTerminalOp{}, err
	}
	checkpoint, err := prepareFeishuSupersedeFromReference(reference, notice, uuid.NewString())
	if err != nil {
		return feishuStreamTerminalOp{}, err
	}
	var op feishuStreamTerminalOp
	if err := json.Unmarshal(checkpoint.Payload, &op); err != nil {
		return feishuStreamTerminalOp{}, err
	}
	return op, nil
}

func (s *feishuStream) deliverPreparedTerminal(ctx context.Context, checkpoint platform.TerminalCheckpoint) error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.mu.Lock()
	if s.terminalDelivered {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := deliverFeishuTerminalCheckpoint(ctx, s.cardKit, checkpoint); err != nil {
		return err
	}
	s.mu.Lock()
	s.terminalDelivered = true
	s.mu.Unlock()
	return nil
}

// PrepareTerminal 冻结流并导出可跨进程重放的 CardKit 终态操作。
func (s *feishuStream) PrepareTerminal(finalContent string, failed bool) (platform.TerminalCheckpoint, error) {
	state := platform.StreamTerminalCompleted
	if failed {
		state = platform.StreamTerminalFailed
	}
	return s.PrepareTerminalWithState(finalContent, state)
}

// PrepareTerminalWithState 冻结流并保留完成、失败、停止三种终态语义。
func (s *feishuStream) PrepareTerminalWithState(finalContent string, state platform.StreamTerminalState) (platform.TerminalCheckpoint, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.mu.Lock()
	if s.terminal != nil {
		checkpoint := *s.terminal
		s.mu.Unlock()
		return checkpoint, nil
	}
	if s.closed {
		s.mu.Unlock()
		return platform.TerminalCheckpoint{}, nil
	}
	status := cardStatusDone
	switch state {
	case platform.StreamTerminalCompleted:
	case platform.StreamTerminalFailed:
		status = cardStatusError
	case platform.StreamTerminalStopped:
		status = cardStatusStopped
	default:
		s.mu.Unlock()
		return platform.TerminalCheckpoint{}, fmt.Errorf("unsupported stream terminal state %q", state)
	}
	s.closed = true
	s.cancelPendingUpdate()
	s.cancelPendingPresentation()
	s.mu.Unlock()
	op, err := s.prepareTerminalUpdate(status, finalContent)
	if err != nil {
		return platform.TerminalCheckpoint{}, err
	}
	payload, err := json.Marshal(op)
	if err != nil {
		return platform.TerminalCheckpoint{}, err
	}
	checkpoint := platform.TerminalCheckpoint{Kind: feishuTerminalCheckpointKind, Payload: payload}
	s.mu.Lock()
	s.terminal = &checkpoint
	s.mu.Unlock()
	return checkpoint, nil
}

func (s *feishuStream) prepareTerminalUpdate(status string, content string) (feishuStreamTerminalOp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	opts := cardOptions{
		Status: status, Title: s.title, Summary: s.lastSummary, Content: content,
		Collapsible: s.collapsible, Expanded: false,
		InlineActiveStatus: s.inlineActiveStatus,
	}
	if s.preserveTerminalContent {
		opts.Content = strings.TrimSpace(content)
		if opts.Content == "" {
			opts.Content = trimTaskStreamThinkingIndicator(s.lastContent)
		}
		opts.Approvals = append([]string(nil), s.preservedApprovals...)
	}
	if s.taskCards != nil {
		if snapshot, ok := s.taskCards.updateAndSnapshot(s.cardID, status, opts.Content, s.preserveTerminalContent); ok {
			opts = snapshot
			opts.Expanded = false
		}
	}
	if strings.TrimSpace(opts.Content) == "" && isCompactTerminalStatus(status) {
		opts.Summary = ""
		opts.Collapsible = false
	} else if s.collapsible {
		opts.Collapsible = true
		opts.Expanded = false
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		return feishuStreamTerminalOp{}, err
	}
	return feishuStreamTerminalOp{
		CardID:           s.cardID,
		DisableSeq:       s.nextSequence(),
		DisableOperation: uuid.NewString(),
		UpdateSeq:        s.nextSequence(),
		UpdateOperation:  uuid.NewString(),
		CardJSON:         cardJSON,
	}, nil
}

func trimTaskStreamThinkingIndicator(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimSpace(strings.TrimSuffix(content, platform.TaskStreamThinkingIndicator))
	return content
}

func decodeFeishuStreamReference(reference platform.DurableStreamReference) (feishuStreamReferencePayload, error) {
	if reference.Kind != feishuStreamReferenceKind {
		return feishuStreamReferencePayload{}, fmt.Errorf("unsupported Feishu stream reference %q", reference.Kind)
	}
	var payload feishuStreamReferencePayload
	if err := json.Unmarshal(reference.Payload, &payload); err != nil {
		return feishuStreamReferencePayload{}, fmt.Errorf("decode Feishu stream reference: %w", err)
	}
	if strings.TrimSpace(payload.CardID) == "" || strings.TrimSpace(payload.Title) == "" || payload.Sequence <= 0 {
		return feishuStreamReferencePayload{}, fmt.Errorf("invalid Feishu stream reference")
	}
	return payload, nil
}

func normalizeFeishuReferencePresentation(summary, details, legacyContent string) (string, string) {
	legacyContent = trimTaskStreamThinkingIndicator(legacyContent)
	summary = strings.TrimSpace(summary)
	details = trimTaskStreamThinkingIndicator(details)
	if summary == "" {
		summary = legacyContent
	}
	if details == "" {
		details = legacyContent
	}
	if summary == "" {
		summary = details
	}
	return summary, details
}

func prepareFeishuSupersedeFromReference(reference platform.DurableStreamReference, notice string, operationID string) (platform.SupersedeCheckpoint, error) {
	return prepareFeishuStreamFreezeFromReference(reference, notice, operationID, cardStatusSuperseded)
}

func prepareFeishuDetachFromReference(reference platform.DurableStreamReference, notice string, operationID string) (platform.SupersedeCheckpoint, error) {
	return prepareFeishuStreamFreezeFromReference(reference, notice, operationID, cardStatusDetached)
}

func prepareFeishuStreamFreezeFromReference(reference platform.DurableStreamReference, notice string, operationID string, status string) (platform.SupersedeCheckpoint, error) {
	payload, err := decodeFeishuStreamReference(reference)
	if err != nil {
		return platform.SupersedeCheckpoint{}, err
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return platform.SupersedeCheckpoint{}, fmt.Errorf("Feishu supersede operation ID is required")
	}
	notice = strings.TrimSpace(notice)
	if notice == "" {
		if status == cardStatusDetached {
			notice = "已解除当前窗口的会话绑定；本地 Codex 任务继续运行。"
		} else {
			notice = defaultSupersededTaskCardNotice
		}
	}
	summary, details := normalizeFeishuReferencePresentation(payload.Summary, payload.Details, payload.Content)
	if !strings.Contains(details, notice) {
		if details == "" {
			details = notice
		} else {
			details += "\n\n---\n\n" + notice
		}
	}
	cardJSON, err := buildCardV2(cardOptions{
		Status: status, Title: payload.Title,
		Summary: summary, Content: details, Approvals: payload.Approvals,
		Collapsible: payload.Collapsible, Expanded: false, taskCardID: payload.CardID,
	})
	if err != nil {
		return platform.SupersedeCheckpoint{}, err
	}
	op := feishuStreamTerminalOp{
		CardID: payload.CardID, Status: status, DisableSeq: payload.Sequence + 1,
		DisableOperation: uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":disable")).String(),
		UpdateSeq:        payload.Sequence + 2,
		UpdateOperation:  uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":update")).String(),
		CardJSON:         cardJSON,
	}
	checkpointPayload, err := json.Marshal(op)
	if err != nil {
		return platform.SupersedeCheckpoint{}, err
	}
	return platform.SupersedeCheckpoint{Kind: feishuSupersedeCheckpointKind, Payload: checkpointPayload}, nil
}

func deliverFeishuSupersedeCheckpoint(ctx context.Context, client cardKitClient, checkpoint platform.SupersedeCheckpoint) error {
	if checkpoint.Kind != feishuSupersedeCheckpointKind {
		return fmt.Errorf("unsupported Feishu supersede checkpoint %q", checkpoint.Kind)
	}
	if client == nil {
		return fmt.Errorf("CardKit client is unavailable")
	}
	var op feishuStreamTerminalOp
	if err := json.Unmarshal(checkpoint.Payload, &op); err != nil {
		return fmt.Errorf("decode Feishu supersede checkpoint: %w", err)
	}
	if op.CardID == "" || op.DisableSeq <= 0 || op.UpdateSeq <= op.DisableSeq || op.DisableOperation == "" || op.UpdateOperation == "" || op.CardJSON == "" {
		return fmt.Errorf("invalid Feishu supersede checkpoint")
	}
	idempotent, ok := client.(idempotentCardKitClient)
	if !ok {
		return platform.ErrUnsupported
	}
	disableErr := idempotent.SetStreamingIdempotent(ctx, op.CardID, false, op.DisableSeq, op.DisableOperation)
	updateErr := idempotent.UpdateCardIdempotent(ctx, op.CardID, op.CardJSON, op.UpdateSeq, op.UpdateOperation)
	return firstErr(ignoreCardKitUpdateError(updateErr), ignoreCardKitUpdateError(disableErr))
}

func deliverFeishuTerminalCheckpoint(ctx context.Context, client cardKitClient, checkpoint platform.TerminalCheckpoint) error {
	if checkpoint.Kind != feishuTerminalCheckpointKind {
		return fmt.Errorf("unsupported Feishu terminal checkpoint %q", checkpoint.Kind)
	}
	if client == nil {
		return fmt.Errorf("CardKit client is unavailable")
	}
	var op feishuStreamTerminalOp
	if err := json.Unmarshal(checkpoint.Payload, &op); err != nil {
		return fmt.Errorf("decode Feishu terminal checkpoint: %w", err)
	}
	if op.CardID == "" || op.DisableSeq <= 0 || op.UpdateSeq <= op.DisableSeq || op.DisableOperation == "" || op.UpdateOperation == "" || op.CardJSON == "" {
		return fmt.Errorf("invalid Feishu terminal checkpoint")
	}
	idempotent, ok := client.(idempotentCardKitClient)
	if !ok {
		return platform.ErrUnsupported
	}
	disableErr := idempotent.SetStreamingIdempotent(ctx, op.CardID, false, op.DisableSeq, op.DisableOperation)
	updateErr := idempotent.UpdateCardIdempotent(ctx, op.CardID, op.CardJSON, op.UpdateSeq, op.UpdateOperation)
	destroyErr := client.DestroyCard(ctx, op.CardID)
	return firstErr(ignoreCardKitUpdateError(updateErr), ignoreCardKitUpdateError(disableErr), destroyErr)
}

func (s *feishuStream) nextSequence() int {
	if s.taskCards != nil {
		s.sequence = s.taskCards.nextSequence(s.cardID, s.sequence)
		return s.sequence
	}
	s.sequence++
	return s.sequence
}

func shouldReenableStreaming(err error) bool {
	code, ok := feishuErrorCode(err)
	return ok && (code == 200850 || code == 300309)
}

func ignoreCardKitUpdateError(err error) error {
	code, ok := feishuErrorCode(err)
	if !ok {
		return err
	}
	switch code {
	case 200400, 200740, 200810, 200937, 300317:
		return nil
	default:
		return err
	}
}

func firstErr(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}
