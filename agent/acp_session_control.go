package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// SetCwd changes the working directory for subsequent sessions.
func (a *ACPAgent) SetCwd(cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd = cwd
}

// SetConversationCwd 固定单个 conversation 的工作目录，避免后台任务被全局 cwd 切换影响。
func (a *ACPAgent) SetConversationCwd(conversationID string, cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		delete(a.conversationCwds, conversationID)
		return
	}
	a.conversationCwds[conversationID] = cwd
}

func (a *ACPAgent) cwdForConversation(conversationID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cwd := strings.TrimSpace(a.conversationCwds[conversationID]); cwd != "" {
		return cwd
	}
	return a.cwd
}

// ResetSession clears the existing session for the given conversationID and
// immediately creates a new one, returning the new session ID.
func (a *ACPAgent) ResetSession(ctx context.Context, conversationID string) (string, error) {
	if err := a.ensureStarted(ctx); err != nil {
		return "", err
	}

	if a.protocol == protocolCodexAppServer {
		a.mu.Lock()
		delete(a.threads, conversationID)
		delete(a.resumeOnFirstUse, conversationID)
		delete(a.history, conversationID)
		a.mu.Unlock()
		a.persistState()
		log.Printf("[acp] thread reset (conversation=%s), creating new thread", conversationID)

		return a.createResetCodexThread(ctx, conversationID)
	}

	a.mu.Lock()
	delete(a.sessions, conversationID)
	delete(a.history, conversationID)
	a.mu.Unlock()
	a.persistState()
	log.Printf("[acp] session reset (conversation=%s), creating new session", conversationID)

	sessionID, _, err := a.getOrCreateSession(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("create new session: %w", err)
	}
	return sessionID, nil
}

// createResetCodexThread 处理 /new 的新 thread 创建，并在 stdin 关闭时重启一次。
func (a *ACPAgent) createResetCodexThread(ctx context.Context, conversationID string) (string, error) {
	threadID, _, err := a.getOrCreateThread(ctx, conversationID)
	if err == nil {
		return threadID, nil
	}
	if !isClosedStdinError(err) {
		return "", fmt.Errorf("create new thread: %w", err)
	}
	log.Printf("[acp] codex stdin is closed during reset, restarting runtime (conversation=%s): %v", conversationID, err)
	a.Stop()
	if err := a.Start(ctx); err != nil {
		return "", fmt.Errorf("restart codex runtime: %w", err)
	}
	threadID, _, err = a.getOrCreateThread(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("create new thread after runtime restart: %w", err)
	}
	return threadID, nil
}
