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
	if handled, err := a.bindKnownDesktopThread(conversationID, threadID); handled {
		return err
	}
	if err := a.resumeThread(ctx, conversationID, threadID); err != nil {
		return fmt.Errorf("resume thread %s: %w", threadID, err)
	}
	a.mu.Lock()
	a.threads[conversationID] = threadID
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	a.persistState()
	return nil
}

// bindKnownDesktopThread 防止旧入口通过 app-server 抢占 Desktop 正在持有的 thread。
func (a *ACPAgent) bindKnownDesktopThread(conversationID string, threadID string) (bool, error) {
	if a.codexOwners == nil {
		return false, nil
	}
	binding, ok := a.codexOwners.threadBinding(threadID)
	if !ok || (binding.Owner != CodexOwnerDesktopLive && binding.Owner != CodexOwnerDesktopDisconnected) {
		return false, nil
	}
	a.codexOwners.bindConversation(CodexThreadRef{
		ConversationID: conversationID,
		ThreadID:       threadID,
	}, binding)
	a.persistState()
	if binding.Owner == CodexOwnerDesktopDisconnected {
		return true, ErrCodexDesktopDisconnected
	}
	return true, nil
}

// ClearCodexThread 清理指定会话的 Codex thread，下一条消息会创建新 thread。
func (a *ACPAgent) ClearCodexThread(conversationID string) {
	if a.protocol != protocolCodexAppServer {
		return
	}
	a.clearCodexThread(conversationID)
}

// clearACPSession 删除旧 ACP session 映射，避免恢复到服务端已经不存在的 session。
func (a *ACPAgent) clearACPSession(conversationID string) string {
	a.mu.Lock()
	oldSessionID := a.sessions[conversationID]
	delete(a.sessions, conversationID)
	a.mu.Unlock()
	a.persistState()
	return oldSessionID
}

func (a *ACPAgent) getOrCreateSession(ctx context.Context, conversationID string) (string, bool, error) {
	a.mu.Lock()
	sid, exists := a.sessions[conversationID]
	a.mu.Unlock()

	if exists {
		return sid, false, nil
	}

	result, err := a.rpc(ctx, "session/new", newSessionParams{
		Cwd:        a.cwdForConversation(conversationID),
		McpServers: []interface{}{},
	})
	if err != nil {
		return "", false, err
	}

	var sessionResult newSessionResult
	if err := json.Unmarshal(result, &sessionResult); err != nil {
		return "", false, fmt.Errorf("parse session result: %w", err)
	}
	if sessionResult.SessionID == "" {
		return "", false, fmt.Errorf("session/new returned empty session id")
	}
	if a.isClaudeLegacyACP() {
		if err := a.configureClaudeSession(ctx, sessionResult.SessionID, sessionResult.ConfigOptions); err != nil {
			return "", false, err
		}
	}

	a.mu.Lock()
	a.sessions[conversationID] = sessionResult.SessionID
	a.mu.Unlock()
	a.persistState()

	return sessionResult.SessionID, true, nil
}

func (a *ACPAgent) getOrCreateThread(ctx context.Context, conversationID string) (string, bool, error) {
	a.mu.Lock()
	tid, exists := a.threads[conversationID]
	shouldResume := exists && a.resumeOnFirstUse[conversationID]
	a.mu.Unlock()

	if exists {
		if shouldResume {
			if err := a.resumeThread(ctx, conversationID, tid); err != nil {
				return "", false, fmt.Errorf("resume restored thread %s: %w", tid, err)
			}
			a.mu.Lock()
			delete(a.resumeOnFirstUse, conversationID)
			a.mu.Unlock()
			log.Printf("[acp] restored thread resumed (conversation=%s, thread=%s)", conversationID, tid)
		}
		return tid, false, nil
	}

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
	startedAt := time.Now()
	result, err := a.rpc(ctx, "thread/start", params)
	elapsed := time.Since(startedAt)
	if err != nil {
		log.Printf("[acp] thread/start failed (conversation=%s, elapsed=%s): %v", conversationID, elapsed, err)
		return "", false, err
	}
	log.Printf("[acp] thread/start completed (conversation=%s, elapsed=%s)", conversationID, elapsed)

	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &threadResult); err != nil {
		return "", false, fmt.Errorf("parse thread/start result: %w", err)
	}
	if threadResult.Thread.ID == "" {
		return "", false, fmt.Errorf("thread/start returned empty thread id")
	}

	a.mu.Lock()
	a.threads[conversationID] = threadResult.Thread.ID
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	a.persistState()

	return threadResult.Thread.ID, true, nil
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
	config := a.modelConfigSnapshot()
	if config.model != "" {
		params["model"] = config.model
	}
	if config.effort != "" {
		params["effort"] = config.effort
	}

	startedAt := time.Now()
	result, err := a.rpc(ctx, "thread/resume", params)
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
	if err := json.Unmarshal(result, &resumeResult); err == nil && resumeResult.Thread.ID != "" && resumeResult.Thread.ID != threadID {
		log.Printf("[acp] thread/resume returned different id (requested=%s, returned=%s)", threadID, resumeResult.Thread.ID)
	}
	return nil
}
