package agent

import (
	"context"
	"encoding/json"
	"errors"
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
		return "", fmt.Errorf("Codex app-server 必须通过受控 turn 执行: %w", ErrCodexControlRequired)
	}
	if !a.isRuntimeStarted() && !a.hasLegacySessionCandidate(conversationID) {
		return "", fmt.Errorf("session error: %w", ErrAgentSessionNotBound)
	}
	if !a.isRuntimeStarted() {
		if err := a.Start(ctx); err != nil {
			return "", err
		}
	}

	sessionID, err := a.requireSession(conversationID)
	if err != nil {
		return "", fmt.Errorf("session error: %w", err)
	}
	if err := a.resumeClaudeSessionIfStale(ctx, conversationID, sessionID); err != nil {
		return "", err
	}

	return a.chatLegacyACP(ctx, conversationID, message, onProgress)
}

// chatLegacyACP 处理标准 ACP session/prompt 流程，任何失败都保留原 session 绑定。
func (a *ACPAgent) chatLegacyACP(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	// 普通消息只能使用已经由用户显式选择或创建的 session。
	sessionID, err := a.requireSession(conversationID)
	if err != nil {
		return "", fmt.Errorf("session error: %w", err)
	}

	pid := a.runtimePID()
	log.Printf("[acp] reusing session (pid=%d, session=%s, conversation=%s)", pid, sessionID, conversationID)

	// Register notification channel for this session
	notifyCh := make(chan *sessionUpdate, 256)
	approvalCh := make(chan *codexTurnEvent, 16)
	if !a.registerLegacySessionChannels(sessionID, notifyCh, approvalCh) {
		return "", fmt.Errorf("session %s already has an active prompt", sessionID)
	}
	defer a.unregisterLegacySessionChannels(sessionID, notifyCh, approvalCh)
	state := legacyPromptState{
		ctx: ctx, sessionID: sessionID, notifyCh: notifyCh, approvalCh: approvalCh,
		promptDone: a.startLegacyPrompt(ctx, sessionID, message), onProgress: onProgress,
		progress: newClaudeACPProgressState(),
	}
	return a.waitLegacyPrompt(state)
}

type legacyPromptDone struct {
	result json.RawMessage
	err    error
}

type legacyPromptState struct {
	ctx        context.Context
	sessionID  string
	notifyCh   <-chan *sessionUpdate
	approvalCh <-chan *codexTurnEvent
	promptDone <-chan legacyPromptDone
	onProgress func(string)
	progress   *claudeACPProgressState
}

// startLegacyPrompt 异步执行阻塞的 session/prompt RPC，让当前协程继续消费事件。
func (a *ACPAgent) startLegacyPrompt(ctx context.Context, sessionID string, message string) <-chan legacyPromptDone {
	done := make(chan legacyPromptDone, 1)
	go func() {
		result, err := a.rpc(ctx, "session/prompt", promptParams{
			SessionID: sessionID,
			Prompt:    []promptEntry{{Type: "text", Text: message}},
		})
		done <- legacyPromptDone{result: result, err: err}
	}()
	return done
}

// waitLegacyPrompt 消费标准 ACP 更新，直到 prompt 返回终态。
func (a *ACPAgent) waitLegacyPrompt(state legacyPromptState) (string, error) {
	var textParts []string
	for {
		select {
		case <-state.ctx.Done():
			if err := a.cancelLegacyPrompt(state); err != nil {
				return "", err
			}
			return "", state.ctx.Err()
		case update := <-state.notifyCh:
			textParts = appendLegacyChunk(textParts, update)
			emitLegacyProgress(state, update)
		case event := <-state.approvalCh:
			if err := a.handleLegacyInteraction(state.ctx, event); err != nil {
				return "", err
			}
		case done := <-state.promptDone:
			return a.finishLegacyPrompt(state, textParts, done)
		}
	}
}

// cancelLegacyPrompt 将调用方取消同步通知给 ACP runtime。
func (a *ACPAgent) cancelLegacyPrompt(state legacyPromptState) error {
	err := a.notify("session/cancel", map[string]interface{}{"sessionId": state.sessionID})
	if err != nil {
		return fmt.Errorf("%w: session/cancel failed: %v", state.ctx.Err(), err)
	}
	return nil
}

// handleLegacyInteraction 复用统一审批与补充输入处理器。
func (a *ACPAgent) handleLegacyInteraction(ctx context.Context, event *codexTurnEvent) error {
	if event.Approval != nil {
		if err := a.handleCodexApprovalEvent(ctx, event); err != nil {
			return fmt.Errorf("approval response error: %w", err)
		}
		return nil
	}
	if event.UserInput != nil {
		if err := a.handleCodexUserInputEvent(ctx, event); err != nil {
			return fmt.Errorf("user input response error: %w", err)
		}
	}
	return nil
}

// appendLegacyChunk 仅聚合最终正文，普通消息块不作为进度输出。
func appendLegacyChunk(parts []string, update *sessionUpdate) []string {
	if update.SessionUpdate != "agent_message_chunk" {
		return parts
	}
	if text := extractChunkText(update); text != "" {
		return append(parts, text)
	}
	return parts
}

// finishLegacyPrompt 在 RPC 先返回取消错误时补发 cancel，再解析其余终态响应。
func (a *ACPAgent) finishLegacyPrompt(state legacyPromptState, parts []string, done legacyPromptDone) (string, error) {
	if ctxErr := state.ctx.Err(); ctxErr != nil && errors.Is(done.err, ctxErr) {
		if err := a.cancelLegacyPrompt(state); err != nil {
			return "", err
		}
		return "", ctxErr
	}
	for {
		select {
		case update := <-state.notifyCh:
			parts = appendLegacyChunk(parts, update)
			emitLegacyProgress(state, update)
		case event := <-state.approvalCh:
			if err := a.handleLegacyInteraction(state.ctx, event); err != nil {
				return "", err
			}
		default:
			return legacyPromptResult(parts, done)
		}
	}
}

// emitLegacyProgress 只把结构化 Claude ACP 事件发送到实时进度链路。
func emitLegacyProgress(state legacyPromptState, update *sessionUpdate) {
	if state.onProgress == nil || state.progress == nil {
		return
	}
	if text, ok := state.progress.progressText(update); ok {
		state.onProgress(text)
	}
}

// legacyPromptResult 统一处理 RPC 错误、正文聚合和空响应。
func legacyPromptResult(parts []string, done legacyPromptDone) (string, error) {
	if done.err != nil {
		return "", fmt.Errorf("prompt error: %w", done.err)
	}
	result := strings.TrimSpace(strings.Join(parts, ""))
	if result == "" {
		result = extractPromptResultText(done.result)
	}
	if result == "" {
		return "", fmt.Errorf("agent returned empty response")
	}
	return result, nil
}
