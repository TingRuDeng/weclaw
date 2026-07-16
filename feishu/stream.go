package feishu

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const cardkitThrottle = 500 * time.Millisecond

type feishuStream struct {
	mu                sync.Mutex
	cardKit           cardKitClient
	taskCards         *taskCardRegistry
	cardID            string
	title             string
	sequence          int
	lastUpdate        time.Time
	lastContent       string
	closed            bool
	throttle          time.Duration
	now               func() time.Time
	pendingCtx        context.Context
	pendingText       string
	hasPending        bool
	pendingTimer      *time.Timer
	pendingGeneration uint64
}

// openCardKitStream 创建并发送 CardKit 卡片，然后开启流式模式。
func (r *Replier) openCardKitStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	return r.openCardKitStreamWithMode(ctx, opts, false)
}

func (r *Replier) openTaskCardKitStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	return r.openCardKitStreamWithMode(ctx, opts, true)
}

func (r *Replier) openCardKitStreamWithMode(ctx context.Context, opts platform.StreamOptions, trackTask bool) (platform.Stream, error) {
	cardJSON, err := buildCardV2(cardOptions{
		Status:  cardStatusThinking,
		Title:   opts.Title,
		Content: opts.InitialContent,
	})
	if err != nil {
		return nil, err
	}
	cardID, err := r.cardKit.CreateCard(ctx, cardJSON)
	if err != nil {
		return nil, err
	}
	if err := r.sendCard(ctx, r.openID, cardID); err != nil {
		return nil, err
	}
	if trackTask {
		r.setCurrentTaskCardID(cardID)
		if r.taskCards != nil {
			r.taskCards.record(cardID, cardOptions{
				Status:  cardStatusThinking,
				Title:   opts.Title,
				Content: opts.InitialContent,
			})
		}
	}
	stream := &feishuStream{
		cardKit:   r.cardKit,
		taskCards: r.taskCards,
		cardID:    cardID,
		title:     opts.Title,
		throttle:  cardkitThrottle,
		now:       time.Now,
	}
	if err := stream.cardKit.SetStreaming(ctx, stream.cardID, true, stream.nextSequence()); err != nil {
		return nil, err
	}
	return stream, nil
}

// Update 节流更新主内容组件，触发飞书打字机效果。
func (s *feishuStream) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if content == s.lastContent {
		if s.hasPending {
			s.cancelPendingUpdate()
		}
		return nil
	}
	now := s.now()
	if delay := s.throttleDelay(now); delay > 0 {
		s.queuePendingUpdate(ctx, content, delay)
		return nil
	}
	s.cancelPendingUpdate()
	return s.updateNow(ctx, content, now)
}

func (s *feishuStream) updateNow(ctx context.Context, content string, now time.Time) error {
	if s.taskCards != nil {
		return s.updateTaskCard(ctx, content, now)
	}
	err := s.cardKit.StreamContent(ctx, s.cardID, cardMainContentID, content, s.nextSequence())
	if shouldReenableStreaming(err) {
		if enableErr := s.cardKit.SetStreaming(ctx, s.cardID, true, s.nextSequence()); enableErr != nil {
			return ignoreCardKitUpdateError(enableErr)
		}
		err = s.cardKit.StreamContent(ctx, s.cardID, cardMainContentID, content, s.nextSequence())
	}
	if ignored := ignoreCardKitUpdateError(err); ignored != nil {
		return ignored
	}
	if err != nil {
		log.Printf("[feishu] ignored non-fatal card stream update error: %v", err)
	}
	s.lastUpdate = now
	s.lastContent = content
	if s.taskCards != nil {
		s.taskCards.updateContent(s.cardID, content)
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
	defer s.mu.Unlock()
	if s.closed || generation != s.pendingGeneration || s.pendingTimer == nil {
		return
	}
	s.pendingTimer = nil
	if !s.hasPending {
		return
	}
	now := s.now()
	if delay := s.throttleDelay(now); delay > 0 {
		s.schedulePendingUpdate(delay)
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
	if err := s.updateNow(ctx, content, now); err != nil {
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

func (s *feishuStream) updateTaskCard(ctx context.Context, content string, now time.Time) error {
	opts, sequence, ok := s.taskCards.updateContentWithSequence(s.cardID, content)
	if !ok {
		err := s.cardKit.StreamContent(ctx, s.cardID, cardMainContentID, content, s.nextSequence())
		return ignoreCardKitUpdateError(err)
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		return err
	}
	err = s.cardKit.UpdateCard(ctx, s.cardID, cardJSON, sequence)
	if ignored := ignoreCardKitUpdateError(err); ignored != nil {
		return ignored
	}
	if err != nil {
		log.Printf("[feishu] ignored non-fatal task card update error: %v", err)
	}
	s.lastUpdate = now
	s.lastContent = content
	s.sequence = sequence
	return nil
}

// Complete 关闭流式并全量更新为完成卡片。
func (s *feishuStream) Complete(ctx context.Context, finalContent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancelPendingUpdate()
	disableErr := s.disableStreaming(ctx)
	opts := cardOptions{Status: cardStatusDone, Title: s.title, Content: finalContent}
	if s.taskCards != nil {
		if snapshot, ok := s.taskCards.updateAndSnapshot(s.cardID, cardStatusDone, finalContent); ok {
			opts = snapshot
		}
	}
	cardJSON, buildErr := buildCardV2(opts)
	if buildErr != nil {
		return buildErr
	}
	updateErr := s.cardKit.UpdateCard(ctx, s.cardID, cardJSON, s.nextSequence())
	destroyErr := s.cardKit.DestroyCard(ctx, s.cardID)
	return firstErr(updateErr, disableErr, destroyErr)
}

// Fail 关闭流式并全量更新为失败卡片。
func (s *feishuStream) Fail(ctx context.Context, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancelPendingUpdate()
	disableErr := s.disableStreaming(ctx)
	opts := cardOptions{Status: cardStatusError, Title: s.title, Content: errText}
	if s.taskCards != nil {
		if snapshot, ok := s.taskCards.updateAndSnapshot(s.cardID, cardStatusError, errText); ok {
			opts = snapshot
		}
	}
	cardJSON, buildErr := buildCardV2(opts)
	if buildErr != nil {
		return buildErr
	}
	updateErr := s.cardKit.UpdateCard(ctx, s.cardID, cardJSON, s.nextSequence())
	destroyErr := s.cardKit.DestroyCard(ctx, s.cardID)
	return firstErr(updateErr, disableErr, destroyErr)
}

func (s *feishuStream) disableStreaming(ctx context.Context) error {
	return ignoreCardKitUpdateError(s.cardKit.SetStreaming(ctx, s.cardID, false, s.nextSequence()))
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
