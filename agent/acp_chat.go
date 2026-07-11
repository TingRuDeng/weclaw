package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// Chat sends a message and returns the full response.
func (a *ACPAgent) Chat(ctx context.Context, conversationID string, message string) (string, error) {
	return a.chat(ctx, conversationID, message, nil)
}

// ChatWithProgress sends a message and emits incremental deltas during generation.
func (a *ACPAgent) ChatWithProgress(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	return a.chat(ctx, conversationID, message, onProgress)
}

func (a *ACPAgent) chat(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	if a.protocol == protocolCodexAppServer {
		if binding, ok := a.CurrentCodexThreadBinding(conversationID); ok {
			switch binding.Owner {
			case CodexOwnerDesktopLive:
				return a.chatCodexDesktop(ctx, binding, message, onProgress)
			case CodexOwnerDesktopDisconnected:
				return "", ErrCodexDesktopDisconnected
			case CodexOwnerUnknown:
				return "", ErrCodexDesktopOwnershipUnknown
			case CodexOwnerPersistedOnly:
				return "", fmt.Errorf("Codex thread 必须先恢复再继续对话")
			}
		}
	}
	if !a.isRuntimeStarted() {
		if err := a.Start(ctx); err != nil {
			return "", err
		}
	}

	// Route to codex app-server protocol if applicable
	if a.protocol == protocolCodexAppServer {
		return a.chatCodexAppServer(ctx, conversationID, message, onProgress)
	}

	return a.chatLegacyACP(ctx, conversationID, message, onProgress, true)
}

// chatLegacyACP 处理标准 ACP session/prompt 流程，并在会话失效时允许一次重建重试。
func (a *ACPAgent) chatLegacyACP(ctx context.Context, conversationID string, message string, onProgress func(delta string), allowSessionRetry bool) (string, error) {
	// Get or create session
	sessionID, isNew, err := a.getOrCreateSession(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("session error: %w", err)
	}

	pid := a.runtimePID()
	if isNew {
		log.Printf("[acp] new session created (pid=%d, session=%s, conversation=%s)", pid, sessionID, conversationID)
	} else {
		log.Printf("[acp] reusing session (pid=%d, session=%s, conversation=%s)", pid, sessionID, conversationID)
	}

	// Register notification channel for this session
	notifyCh := make(chan *sessionUpdate, 256)
	approvalCh := make(chan *codexTurnEvent, 16)
	if !a.registerLegacySessionChannels(sessionID, notifyCh, approvalCh) {
		return "", fmt.Errorf("session %s already has an active prompt", sessionID)
	}
	defer a.unregisterLegacySessionChannels(sessionID, notifyCh, approvalCh)

	// Send prompt (this blocks until the prompt completes)
	type promptDoneMsg struct {
		result json.RawMessage
		err    error
	}
	promptDone := make(chan promptDoneMsg, 1)
	go func() {
		result, err := a.rpc(ctx, "session/prompt", promptParams{
			SessionID: sessionID,
			Prompt:    []promptEntry{{Type: "text", Text: message}},
		})
		if result != nil {
			log.Printf("[acp] prompt result (session=%s): %s", sessionID, string(result))
		}
		promptDone <- promptDoneMsg{result: result, err: err}
	}()

	// 普通 agent_message_chunk 是最终回复正文，不能作为进度推给飞书任务卡片。
	var textParts []string

	for {
		select {
		case <-ctx.Done():
			if err := a.notify("session/cancel", map[string]interface{}{"sessionId": sessionID}); err != nil {
				return "", fmt.Errorf("%w: session/cancel failed: %v", ctx.Err(), err)
			}
			return "", ctx.Err()
		case update := <-notifyCh:
			if update.SessionUpdate == "agent_message_chunk" {
				text := extractChunkText(update)
				if text != "" {
					textParts = append(textParts, text)
				}
			}
		case evt := <-approvalCh:
			if evt.Approval != nil {
				if err := a.handleCodexApprovalEvent(ctx, evt); err != nil {
					return "", fmt.Errorf("approval response error: %w", err)
				}
				continue
			}
			if evt.UserInput != nil {
				if err := a.handleCodexUserInputEvent(ctx, evt); err != nil {
					return "", fmt.Errorf("user input response error: %w", err)
				}
				continue
			}
		case done := <-promptDone:
			// Drain remaining notifications
			for {
				select {
				case update := <-notifyCh:
					if update.SessionUpdate == "agent_message_chunk" {
						text := extractChunkText(update)
						if text != "" {
							textParts = append(textParts, text)
						}
					}
				default:
					goto drained
				}
			}
		drained:
			if done.err != nil {
				if allowSessionRetry && isMissingThreadError(done.err) {
					log.Printf("[acp] stale ACP session detected, retrying with a fresh session (conversation=%s, oldSession=%s): %v", conversationID, sessionID, done.err)
					a.clearACPSession(conversationID)
					return a.chatLegacyACP(ctx, conversationID, message, onProgress, false)
				}
				return "", fmt.Errorf("prompt error: %w", done.err)
			}
			result := strings.TrimSpace(strings.Join(textParts, ""))
			if result == "" {
				// Try extracting from prompt result (some agents return content here)
				result = extractPromptResultText(done.result)
			}
			if result == "" {
				return "", fmt.Errorf("agent returned empty response")
			}
			return result, nil
		}
	}
}
