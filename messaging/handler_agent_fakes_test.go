package messaging

import (
	"context"
	"sync"

	"github.com/fastclaw-ai/weclaw/agent"
)

func newTestHandler() *Handler {
	return &Handler{agents: make(map[string]agent.Agent)}
}

type fakeAgent struct {
	mu                 sync.Mutex
	reply              string
	err                error
	chatCalled         bool
	chatCalls          int
	lastConversationID string
	lastMessage        string
	lastCwd            string
	resetConversation  string
	resetSessionID     string
	info               agent.AgentInfo
}

func (f *fakeAgent) Chat(_ context.Context, conversationID string, message string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.chatCalled = true
	f.chatCalls++
	f.lastConversationID = conversationID
	f.lastMessage = message
	return f.reply, f.err
}

func (f *fakeAgent) ResetSession(_ context.Context, conversationID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.resetConversation = conversationID
	return f.resetSessionID, nil
}

func (f *fakeAgent) Info() agent.AgentInfo {
	if f.info.Name != "" {
		return f.info
	}
	return agent.AgentInfo{Name: "fake", Type: "test", Model: "mock", Command: "fake"}
}

func (f *fakeAgent) SetCwd(cwd string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastCwd = cwd
}

func (f *fakeAgent) wasChatCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chatCalled
}

func (f *fakeAgent) chatCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chatCalls
}

func (f *fakeAgent) lastChatConversationID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastConversationID
}

func (f *fakeAgent) lastChatMessage() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastMessage
}

func (f *fakeAgent) lastWorkingDir() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCwd
}

func (f *fakeAgent) resetConversationID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetConversation
}

type fakeCodexThreadAgent struct {
	fakeAgent
	threadID        string
	useConversation string
	useThreadID     string
	clearCalledWith string
	useErr          error
	modelStatus     agent.CodexModelStatus
	models          []agent.CodexModel
	modelErr        error
	quota           agent.CodexQuota
	quotaErr        error
}

type fakeVisibleCodexAgent struct {
	fakeCodexThreadAgent
	openCalls   int
	detachCalls int
	detachOK    bool
	openErr     error
}

type fakeClaudeSessionAgent struct {
	fakeAgent
	sessionID       string
	useConversation string
	useSessionID    string
	clearCalledWith string
	useErr          error
}

func (f *fakeClaudeSessionAgent) CurrentClaudeSession(conversationID string) (string, bool) {
	if f.sessionID == "" {
		return "", false
	}
	return f.sessionID, true
}

func (f *fakeClaudeSessionAgent) UseClaudeSession(_ context.Context, conversationID string, sessionID string) error {
	f.useConversation = conversationID
	f.useSessionID = sessionID
	if f.useErr != nil {
		return f.useErr
	}
	f.sessionID = sessionID
	return nil
}

func (f *fakeClaudeSessionAgent) ClearClaudeSession(conversationID string) {
	f.clearCalledWith = conversationID
	f.sessionID = ""
}

func (f *fakeVisibleCodexAgent) OpenVisibleCompanion(_ context.Context) error {
	f.openCalls++
	return f.openErr
}

func (f *fakeVisibleCodexAgent) DetachVisibleCompanion() bool {
	f.detachCalls++
	return f.detachOK
}

type recordedCodexAppOpen struct {
	command   string
	workspace string
}

type recordedCodexCLIResume struct {
	command   string
	workspace string
	threadID  string
}

type recordedClaudeCLIResume struct {
	command   string
	workspace string
	sessionID string
}

func (f *fakeCodexThreadAgent) CurrentCodexThread(conversationID string) (string, bool) {
	if f.threadID == "" {
		return "", false
	}
	return f.threadID, true
}

func (f *fakeCodexThreadAgent) UseCodexThread(_ context.Context, conversationID string, threadID string) error {
	f.useConversation = conversationID
	f.useThreadID = threadID
	if f.useErr != nil {
		return f.useErr
	}
	f.threadID = threadID
	return nil
}

func (f *fakeCodexThreadAgent) ClearCodexThread(conversationID string) {
	f.clearCalledWith = conversationID
	f.threadID = ""
}

func (f *fakeCodexThreadAgent) CodexModelStatus() agent.CodexModelStatus {
	return f.modelStatus
}

func (f *fakeCodexThreadAgent) ListCodexModels(_ context.Context) ([]agent.CodexModel, error) {
	if f.modelErr != nil {
		return nil, f.modelErr
	}
	return f.models, nil
}

func (f *fakeCodexThreadAgent) ReadCodexQuota(_ context.Context) (agent.CodexQuota, error) {
	if f.quotaErr != nil {
		return agent.CodexQuota{}, f.quotaErr
	}
	return f.quota, nil
}
