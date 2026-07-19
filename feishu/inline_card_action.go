package feishu

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type inlineCardReply struct {
	card   *callback.Card
	replay func(context.Context) error
}

type inlineCardReplier struct {
	platform.Replier
	conversationKey string
	mu              sync.Mutex
	capturing       bool
	pending         []*inlineCardReply
	fallbackDone    chan struct{}
}

func newInlineCardReplier(reply platform.Replier, conversationKey string) *inlineCardReplier {
	return &inlineCardReplier{Replier: reply, conversationKey: conversationKey, capturing: true}
}

func (r *inlineCardReplier) SendText(ctx context.Context, content string) error {
	result := &inlineCardReply{
		card: buildChoiceHandledStatusCard("blue", strings.TrimSpace(content)),
		replay: func(replayCtx context.Context) error {
			return r.Replier.SendText(replayCtx, content)
		},
	}
	if r.capture(result) {
		return nil
	}
	if err := r.waitForFallback(ctx); err != nil {
		return err
	}
	return r.Replier.SendText(ctx, content)
}

func (r *inlineCardReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	cardJSON, err := buildChoiceCard(prompt, choices, r.conversationKey)
	if err != nil {
		return err
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		return err
	}
	copied := append([]platform.Choice(nil), choices...)
	result := &inlineCardReply{
		card: &callback.Card{Type: "raw", Data: card},
		replay: func(replayCtx context.Context) error {
			return r.Replier.AskChoices(replayCtx, prompt, copied)
		},
	}
	if r.capture(result) {
		return nil
	}
	if err := r.waitForFallback(ctx); err != nil {
		return err
	}
	return r.Replier.AskChoices(ctx, prompt, choices)
}

func (r *inlineCardReplier) SendImage(ctx context.Context, path string) error {
	r.replayPendingAsync(r.activateFallback())
	if err := r.waitForFallback(ctx); err != nil {
		return err
	}
	return r.Replier.SendImage(ctx, path)
}

func (r *inlineCardReplier) SendFile(ctx context.Context, path string) error {
	r.replayPendingAsync(r.activateFallback())
	if err := r.waitForFallback(ctx); err != nil {
		return err
	}
	return r.Replier.SendFile(ctx, path)
}

func (r *inlineCardReplier) Typing(ctx context.Context, on bool) error {
	r.replayPendingAsync(r.activateFallback())
	if err := r.waitForFallback(ctx); err != nil {
		return err
	}
	return r.Replier.Typing(ctx, on)
}

func (r *inlineCardReplier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.replayPendingAsync(r.activateFallback())
	if err := r.waitForFallback(ctx); err != nil {
		return nil, err
	}
	return r.Replier.OpenStream(ctx, opts)
}

func (r *inlineCardReplier) CurrentTaskCardID() string {
	reporter, ok := r.Replier.(platform.TaskCardReporter)
	if !ok {
		return ""
	}
	return reporter.CurrentTaskCardID()
}

func (r *inlineCardReplier) capture(result *inlineCardReply) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.capturing {
		return false
	}
	r.pending = append(r.pending, result)
	return true
}

func (r *inlineCardReplier) finish() *callback.Card {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capturing = false
	if len(r.pending) == 0 {
		return nil
	}
	card := r.pending[len(r.pending)-1].card
	r.pending = nil
	return card
}

func (r *inlineCardReplier) activateFallback() []*inlineCardReply {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.capturing {
		return nil
	}
	r.capturing = false
	pending := r.pending
	r.pending = nil
	if len(pending) > 0 {
		r.fallbackDone = make(chan struct{})
	}
	return pending
}

func (r *inlineCardReplier) replayPendingAsync(pending []*inlineCardReply) {
	if len(pending) == 0 {
		return
	}
	go func() {
		defer r.finishFallbackReplay()
		ctx, cancel := context.WithTimeout(context.Background(), feishuCardActionNoticeTimeout)
		defer cancel()
		for _, result := range pending {
			if result == nil || result.replay == nil {
				continue
			}
			if err := result.replay(ctx); err != nil {
				log.Printf("[feishu] failed to replay inline card result: %v", err)
				return
			}
		}
	}()
}

func (r *inlineCardReplier) waitForFallback(ctx context.Context) error {
	r.mu.Lock()
	done := r.fallbackDone
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *inlineCardReplier) finishFallbackReplay() {
	r.mu.Lock()
	done := r.fallbackDone
	r.fallbackDone = nil
	r.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (a *Adapter) handleInlineCardAction(ctx context.Context, msg platform.IncomingMessage, action parsedCardAction, dispatch platform.DispatchFunc, ticket feishuDispatchTicket) *callback.CardActionTriggerResponse {
	conversationKey := firstNonEmpty(action.SessionKey, action.Conv, msg.ConversationKey())
	resultReply := platform.Replier(a.newScopedReplier(msg))
	if isDeferredCardResultCommand(action.Choice) && strings.TrimSpace(action.MessageID) != "" {
		resultReply = newDeferredCardResultReplierWithTitle(resultReply, a.sender, action.MessageID, deferredCardResultTitle(action.Choice))
	}
	reply := newInlineCardReplier(resultReply, conversationKey)
	dispatchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cardActionTimeout)
	done := make(chan bool, 1)
	go func() {
		completed := ticket.run(dispatchCtx, func() { dispatch(dispatchCtx, msg, reply) })
		if !completed {
			log.Printf("[feishu] inline card action dispatch timed out: action=%s", action.Action)
			a.sendInlineCardActionTimeout(action, msg)
		}
		done <- completed
		cancel()
	}()

	timeout := a.cardInlineTimeout
	if timeout <= 0 {
		timeout = feishuInlineCardActionTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case completed := <-done:
		if completed {
			if card := reply.finish(); card != nil {
				return &callback.CardActionTriggerResponse{
					Toast: &callback.Toast{Type: "success", Content: "已完成"}, Card: card,
				}
			}
			return &callback.CardActionTriggerResponse{
				Toast: &callback.Toast{Type: "success", Content: "已完成"},
				Card:  buildInlineChoiceCompletedCard(action),
			}
		}
		reply.replayPendingAsync(reply.activateFallback())
		return submittedCardActionResponse(action)
	case <-timer.C:
		reply.replayPendingAsync(reply.activateFallback())
		return submittedCardActionResponse(action)
	}
}

func (a *Adapter) sendInlineCardActionTimeout(action parsedCardAction, msg platform.IncomingMessage) {
	if !isDeferredCardResultCommand(action.Choice) || strings.TrimSpace(action.MessageID) == "" {
		a.sendCardActionTimeoutNotice(msg)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), feishuCardActionNoticeTimeout)
	defer cancel()
	reply := newDeferredCardResultReplierWithTitle(a.newScopedReplier(msg), a.sender, action.MessageID, deferredCardResultTitle(action.Choice))
	if err := reply.SendText(ctx, deferredCardTimeoutText(action.Choice)); err != nil {
		log.Printf("[feishu] failed to update timed out card action: message=%s err=%v", action.MessageID, err)
	}
}

func isInlineCardCommand(choice string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(choice)))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/help", "/status", "/ps", "/cwd", "/mode", "/progress", "/model", "/reasoning":
		return true
	case "/cx", "/cc":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "help", "ls", "cd", "page", "status", "whoami", "pwd", "model", "quota", "switch", "account":
			return true
		}
	}
	return false
}

func isDeferredCardResultCommand(choice string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(choice)))
	if len(fields) < 2 || (fields[0] != "/cx" && fields[0] != "/cc") {
		return false
	}
	return fields[1] == "switch" || fields[0] == "/cx" && fields[1] == "account" && len(fields) >= 3 && fields[2] == "confirm"
}

func deferredCardResultTitle(choice string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(choice)))
	if len(fields) >= 3 && fields[0] == "/cx" && fields[1] == "account" && fields[2] == "confirm" {
		return "Codex 账号切换结果"
	}
	return "会话切换结果"
}

func deferredCardTimeoutText(choice string) string {
	if deferredCardResultTitle(choice) == "Codex 账号切换结果" {
		return "Codex 账号切换等待超时，请发送 /cx account status 检查当前状态，确认后再重试。"
	}
	return "会话切换等待超时，请检查当前状态后重试。"
}
