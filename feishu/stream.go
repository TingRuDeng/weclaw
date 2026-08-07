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
	closed                  bool
	throttle                time.Duration
	now                     func() time.Time
	pendingCtx              context.Context
	pendingText             string
	hasPending              bool
	pendingTimer            *time.Timer
	pendingGeneration       uint64
	cardJSONSoftLimitBytes  int
	preserveTerminalContent bool
	inlineActiveStatus      bool
	preservedApprovals      []string
	terminal                *platform.TerminalCheckpoint
	terminalDelivered       bool
}

type feishuStreamUpdateOp struct {
	content       string
	streamSeq     int
	taskCardJSON  string
	taskUpdateSeq int
}

type feishuStreamTerminalOp struct {
	CardID           string `json:"card_id"`
	DisableSeq       int    `json:"disable_sequence"`
	DisableOperation string `json:"disable_operation"`
	UpdateSeq        int    `json:"update_sequence"`
	UpdateOperation  string `json:"update_operation"`
	CardJSON         string `json:"card_json"`
}

const feishuTerminalCheckpointKind = "feishu.cardkit.terminal.v1"
const feishuStreamReferenceKind = "feishu.cardkit.stream.v1"

type feishuStreamReferencePayload struct {
	CardID    string   `json:"card_id"`
	Title     string   `json:"title"`
	Sequence  int      `json:"sequence"`
	Content   string   `json:"content,omitempty"`
	Approvals []string `json:"approvals,omitempty"`
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
	cardJSON, err := buildCardV2(cardOptions{
		Status:             cardStatusThinking,
		Title:              opts.Title,
		Content:            opts.InitialContent,
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
		cardKit:                 r.cardKit,
		taskCards:               r.taskCards,
		cardID:                  cardID,
		title:                   opts.Title,
		throttle:                cardkitThrottle,
		now:                     time.Now,
		lastContent:             opts.InitialContent,
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
				Status:             cardStatusThinking,
				Title:              opts.Title,
				Content:            opts.InitialContent,
				InlineActiveStatus: trackTask,
			}, stream.sequence)
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
	content := trimTaskStreamThinkingIndicator(s.lastContent)
	approvals := append([]string(nil), s.preservedApprovals...)
	if s.taskCards != nil {
		if snapshot, sequence, ok := s.taskCards.snapshotWithSequence(s.cardID); ok {
			content = trimTaskStreamThinkingIndicator(snapshot.Content)
			approvals = append([]string(nil), snapshot.Approvals...)
			if sequence > s.sequence {
				s.sequence = sequence
			}
		}
	}
	if s.hasPending && strings.TrimSpace(s.pendingText) != "" {
		content = trimTaskStreamThinkingIndicator(s.pendingText)
	}
	if strings.TrimSpace(content) == "" {
		content = taskCardNoStructuredProgress
	}
	payload, err := json.Marshal(feishuStreamReferencePayload{
		CardID: s.cardID, Title: s.title, Sequence: s.sequence,
		Content: content, Approvals: approvals,
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
	if reference.Kind != feishuStreamReferenceKind {
		return platform.TerminalCheckpoint{}, fmt.Errorf("unsupported Feishu stream reference %q", reference.Kind)
	}
	var payload feishuStreamReferencePayload
	if err := json.Unmarshal(reference.Payload, &payload); err != nil {
		return platform.TerminalCheckpoint{}, fmt.Errorf("decode Feishu stream reference: %w", err)
	}
	if payload.CardID == "" || payload.Title == "" || payload.Sequence <= 0 {
		return platform.TerminalCheckpoint{}, fmt.Errorf("invalid Feishu stream reference")
	}
	referenceContent := strings.TrimSpace(payload.Content)
	if referenceContent == "" {
		referenceContent = taskCardNoStructuredProgress
	}
	stream := &feishuStream{
		cardKit: r.cardKit, taskCards: r.taskCards,
		cardID: payload.CardID, title: payload.Title, sequence: payload.Sequence,
		lastContent:             referenceContent,
		preserveTerminalContent: true, inlineActiveStatus: true,
		preservedApprovals: append([]string(nil), payload.Approvals...),
		now:                time.Now,
	}
	if r.taskCards != nil {
		r.taskCards.recordWithSequence(payload.CardID, cardOptions{
			Status: cardStatusThinking, Title: payload.Title, Content: stream.lastContent,
			Approvals: payload.Approvals, InlineActiveStatus: true,
		}, payload.Sequence)
	}
	// 恢复引用已经携带最后一次安全进度快照；finalContent 属于独立结果消息，不得写回任务卡。
	return stream.PrepareTerminalWithState("", state)
}

// Supersede 退役旧任务卡但不生成任务终态；新卡将独立承接后续进展和结果。
func (s *feishuStream) Supersede(ctx context.Context, notice string) error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()

	s.mu.Lock()
	if s.closed || s.terminal != nil || s.terminalDelivered {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancelPendingUpdate()
	s.mu.Unlock()

	notice = strings.TrimSpace(notice)
	if notice == "" {
		notice = defaultSupersededTaskCardNotice
	}
	op, err := s.prepareSupersedeUpdate(notice)
	if err != nil {
		return err
	}
	disableErr := s.cardKit.SetStreaming(ctx, s.cardID, false, op.DisableSeq)
	updateErr := s.cardKit.UpdateCard(ctx, s.cardID, op.CardJSON, op.UpdateSeq)
	return firstErr(ignoreCardKitUpdateError(updateErr), ignoreCardKitUpdateError(disableErr))
}

func (s *feishuStream) prepareSupersedeUpdate(content string) (feishuStreamTerminalOp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	opts := cardOptions{Status: cardStatusSuperseded, Title: s.title, Content: content}
	if s.taskCards != nil {
		if snapshot, ok := s.taskCards.updateAndSnapshot(s.cardID, cardStatusSuperseded, content); ok {
			opts = snapshot
		}
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		return feishuStreamTerminalOp{}, err
	}
	return feishuStreamTerminalOp{
		CardID: s.cardID, DisableSeq: s.nextSequence(), UpdateSeq: s.nextSequence(), CardJSON: cardJSON,
	}, nil
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
		Status: status, Title: s.title, Content: content,
		InlineActiveStatus: s.inlineActiveStatus,
	}
	if s.preserveTerminalContent {
		opts.Content = strings.TrimSpace(content)
		if opts.Content == "" {
			opts.Content = trimTaskStreamThinkingIndicator(s.lastContent)
		}
		if opts.Content == "" {
			opts.Content = taskCardNoStructuredProgress
		}
		opts.Approvals = append([]string(nil), s.preservedApprovals...)
	}
	if s.taskCards != nil {
		if s.preserveTerminalContent {
			s.taskCards.updateContent(s.cardID, opts.Content)
		}
		if snapshot, ok := s.taskCards.updateAndSnapshot(s.cardID, status, ""); ok {
			opts = snapshot
		}
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
