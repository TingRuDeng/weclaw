package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// isClaudeACP 只使用显式配置名或标准握手身份判断 Claude，不依赖命令路径。
func (a *ACPAgent) isClaudeACP() bool {
	if a.protocol != protocolLegacyACP {
		return false
	}
	snapshot := a.acpCapabilitiesSnapshot()
	return requiresClaudeSessionCapabilities(a.configuredName, snapshot.AgentInfo)
}

// requireSession 返回普通聊天已经绑定的 ACP session，不承担创建职责。
func (a *ACPAgent) requireSession(conversationID string) (string, error) {
	a.mu.Lock()
	sessionID, exists := a.sessions[conversationID]
	a.mu.Unlock()
	if !exists || strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("%w: conversation=%s", ErrAgentSessionNotBound, conversationID)
	}
	return sessionID, nil
}

// hasLegacySessionCandidate 判断聊天能否先启动 runtime 完成 ACP 身份识别。
func (a *ACPAgent) hasLegacySessionCandidate(conversationID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.TrimSpace(a.sessions[conversationID]) != "" ||
		strings.TrimSpace(a.pendingPersistedSessions[conversationID]) != ""
}

// createSession 创建并保存一个由用户显式请求的新标准 ACP session。
func (a *ACPAgent) createSession(ctx context.Context, conversationID string) (string, error) {
	revision := a.beginBindingIntent(conversationID)
	cwd := a.cwdForConversation(conversationID)
	result, err := a.rpc(ctx, "session/new", newSessionParams{
		Cwd:        cwd,
		McpServers: []interface{}{},
	})
	if err != nil {
		return "", err
	}
	session, err := parseNewACPSession(result)
	if err != nil {
		return "", err
	}
	if a.isClaudeACP() || supportsClaudeSessionConfig(session.ConfigOptions) {
		if err := a.configureClaudeSession(ctx, session.SessionID, session.ConfigOptions); err != nil {
			return "", err
		}
	}
	commit := conversationBindingCommit{sessionID: session.SessionID, cwd: cwd}
	if err := a.commitBindingIntent(conversationID, revision, commit); err != nil {
		return "", err
	}
	a.persistState()
	return session.SessionID, nil
}

type conversationBindingCommit struct {
	sessionID string
	cwd       string
}

// beginBindingIntent 在任何绑定 RPC 前登记操作顺序，后发操作拥有更高 revision。
func (a *ACPAgent) beginBindingIntent(conversationID string) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.advanceBindingRevisionLocked(conversationID)
}

// advanceBindingRevisionLocked 分配进程内不复用的 revision，避免清表后的 ABA。
func (a *ACPAgent) advanceBindingRevisionLocked(conversationID string) uint64 {
	a.bindingRevisionCounter++
	a.bindingRevisions[conversationID] = a.bindingRevisionCounter
	return a.bindingRevisionCounter
}

// commitBindingIntent 仅允许当前 revision 提交完整绑定状态。
func (a *ACPAgent) commitBindingIntent(conversationID string, revision uint64, commit conversationBindingCommit) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bindingRevisions[conversationID] != revision {
		return fmt.Errorf("Claude 会话绑定已变化，当前操作已过期")
	}
	a.sessions[conversationID] = commit.sessionID
	a.conversationCwds[conversationID] = commit.cwd
	a.sessionGenerations[conversationID] = a.legacyRuntimeGeneration
	return nil
}

type legacySessionGenerationState struct {
	sessionID         string
	cwd               string
	runtimeGeneration uint64
	bindingRevision   uint64
}

// resumeClaudeSessionIfStale 在新 runtime 首次 prompt 前恢复已有 Claude session。
func (a *ACPAgent) resumeClaudeSessionIfStale(ctx context.Context, conversationID string, sessionID string) error {
	state, stale := a.staleClaudeSessionState(conversationID, sessionID)
	if !stale {
		return nil
	}
	params := acpSessionResumeParams{SessionID: state.sessionID, Cwd: state.cwd, McpServers: []interface{}{}}
	result, err := a.rpc(ctx, "session/resume", params)
	if err != nil {
		return fmt.Errorf("session/resume 懒恢复失败: %w", err)
	}
	if err := validateACPObjectResult(result, "session/resume"); err != nil {
		return err
	}
	return a.commitClaudeSessionGeneration(conversationID, state)
}

// staleClaudeSessionState 在同一锁区读取绑定、cwd 与 runtime 代次。
func (a *ACPAgent) staleClaudeSessionState(conversationID string, sessionID string) (legacySessionGenerationState, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.isClaudeACPIdentityLocked() || a.sessions[conversationID] != sessionID {
		return legacySessionGenerationState{}, false
	}
	generation := a.legacyRuntimeGeneration
	if a.sessionGenerations[conversationID] >= generation {
		return legacySessionGenerationState{}, false
	}
	return legacySessionGenerationState{
		sessionID: sessionID, cwd: a.legacySessionCwdLocked(conversationID), runtimeGeneration: generation,
		bindingRevision: a.bindingRevisions[conversationID],
	}, true
}

// commitClaudeSessionGeneration 仅在绑定和 runtime 未并发变化时提交恢复代次。
func (a *ACPAgent) commitClaudeSessionGeneration(conversationID string, state legacySessionGenerationState) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	unchanged := a.sessions[conversationID] == state.sessionID &&
		a.legacySessionCwdLocked(conversationID) == state.cwd &&
		a.legacyRuntimeGeneration == state.runtimeGeneration &&
		a.bindingRevisions[conversationID] == state.bindingRevision
	if !unchanged {
		return fmt.Errorf("session/resume 完成前 Claude 会话绑定或 runtime 已变化")
	}
	a.sessionGenerations[conversationID] = state.runtimeGeneration
	return nil
}

// legacySessionCwdLocked 返回 conversation 固定 cwd，未设置时使用 Agent 默认值。
func (a *ACPAgent) legacySessionCwdLocked(conversationID string) string {
	if cwd := strings.TrimSpace(a.conversationCwds[conversationID]); cwd != "" {
		return cwd
	}
	return a.cwd
}

// supportsClaudeSessionConfig 依据 session/new 的显式配置契约决定是否应用模型配置。
func supportsClaudeSessionConfig(options []acpSessionConfigOption) bool {
	for _, option := range options {
		if option.ID == claudeModelConfigID || option.ID == claudeEffortConfigID {
			return true
		}
	}
	return false
}

func parseNewACPSession(result json.RawMessage) (newSessionResult, error) {
	var session newSessionResult
	if err := json.Unmarshal(result, &session); err != nil {
		return newSessionResult{}, fmt.Errorf("parse session result: %w", err)
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return newSessionResult{}, fmt.Errorf("session/new returned empty session id")
	}
	return session, nil
}

// CurrentClaudeSession 返回 conversation 当前进程内绑定的 Claude ACP session。
func (a *ACPAgent) CurrentClaudeSession(conversationID string) (string, bool) {
	if !a.isClaudeACP() {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	sessionID := strings.TrimSpace(a.sessions[conversationID])
	return sessionID, sessionID != ""
}

// ClearClaudeSession 仅清理进程内 session 绑定，保留 conversation 工作目录。
func (a *ACPAgent) ClearClaudeSession(conversationID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.isClaudeACPIdentityLocked() {
		return
	}
	a.advanceBindingRevisionLocked(conversationID)
	delete(a.sessions, conversationID)
	delete(a.sessionGenerations, conversationID)
}
