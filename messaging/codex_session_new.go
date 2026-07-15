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

// createAndAcquireCodexSessionWithBindingLocked 在调用方持有 binding 锁时完成创建、接管和失败恢复。
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
		markErr := h.markCodexSessionCreateConflict(failure)
		return result, errors.Join(errCodexSessionAcquireUncertain, failure.cause, restoreErr, markErr)
	}
	return result, failure.cause
}

// markCodexSessionCreateConflict 在 mapping 无法恢复时持久标记相关 thread 为冲突态。
func (h *Handler) markCodexSessionCreateConflict(failure codexSessionCreateFailureRequest) error {
	createdThread := strings.TrimSpace(failure.createdThread)
	previousThread := strings.TrimSpace(failure.previousThreadID)
	if createdThread == "" && previousThread == "" {
		return nil
	}
	liveAgent := failure.createRequest.acquire.agent.(agent.CodexLiveRuntimeAgent)
	route := failure.createRequest.acquire.route
	cleanupCtx, cancel := newCodexSessionAcquireCleanupContext(failure.createRequest.acquire.ctx)
	defer cancel()
	seen := make(map[string]struct{}, 2)
	var markErr error
	for _, threadID := range []string{createdThread, previousThread} {
		if threadID == "" {
			continue
		}
		if _, duplicate := seen[threadID]; duplicate {
			continue
		}
		seen[threadID] = struct{}{}
		route.threadID = threadID
		markErr = errors.Join(markErr, h.markCodexRuntimeConflict(codexRuntimeConflictRequest{
			ctx: cleanupCtx, liveAgent: liveAgent,
			change: codexRuntimeIntentChange{threadID: threadID, route: route},
			intent: h.ensureCodexSessions().controlIntent(threadID),
		}))
	}
	return markErr
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

// validateCodexSessionCreatePreflight 确保不会在旧远程任务活动时先破坏 ACP mapping。
func (h *Handler) validateCodexSessionCreatePreflight(req codexSessionAcquireRequest) error {
	snapshot := h.ensureCodexSessions().remoteSelectionSnapshot(req.route.bindingKey, "")
	for threadID := range snapshot.RouteOwned {
		if _, active := h.activeCodexTaskConversation(threadID); active {
			return errCodexSessionAcquireActiveOld
		}
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
		lines := []string{"Codex 控制权移交结果未确认，当前禁止继续写入。"}
		if result.createdThread != "" {
			lines = append(lines, "新会话仍保留在 Codex 历史中。")
		}
		return wechatCommandText(lines...)
	}
	if result.createdThread == "" {
		if errors.Is(err, errCodexSessionAcquireActiveOld) ||
			errors.Is(err, errCodexSessionAcquireUnsupported) ||
			errors.Is(err, errCodexRemoteSelectionOtherRoute) ||
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
		"新 Codex 会话已创建，但接管失败；原会话已恢复。",
		"新会话仍保留在 Codex 历史中。",
		renderCodexSessionAcquireFailure(err),
	)
}
