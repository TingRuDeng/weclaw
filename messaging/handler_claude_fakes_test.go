package messaging

import (
	"context"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeClaudeSessionAgent struct {
	fakeAgent
	sessionID        string
	catalogSessions  []agent.ClaudeSession
	catalogErr       error
	listCalls        int
	useConversation  string
	useSessionID     string
	useCalls         []string
	clearCalledWith  string
	useErr           error
	useErrors        []error
	resetErr         error
	resetClears      bool
	sessionConfig    agent.ClaudeSessionConfig
	conversationCwds map[string]string
	runtimeSessions  map[string]string
}

func (f *fakeClaudeSessionAgent) ResetSession(_ context.Context, conversationID string) (string, error) {
	f.fakeAgent.mu.Lock()
	f.fakeAgent.resetConversation = conversationID
	sessionID := f.fakeAgent.resetSessionID
	f.fakeAgent.mu.Unlock()
	if f.resetClears {
		f.sessionID = ""
	}
	return sessionID, f.resetErr
}

func (f *fakeClaudeSessionAgent) ListClaudeSessions(context.Context) ([]agent.ClaudeSession, error) {
	f.listCalls++
	return append([]agent.ClaudeSession(nil), f.catalogSessions...), f.catalogErr
}

func (f *fakeClaudeSessionAgent) SetConversationCwd(conversationID string, cwd string) {
	if f.conversationCwds == nil {
		f.conversationCwds = make(map[string]string)
	}
	f.conversationCwds[conversationID] = cwd
}

func (f *fakeClaudeSessionAgent) CurrentClaudeSession(conversationID string) (string, bool) {
	if sessionID := f.runtimeSessions[conversationID]; sessionID != "" {
		return sessionID, true
	}
	if f.sessionID == "" {
		return "", false
	}
	return f.sessionID, true
}

func (f *fakeClaudeSessionAgent) UseClaudeSession(_ context.Context, conversationID string, sessionID string) error {
	f.useConversation = conversationID
	f.useSessionID = sessionID
	f.useCalls = append(f.useCalls, sessionID)
	if len(f.useErrors) > 0 {
		err := f.useErrors[0]
		f.useErrors = f.useErrors[1:]
		if err != nil {
			return err
		}
	}
	if f.useErr != nil {
		return f.useErr
	}
	f.sessionID = sessionID
	if f.runtimeSessions == nil {
		f.runtimeSessions = make(map[string]string)
	}
	f.runtimeSessions[conversationID] = sessionID
	return nil
}

func (f *fakeClaudeSessionAgent) ClaudeSessionConfig(string) (agent.ClaudeSessionConfig, bool) {
	return f.sessionConfig, f.sessionConfig.Model != "" || f.sessionConfig.Effort != ""
}

func (f *fakeClaudeSessionAgent) SetClaudeSessionConfig(context.Context, agent.ClaudeSessionConfigUpdate) error {
	return nil
}

func (f *fakeClaudeSessionAgent) ClearClaudeSession(conversationID string) {
	f.clearCalledWith = conversationID
	delete(f.runtimeSessions, conversationID)
	if len(f.runtimeSessions) == 0 {
		f.sessionID = ""
	}
}
