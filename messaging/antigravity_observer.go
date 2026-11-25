package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/antigravity"
	"github.com/fastclaw-ai/weclaw/platform"
)

type antigravityObserver struct {
	h                  *Handler
	key                string
	generation         uint64
	accountID          string
	routeUserID        string
	authorizedIdentity string
	deliveryRoute      platform.DeliveryRoute
	fallbackReply      platform.Replier
	conversationID     string
	projectName        string
	title              string
	ctx                context.Context
	cancelFn           context.CancelFunc
	done               chan struct{}

	mu          sync.Mutex
	stream      platform.Stream
	delivered   map[int64]bool
	deliveredMu sync.Mutex
}

func newAntigravityObserver(
	h *Handler,
	key string,
	generation uint64,
	accountID, routeUserID, authorizedIdentity string,
	deliveryRoute platform.DeliveryRoute,
	fallbackReply platform.Replier,
	conversationID, projectName, title string,
) *antigravityObserver {
	ctx, cancel := context.WithCancel(context.Background())
	return &antigravityObserver{
		h: h, key: key, generation: generation, accountID: accountID, routeUserID: routeUserID,
		authorizedIdentity: authorizedIdentity, deliveryRoute: deliveryRoute, fallbackReply: fallbackReply,
		conversationID: conversationID, projectName: projectName, title: title,
		ctx: ctx, cancelFn: cancel, done: make(chan struct{}), delivered: make(map[int64]bool),
	}
}

func (o *antigravityObserver) cancel() {
	if o == nil {
		return
	}
	o.cancelFn()
}

func (o *antigravityObserver) closed() bool {
	if o == nil {
		return true
	}
	select {
	case <-o.done:
		return true
	default:
		return false
	}
}

func (o *antigravityObserver) active() bool {
	o.h.antigravityMu.Lock()
	defer o.h.antigravityMu.Unlock()
	binding := o.h.antigravityBindings[o.key]
	return binding != nil && binding.generation == o.generation && binding.observer == o
}

func (o *antigravityObserver) run(fileID string, offset int64) {
	defer close(o.done)
	defer o.detachStream()
	client, ok := o.h.antigravityClientSnapshot()
	if !ok || client == nil {
		return
	}
	cursorFileID, cursorOffset := fileID, offset
	turnStart := offset
	var progress []string
	replyText := ""
	activity := false
	cancelling := false
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-timer.C:
		}
		if !o.active() {
			return
		}
		ctx, cancel := context.WithTimeout(o.ctx, 4*time.Second)
		result, err := client.Observe(ctx, o.conversationID, cursorFileID, cursorOffset)
		cancel()
		if err != nil {
			if !o.active() {
				return
			}
			timer.Reset(antigravityObserveIdle)
			continue
		}
		if result.Discontinuous {
			o.finishUnconfirmed("会话日志已重置，本轮结果未确认。")
			cursorFileID, cursorOffset = result.NextFileID, result.NextOffset
			turnStart = cursorOffset
			progress = nil
			replyText = ""
			activity = false
			cancelling = false
			timer.Reset(antigravityObserveIdle)
			continue
		}
		for _, event := range result.Events {
			switch event.Kind {
			case antigravity.EventTurnInput:
				if activity && replyText != "" && turnStart != event.Offset {
					o.deliverTurn(replyText, turnStart)
					progress = nil
					replyText = ""
					activity = false
					cancelling = false
				}
				turnStart = event.Offset
				progress = nil
				replyText = ""
				activity = false
				cancelling = false
			case antigravity.EventProgress:
				activity = true
				progress = appendAntigravityProgress(progress, event.Text)
			case antigravity.EventFocus:
				activity = true
				if strings.TrimSpace(event.Focus) != "" {
					progress = appendAntigravityProgress(progress, "聚焦："+event.Focus)
				}
			case antigravity.EventTool:
				activity = true
				if strings.TrimSpace(event.Tool) != "" {
					progress = appendAntigravityProgress(progress, "工具："+event.Tool)
				}
			case antigravity.EventAwaitDesktop:
				activity = true
				progress = appendAntigravityProgress(progress, "等待桌面端回答，请回到 Antigravity 或官方 Remote Control 处理。")
			case antigravity.EventError:
				activity = true
				progress = appendAntigravityProgress(progress, "错误："+event.Text)
			case antigravity.EventReply:
				activity = true
				replyText = event.Text
			}
		}
		if len(result.Events) > 0 && activity {
			o.updateStream(antigravityProgressText(progress))
		}
		cursorFileID, cursorOffset = result.NextFileID, result.NextOffset
		switch strings.TrimSpace(result.Conversation.RunState) {
		case "RUN_STATE_CANCELLING":
			cancelling = true
			o.updateStream("桌面端正在取消本轮…")
			timer.Reset(antigravityObserveRunning)
		case "RUN_STATE_RUNNING":
			timer.Reset(antigravityObserveRunning)
		default:
			if activity {
				// One tail read confirms the final reply belongs to this turn.
				callCtx, tailCancel := context.WithTimeout(o.ctx, 3*time.Second)
				extra, extraErr := client.Observe(callCtx, o.conversationID, cursorFileID, cursorOffset)
				tailCancel()
				if extraErr == nil {
					for _, event := range extra.Events {
						if event.Kind == antigravity.EventReply {
							replyText = event.Text
						}
					}
					cursorFileID, cursorOffset = extra.NextFileID, extra.NextOffset
				}
				if strings.TrimSpace(replyText) != "" && !cancelling {
					o.deliverTurn(replyText, turnStart)
				} else {
					o.finishUnconfirmed("会话已空闲，本轮结果未确认。")
				}
				progress = nil
				replyText = ""
				activity = false
				cancelling = false
				turnStart = cursorOffset
			}
			timer.Reset(antigravityObserveIdle)
		}
	}
}

func (o *antigravityObserver) writeContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(o.ctx, timeout)
}

func (o *antigravityObserver) updateStream(content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	o.h.antigravityDeliveryMu.RLock()
	defer o.h.antigravityDeliveryMu.RUnlock()
	if o.ctx.Err() != nil {
		return
	}
	o.mu.Lock()
	stream := o.stream
	o.mu.Unlock()
	if stream != nil {
		if _, ok := o.h.antigravityDelivery(o); !ok {
			return
		}
		ctx, cancel := o.writeContext(5 * time.Second)
		defer cancel()
		if err := stream.Update(ctx, content); err != nil {
			log.Printf("[handler] failed to update antigravity progress card: %v", err)
		}
		return
	}
	reply, ok := o.h.antigravityDelivery(o)
	if !ok {
		return
	}
	ctx, cancel := o.writeContext(5 * time.Second)
	opened, err := reply.OpenStream(ctx, platform.StreamOptions{
		Title: antigravityProgressTitle(o.projectName, o.title), InitialContent: content,
	})
	cancel()
	if err != nil {
		log.Printf("[handler] failed to open antigravity progress card: %v", err)
		return
	}
	if o.ctx.Err() != nil {
		detachAntigravityStream(opened)
		return
	}
	if _, ok := o.h.antigravityDelivery(o); !ok {
		detachAntigravityStream(opened)
		return
	}
	o.mu.Lock()
	if o.stream != nil {
		o.mu.Unlock()
		detachAntigravityStream(opened)
		return
	}
	o.stream = opened
	o.mu.Unlock()
}

func (o *antigravityObserver) deliverTurn(text string, turnStart int64) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	o.deliveredMu.Lock()
	if o.delivered[turnStart] {
		o.deliveredMu.Unlock()
		return
	}
	o.delivered[turnStart] = true
	o.deliveredMu.Unlock()
	o.h.antigravityDeliveryMu.RLock()
	defer o.h.antigravityDeliveryMu.RUnlock()
	if o.ctx.Err() != nil {
		return
	}
	reply, ok := o.h.antigravityDelivery(o)
	if !ok {
		return
	}
	o.mu.Lock()
	stream := o.stream
	o.stream = nil
	o.mu.Unlock()
	if stream != nil {
		ctx, cancel := o.writeContext(5 * time.Second)
		if err := stream.Complete(ctx, "本轮已完成"); err != nil {
			log.Printf("[handler] failed to complete antigravity progress card: %v", err)
		}
		cancel()
		if o.ctx.Err() != nil {
			return
		}
		// 收敛旧卡后重新取最新回复器，避免使用较早取得的 reply。
		reply, ok = o.h.antigravityDelivery(o)
		if !ok {
			return
		}
	}
	title := antigravityProgressTitle(o.projectName, o.title)
	deliveryKey := fmt.Sprintf("antigravity:%s:%s:%s:%d", o.accountID, o.routeUserID, o.conversationID, turnStart)
	if idempotent, supported := optionalIdempotentResultReplier(reply); supported {
		ctx, cancel := o.writeContext(10 * time.Second)
		defer cancel()
		err := idempotent.SendResultIdempotent(ctx, platform.TerminalResult{
			Title: title, Text: text, State: platform.StreamTerminalCompleted,
		}, deliveryKey)
		if err != nil {
			log.Printf("[handler] failed to deliver antigravity result: %v", err)
		}
		return
	}
	ctx, cancel := o.writeContext(10 * time.Second)
	defer cancel()
	if err := reply.SendText(ctx, text); err != nil {
		log.Printf("[handler] failed to deliver antigravity result: %v", err)
	}
}

func (o *antigravityObserver) finishUnconfirmed(notice string) {
	o.h.antigravityDeliveryMu.RLock()
	defer o.h.antigravityDeliveryMu.RUnlock()
	if o.ctx.Err() != nil {
		return
	}
	reply, ok := o.h.antigravityDelivery(o)
	if !ok {
		return
	}
	o.mu.Lock()
	stream := o.stream
	o.stream = nil
	o.mu.Unlock()
	ctx, cancel := o.writeContext(5 * time.Second)
	defer cancel()
	if stream != nil {
		if err := stream.Fail(ctx, notice); err != nil {
			log.Printf("[handler] failed to fail antigravity progress card: %v", err)
		}
		return
	}
	_ = reply.SendText(ctx, notice)
}

func detachAntigravityStream(stream platform.Stream) {
	if stream == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), antigravityDetachTimeout)
	defer cancel()
	if detachable, ok := stream.(platform.DetachableStream); ok {
		_ = detachable.Detach(ctx, "此窗口已停止跟踪 Antigravity 会话")
		return
	}
	if supersedable, ok := stream.(platform.SupersedableStream); ok {
		_ = supersedable.Supersede(ctx, "此窗口已停止跟踪 Antigravity 会话")
	}
}

func (o *antigravityObserver) detachStream() {
	o.mu.Lock()
	stream := o.stream
	o.stream = nil
	o.mu.Unlock()
	detachAntigravityStream(stream)
}

func appendAntigravityProgress(lines []string, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return lines
	}
	if len(lines) > 0 && lines[len(lines)-1] == text {
		return lines
	}
	lines = append(lines, text)
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return lines
}

func antigravityProgressText(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n\n")
}

// handleAntigravityMessage forwards one plain text message to the bound
// conversation. Any failure consumes the message; it never falls back to the
// default Agent.
func (h *Handler) handleAntigravityMessage(runtime platformMessageRuntime) {
	binding, ok := h.antigravityBindingSnapshot(runtime.msg.AccountID, runtime.routeUserID)
	if !ok {
		return
	}
	client, ok := h.antigravityClientSnapshot()
	if !ok || client == nil {
		runtime.sendText("Antigravity Sidecar 未连接，消息未发送。")
		return
	}
	conversation, baseline, err := client.GetConversation(runtime.ctx, binding.conversationID)
	if err != nil {
		runtime.sendText("Antigravity 会话不可用，消息未发送：" + err.Error())
		return
	}
	switch strings.TrimSpace(conversation.RunState) {
	case "RUN_STATE_RUNNING":
		runtime.sendText("Antigravity 桌面端正在运行，请等本轮结束后重新发送。")
		return
	case "RUN_STATE_CANCELLING":
		runtime.sendText("Antigravity 桌面端正在取消本轮，请稍后重新发送。")
		return
	}
	if !baseline.HasHistory {
		runtime.sendText("Antigravity 会话还没有历史轮次，请先在桌面端发送首句，或使用 /ag new。")
		return
	}
	current, ok := h.antigravityBindingSnapshot(runtime.msg.AccountID, runtime.routeUserID)
	if !ok || current.generation != binding.generation {
		runtime.sendText("Antigravity 窗口绑定已变化，消息未发送。")
		return
	}
	if err := client.SendMessage(runtime.ctx, binding.conversationID, antigravityFeishuPrefix+runtime.text); err != nil {
		runtime.sendText("提交结果待核对：消息可能未送达 Antigravity。请先在桌面端确认本轮是否收到消息，WeClaw 不会自动重发。")
		return
	}
	runtime.sendText("已提交到 Antigravity，正在同步本轮进展。")
}
