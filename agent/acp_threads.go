package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// isClosedStdinError 判断 JSON-RPC 写入失败是否来自已失效的子进程 stdin。
func isClosedStdinError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "write to stdin") &&
		(strings.Contains(text, "file already closed") ||
			strings.Contains(text, "broken pipe") ||
			strings.Contains(text, "closed pipe") ||
			strings.Contains(text, "acp runtime is not running"))
}

// CurrentCodexThread 返回指定会话当前绑定的 Codex thread。
func (a *ACPAgent) CurrentCodexThread(conversationID string) (string, bool) {
	if a.protocol != protocolCodexAppServer {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	threadID := strings.TrimSpace(a.threads[conversationID])
	return threadID, threadID != ""
}

// UseCodexThread 将指定会话切换到已有 Codex thread，并先 resume 验证可用性。
func (a *ACPAgent) UseCodexThread(ctx context.Context, conversationID string, threadID string) error {
	if a.protocol != protocolCodexAppServer {
		return fmt.Errorf("agent is not codex app-server")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return fmt.Errorf("empty thread id")
	}
	if a.desktopProbe != nil {
		if handled, err := a.bindKnownDesktopThread(conversationID, threadID); handled {
			return err
		}
	}
	if err := a.resumeThread(ctx, conversationID, threadID); err != nil {
		return fmt.Errorf("resume thread %s: %w", threadID, err)
	}
	a.mu.Lock()
	a.threads[conversationID] = threadID
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	if a.codexOwners != nil {
		a.codexOwners.claimWeClawConversation(CodexThreadRef{
			ConversationID: conversationID, ThreadID: threadID,
		}, CodexThreadState{ThreadID: threadID})
	}
	a.persistState()
	return nil
}

// bindKnownDesktopThread 防止旧入口通过 app-server 抢占 Desktop 正在持有的 thread。
func (a *ACPAgent) bindKnownDesktopThread(conversationID string, threadID string) (bool, error) {
	if a.codexOwners == nil {
		return false, nil
	}
	binding, ok := a.codexOwners.threadBinding(threadID)
	if !ok || binding.Runtime != CodexRuntimeDesktop {
		return false, nil
	}
	a.codexOwners.bindConversation(CodexThreadRef{
		ConversationID: conversationID,
		ThreadID:       threadID,
	}, binding)
	a.persistState()
	return true, nil
}

// ClearCodexThread 清理指定会话的 Codex thread；后续必须由用户切换会话或显式新建。
func (a *ACPAgent) ClearCodexThread(conversationID string) {
	if a.protocol != protocolCodexAppServer {
		return
	}
	a.clearCodexThread(conversationID)
}

// requireThread 返回普通聊天已经绑定的 Codex thread，必要时恢复同一 thread。
func (a *ACPAgent) requireThread(ctx context.Context, conversationID string) (string, error) {
	a.mu.Lock()
	tid, exists := a.threads[conversationID]
	shouldResume := exists && a.resumeOnFirstUse[conversationID]
	a.mu.Unlock()

	if !exists || strings.TrimSpace(tid) == "" {
		return "", fmt.Errorf("%w: conversation=%s", ErrAgentSessionNotBound, conversationID)
	}
	if shouldResume {
		if err := a.resumeThread(ctx, conversationID, tid); err != nil {
			return "", fmt.Errorf("resume restored thread %s: %w", tid, err)
		}
		a.mu.Lock()
		delete(a.resumeOnFirstUse, conversationID)
		a.mu.Unlock()
		log.Printf("[acp] restored thread resumed (conversation=%s, thread=%s)", conversationID, tid)
	}
	return tid, nil
}

// createThread 创建并保存一个由用户显式请求的新 Codex thread。
func (a *ACPAgent) createThread(ctx context.Context, conversationID string) (string, error) {
	params := a.codexThreadStartParams(ctx, conversationID)
	startedAt := time.Now()
	result, sequence, err := a.rpcWithSequence(ctx, "thread/start", params)
	elapsed := time.Since(startedAt)
	if err != nil {
		log.Printf("[acp] thread/start failed (conversation=%s, elapsed=%s): %v", conversationID, elapsed, err)
		return "", err
	}
	log.Printf("[acp] thread/start completed (conversation=%s, elapsed=%s)", conversationID, elapsed)

	threadID, err := codexThreadIDFromStartResult(result)
	if err != nil {
		return "", err
	}
	a.cacheCodexThreadConfigFromLifecycleResult(result, threadID, CodexThreadConfig{
		Model:            stringMapValue(params, "model"),
		Effort:           stringMapValue(params, "effort"),
		ServiceTier:      stringMapValue(params, "serviceTier"),
		ServiceTierKnown: mapHasKey(params, "serviceTier"),
	}, sequence)
	a.mu.Lock()
	a.threads[conversationID] = threadID
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	if a.codexOwners != nil {
		a.codexOwners.claimWeClawConversation(CodexThreadRef{
			ConversationID: conversationID, ThreadID: threadID,
		}, CodexThreadState{ThreadID: threadID})
	}
	a.persistState()
	return threadID, nil
}

// codexThreadStartParams 组装显式新建 thread 所需的会话配置。
func (a *ACPAgent) codexThreadStartParams(ctx context.Context, conversationID string) map[string]interface{} {
	params := map[string]interface{}{
		"approvalPolicy":         a.approvalPolicyForContext(ctx),
		"cwd":                    a.cwdForConversation(conversationID),
		"sandbox":                a.sandboxModeForCodex(),
		"persistExtendedHistory": true,
	}
	if reviewer := a.approvalReviewerForCodex(); reviewer != "" {
		params["approvalsReviewer"] = reviewer
	}
	config := a.modelConfigSnapshot()
	if config.model != "" {
		params["model"] = config.model
	}
	if config.effort != "" {
		params["effort"] = config.effort
	}
	if config.serviceTier != "" {
		if config.serviceTier == CodexServiceTierStandard {
			params["serviceTier"] = nil
		} else {
			params["serviceTier"] = config.serviceTier
		}
	}
	return params
}

// codexThreadIDFromStartResult 校验 thread/start 响应并提取 thread ID。
func codexThreadIDFromStartResult(result json.RawMessage) (string, error) {
	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &threadResult); err != nil {
		return "", fmt.Errorf("parse thread/start result: %w", err)
	}
	if threadResult.Thread.ID == "" {
		return "", fmt.Errorf("thread/start returned empty thread id")
	}

	return threadResult.Thread.ID, nil
}

func (a *ACPAgent) resumeThread(ctx context.Context, conversationID string, threadID string) error {
	if threadID == "" {
		return fmt.Errorf("empty thread id")
	}

	params := map[string]interface{}{
		"threadId":       threadID,
		"approvalPolicy": a.approvalPolicyForContext(ctx),
		"cwd":            a.cwdForConversation(conversationID),
		"sandbox":        a.sandboxModeForCodex(),
	}
	if reviewer := a.approvalReviewerForCodex(); reviewer != "" {
		params["approvalsReviewer"] = reviewer
	}
	startedAt := time.Now()
	result, sequence, err := a.rpcWithSequence(ctx, "thread/resume", params)
	elapsed := time.Since(startedAt)
	if err != nil {
		log.Printf("[acp] thread/resume failed (thread=%s, conversation=%s, elapsed=%s): %v", threadID, conversationID, elapsed, err)
		return err
	}
	log.Printf("[acp] thread/resume completed (thread=%s, conversation=%s, elapsed=%s)", threadID, conversationID, elapsed)

	var resumeResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	a.cacheCodexThreadConfigFromLifecycleResult(result, threadID, CodexThreadConfig{}, sequence)
	if err := json.Unmarshal(result, &resumeResult); err == nil && resumeResult.Thread.ID != "" && resumeResult.Thread.ID != threadID {
		log.Printf("[acp] thread/resume returned different id (requested=%s, returned=%s)", threadID, resumeResult.Thread.ID)
	}
	return nil
}

func stringMapValue(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func mapHasKey(values map[string]interface{}, key string) bool {
	_, ok := values[key]
	return ok
}
