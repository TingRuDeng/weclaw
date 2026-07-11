package messaging

import (
	"context"
	"log"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

// chatWithAgentWithProgress sends a message and optionally forwards incremental progress text.
func (h *Handler) chatWithAgentWithProgress(ctx context.Context, ag agent.Agent, userID, message string, onProgress func(delta string)) (string, error) {
	info := ag.Info()
	log.Printf("[handler] dispatching to agent (%s) for %s", info, userID)

	h.agentInvocations.Add(1)
	start := time.Now()
	var (
		reply string
		err   error
	)

	if streamAgent, ok := ag.(ProgressChatAgent); ok {
		reply, err = streamAgent.ChatWithProgress(ctx, userID, message, onProgress)
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
