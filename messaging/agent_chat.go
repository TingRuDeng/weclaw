package messaging

import (
	"context"
	"log"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

// chatWithAgentWithProgress sends a message and optionally forwards incremental progress text.
func (h *Handler) chatWithAgentWithProgress(ctx context.Context, ag agent.Agent, userID, message string, onProgress func(delta string)) (string, error) {
	var onEvent func(agent.ProgressEvent)
	if onProgress != nil {
		onEvent = func(event agent.ProgressEvent) {
			if text := event.DisplayText(); text != "" {
				onProgress(text)
			}
		}
	}
	return h.chatWithAgentWithProgressEvents(ctx, ag, userID, message, onEvent)
}

// chatWithAgentWithProgressEvents 优先使用结构化事件，并把旧 Agent 文本包装为兼容事件。
func (h *Handler) chatWithAgentWithProgressEvents(ctx context.Context, ag agent.Agent, userID, message string, onProgress func(agent.ProgressEvent)) (string, error) {
	info := ag.Info()
	log.Printf("[handler] dispatching to agent (%s) for %s", info, userID)

	h.agentInvocations.Add(1)
	start := time.Now()
	var (
		reply string
		err   error
	)

	if streamAgent, ok := ag.(agent.StructuredProgressAgent); ok {
		reply, err = streamAgent.ChatWithProgressEvents(ctx, userID, message, onProgress)
	} else if streamAgent, ok := ag.(ProgressChatAgent); ok {
		var legacyCallback func(string)
		if onProgress != nil {
			legacyCallback = func(delta string) { onProgress(agent.TextProgressEvent(delta)) }
		}
		reply, err = streamAgent.ChatWithProgress(ctx, userID, message, legacyCallback)
	} else {
		reply, err = ag.Chat(ctx, userID, message)
	}
	elapsed := time.Since(start)

	if err != nil {
		h.agentErrors.Add(1)
		log.Printf("[handler] agent error (%s, elapsed=%s): %v", info, elapsed, err)
		return "", err
	}

	log.Printf("[handler] agent replied (%s, elapsed=%s): %q", info, elapsed, truncate(reply, 100))
	return reply, nil
}

// textProgressCallback adapts legacy text-only watchers without discarding the
// structured event contract used by the task snapshot.
func textProgressCallback(onProgress func(agent.ProgressEvent)) func(string) {
	if onProgress == nil {
		return nil
	}
	return func(text string) {
		onProgress(agent.TextProgressEvent(text))
	}
}
