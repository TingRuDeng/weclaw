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
	return r.Replier.AskChoices(ctx, prompt, choices)
}

func (r *inlineCardReplier) SendImage(ctx context.Context, path string) error {
	r.replayPendingAsync(r.activateFallback())
	return r.Replier.SendImage(ctx, path)
}

func (r *inlineCardReplier) SendFile(ctx context.Context, path string) error {
	r.replayPendingAsync(r.activateFallback())
	return r.Replier.SendFile(ctx, path)
}

func (r *inlineCardReplier) Typing(ctx context.Context, on bool) error {
	r.replayPendingAsync(r.activateFallback())
	return r.Replier.Typing(ctx, on)
}

func (r *inlineCardReplier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.replayPendingAsync(r.activateFallback())
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
	return pending
}

func (r *inlineCardReplier) replayPendingAsync(pending []*inlineCardReply) {
	if len(pending) == 0 {
		return
	}
	go func() {
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

func (a *Adapter) handleInlineCardAction(ctx context.Context, msg platform.IncomingMessage, action parsedCardAction, dispatch platform.DispatchFunc, ticket feishuDispatchTicket) *callback.CardActionTriggerResponse {
	conversationKey := firstNonEmpty(action.SessionKey, action.Conv, msg.ConversationKey())
	var resultReply platform.Replier = a.newScopedReplier(msg)
	if isDeferredCardResultCommand(action.Choice) && strings.TrimSpace(action.MessageID) != "" {
		resultReply = newDeferredCardResultReplier(resultReply, a.sender, action.MessageID)
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
	reply := newDeferredCardResultReplier(a.newScopedReplier(msg), a.sender, action.MessageID)
	if err := reply.SendText(ctx, "会话切换等待超时，请检查当前状态后重试。"); err != nil {
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
		case "help", "ls", "cd", "page", "status", "whoami", "pwd", "model", "quota", "switch":
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
	return fields[1] == "switch"
}
