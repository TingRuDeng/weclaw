package feishu

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const cardkitThrottle = 500 * time.Millisecond

type feishuStream struct {
	cardKit     cardKitClient
	taskCards   *taskCardRegistry
	cardID      string
	title       string
	sequence    int
	lastUpdate  time.Time
	lastContent string
	closed      bool
	throttle    time.Duration
	now         func() time.Time
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
	if err := r.sender.SendCard(ctx, r.openID, cardID); err != nil {
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
	if s.closed || content == s.lastContent {
		return nil
	}
	now := s.now()
	if !s.lastUpdate.IsZero() && now.Sub(s.lastUpdate) < s.throttle {
		return nil
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

// Complete 关闭流式并全量更新为完成卡片。
func (s *feishuStream) Complete(ctx context.Context, finalContent string) error {
	if s.closed {
		return nil
	}
	s.closed = true
	if strings.TrimSpace(finalContent) == "" {
		finalContent = s.lastContent
	}
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
	if s.closed {
		return nil
	}
	s.closed = true
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
