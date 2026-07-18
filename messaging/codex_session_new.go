package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexSessionCreateRequest struct {
	acquire codexSessionAcquireRequest
}

type codexSessionCreateResult struct {
	acquireResult codexSessionAcquireResult
	createdThread string
	acquireTried  bool
}

type codexSessionRestoreRequest struct {
	ctx              context.Context
	agent            agent.CodexThreadAgent
	conversationID   string
	previousThreadID string
}

type codexSessionCreateFailureRequest struct {
	createRequest    codexSessionCreateRequest
	threadAgent      agent.CodexThreadAgent
	previousThreadID string
	createdThread    string
	acquireTried     bool
	cause            error
}

// createAndAcquireCodexSessionWithBindingLocked 在调用方持有 binding 锁时完成创建、绑定和失败恢复。
func (h *Handler) createAndAcquireCodexSessionWithBindingLocked(req codexSessionCreateRequest) (codexSessionCreateResult, error) {
	if err := h.validateCodexSessionCreatePreflight(req.acquire); err != nil {
		return codexSessionCreateResult{}, err
	}
	threadAgent, ok := req.acquire.agent.(agent.CodexThreadAgent)
	if !ok {
		return codexSessionCreateResult{}, errCodexSessionAcquireUnsupported
	}
	if _, ok := req.acquire.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return codexSessionCreateResult{}, errCodexSessionAcquireUnsupported
	}
	previousThreadID, pending := h.ensureCodexSessions().getThread(
		req.acquire.route.bindingKey, req.acquire.route.workspaceRoot,
	)
	if pending {
		previousThreadID = ""
	}
	h.bindConversationCwd(req.acquire.agent, req.acquire.route.conversationID, req.acquire.route.workspaceRoot)
	created, createErr := req.acquire.agent.ResetSession(req.acquire.ctx, req.acquire.route.conversationID)
	created = strings.TrimSpace(created)
	if createErr != nil || created == "" {
		return h.restoreCodexSessionCreateFailure(codexSessionCreateFailureRequest{
			createRequest: req, threadAgent: threadAgent, previousThreadID: previousThreadID,
			createdThread: created, cause: codexSessionCreateError(createErr, created),
		})
	}
	req.acquire.route.threadID = created
	req.acquire.pendingFirstTurn = true
	result, acquireErr := h.acquireCodexSessionWithBindingLocked(req.acquire)
	if acquireErr == nil {
		return codexSessionCreateResult{acquireResult: result, createdThread: created}, nil
	}
	return h.restoreCodexSessionCreateFailure(codexSessionCreateFailureRequest{
		createRequest: req, threadAgent: threadAgent, previousThreadID: previousThreadID,
		createdThread: created, acquireTried: true, cause: acquireErr,
	})
}

// restoreCodexSessionCreateFailure 恢复 ACP mapping，并保留新 thread 历史事实供用户提示。
func (h *Handler) restoreCodexSessionCreateFailure(failure codexSessionCreateFailureRequest) (codexSessionCreateResult, error) {
	result := codexSessionCreateResult{
		createdThread: failure.createdThread, acquireTried: failure.acquireTried,
	}
	restoreErr := restoreCodexSessionAfterCreateFailure(codexSessionRestoreRequest{
		ctx: failure.createRequest.acquire.ctx, agent: failure.threadAgent,
		conversationID:   failure.createRequest.acquire.route.conversationID,
		previousThreadID: failure.previousThreadID,
	})
	if restoreErr != nil {
		return result, errors.Join(errCodexSessionAcquireUncertain, failure.cause, restoreErr)
	}
	return result, failure.cause
}

// restoreCodexSessionAfterCreateFailure 使用独立清理预算恢复 ResetSession 改变的 mapping。
func restoreCodexSessionAfterCreateFailure(req codexSessionRestoreRequest) error {
	if strings.TrimSpace(req.previousThreadID) == "" {
		req.agent.ClearCodexThread(req.conversationID)
		return nil
	}
	cleanupCtx, cancel := newCodexSessionAcquireCleanupContext(req.ctx)
	defer cancel()
	return req.agent.UseCodexThread(cleanupCtx, req.conversationID, req.previousThreadID)
}

// validateCodexSessionCreatePreflight prevents ResetSession from replacing the
// conversation mapping while that same frontend conversation is running.
func (h *Handler) validateCodexSessionCreatePreflight(req codexSessionAcquireRequest) error {
	if _, active := h.activeTask(req.route.conversationID); active {
		return errCodexSessionAcquireActiveOld
	}
	return nil
}

// codexSessionCreateError 区分 Agent 明确失败与未返回 thread ID 的协议错误。
func codexSessionCreateError(createErr error, created string) error {
	if createErr != nil {
		return fmt.Errorf("创建新的 Codex 会话失败: %w", createErr)
	}
	if strings.TrimSpace(created) == "" {
		return fmt.Errorf("Codex 未返回新会话 ID")
	}
	return nil
}

// renderCodexSessionCreateFailure 区分可确认恢复与不确定结果，避免虚假成功。
func renderCodexSessionCreateFailure(result codexSessionCreateResult, err error) string {
	if errors.Is(err, errCodexSessionAcquireUncertain) {
		lines := []string{"Codex 新会话绑定结果未确认，原会话映射可能未能恢复。"}
		if result.createdThread != "" {
			lines = append(lines, "新会话仍保留在 Codex 历史中。")
		}
		return wechatCommandText(lines...)
	}
	if result.createdThread == "" {
		if errors.Is(err, errCodexSessionAcquireActiveOld) ||
			errors.Is(err, errCodexSessionAcquireUnsupported) ||
			errors.Is(err, errCodexRemoteSelectionChanged) || isCodexSessionControlTimeout(err) {
			return renderCodexSessionAcquireFailure(err)
		}
		return err.Error()
	}
	if !result.acquireTried {
		return wechatCommandText(
			"创建新的 Codex 会话失败；原会话已恢复。",
			"Codex 已返回会话 ID，该会话可能仍保留在历史中。",
			err.Error(),
		)
	}
	return wechatCommandText(
		"新 Codex 会话已创建，但绑定失败；原会话已恢复。",
		"新会话仍保留在 Codex 历史中。",
		renderCodexSessionAcquireFailure(err),
	)
}
