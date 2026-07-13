package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	testACPRetryEnv       = "WECLAW_TEST_ACP_RETRY"
	testACPRetryCountEnv  = "WECLAW_TEST_ACP_RETRY_COUNT"
	testACPRetryPIDLogEnv = "WECLAW_TEST_ACP_RETRY_PID_LOG"
)

// TestClaudeACPConcurrentUseKeepsLatestIntent 验证后登记的切换不会被迟到 RPC 覆盖。
func TestClaudeACPConcurrentUseKeepsLatestIntent(t *testing.T) {
	agent := newClaudeACPSessionTestAgent(t, t.TempDir())
	workspaceA, workspaceB := t.TempDir(), t.TempDir()
	firstListEntered, releaseFirstList := make(chan struct{}), make(chan struct{})
	var listCalls atomic.Int32
	agent.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method == "session/list" {
			if listCalls.Add(1) == 1 {
				close(firstListEntered)
				<-releaseFirstList
			}
			payload := fmt.Sprintf(`{"sessions":[{"sessionId":"session-a","cwd":%q},{"sessionId":"session-b","cwd":%q}]}`, workspaceA, workspaceB)
			return json.RawMessage(payload), nil
		}
		if method != "session/resume" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return json.RawMessage(`{}`), nil
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- agent.UseClaudeSession(context.Background(), "conversation-1", "session-a") }()
	<-firstListEntered
	if err := agent.UseClaudeSession(context.Background(), "conversation-1", "session-b"); err != nil {
		t.Fatalf("later UseClaudeSession error: %v", err)
	}
	close(releaseFirstList)
	if err := <-firstDone; err == nil || !containsAll(err.Error(), "绑定", "过期") {
		t.Fatalf("earlier UseClaudeSession error=%v, want stale binding conflict", err)
	}
	assertClaudeBinding(t, agent, "conversation-1", "session-b", workspaceB)
}

// TestClaudeACPCreateSessionClearInvalidatesCommit 验证清理动作使在途 session/new 失效。
func TestClaudeACPCreateSessionClearInvalidatesCommit(t *testing.T) {
	agent := newClaudeACPSessionTestAgent(t, t.TempDir())
	if err := agent.cacheAndValidateACPCapabilities(claudeCapabilityPayload()); err != nil {
		t.Fatalf("cache capabilities: %v", err)
	}
	agent.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "session/new" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		agent.ClearClaudeSession("conversation-1")
		return json.RawMessage(`{"sessionId":"session-new"}`), nil
	}
	_, err := agent.createSession(context.Background(), "conversation-1")
	if err == nil || !containsAll(err.Error(), "绑定", "过期") {
		t.Fatalf("createSession error=%v, want stale binding conflict", err)
	}
	if sessionID, ok := agent.CurrentClaudeSession("conversation-1"); ok || sessionID != "" {
		t.Fatalf("binding=(%q,%v), want cleared", sessionID, ok)
	}
}

// TestClaudeACPLazyResumeRejectsABA 验证 A 到 B 再到 A 仍会使旧恢复操作失效。
func TestClaudeACPLazyResumeRejectsABA(t *testing.T) {
	agent := newClaudeACPSessionTestAgent(t, t.TempDir())
	if err := agent.cacheAndValidateACPCapabilities(claudeCapabilityPayload()); err != nil {
		t.Fatalf("cache capabilities: %v", err)
	}
	conversationID, workspace := "conversation-1", t.TempDir()
	agent.mu.Lock()
	agent.sessions[conversationID] = "session-a"
	agent.conversationCwds[conversationID] = workspace
	agent.sessionGenerations[conversationID] = 0
	agent.mu.Unlock()
	agent.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "session/resume" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		agent.ClearClaudeSession(conversationID)
		agent.mu.Lock()
		agent.sessions[conversationID] = "session-a"
		agent.conversationCwds[conversationID] = workspace
		agent.sessionGenerations[conversationID] = 0
		agent.mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
	err := agent.resumeClaudeSessionIfStale(context.Background(), conversationID, "session-a")
	if err == nil || !containsAll(err.Error(), "绑定", "变化") {
		t.Fatalf("lazy resume error=%v, want ABA conflict", err)
	}
	if generation := agent.sessionGenerations[conversationID]; generation != 0 {
		t.Fatalf("session generation=%d, want unchanged 0", generation)
	}
}

// TestACPHandshakeIdentityClassChangeClearsBindings 验证身份类别切换会清理旧绑定并按新身份处理 pending。
func TestACPHandshakeIdentityClassChangeClearsBindings(t *testing.T) {
	tests := []struct {
		name          string
		first, second json.RawMessage
		wantSession   string
	}{
		{"generic 到 Claude", genericCapabilityPayload(), claudeCapabilityPayload(), ""},
		{"Claude 到 generic", claudeCapabilityPayload(), genericCapabilityPayload(), "pending-session"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := newIdentitySwitchAgent(t)
			if err := agent.cacheAndValidateACPCapabilities(tc.first); err != nil {
				t.Fatalf("first handshake: %v", err)
			}
			oldRevision := agent.beginBindingIntent("conversation-1")
			agent.mu.Lock()
			agent.sessions["conversation-1"] = "bound-session"
			agent.sessionGenerations["conversation-1"] = 1
			agent.pendingPersistedSessions["conversation-1"] = "pending-session"
			agent.conversationCwds["conversation-1"] = "/kept/workspace"
			agent.mu.Unlock()

			if err := agent.cacheAndValidateACPCapabilities(tc.second); err != nil {
				t.Fatalf("second handshake: %v", err)
			}
			newRevision := agent.beginBindingIntent("conversation-1")
			if newRevision <= oldRevision {
				t.Fatalf("new revision=%d, want greater than old revision=%d", newRevision, oldRevision)
			}
			stale := conversationBindingCommit{sessionID: "stale-session", cwd: "/stale/workspace"}
			if err := agent.commitBindingIntent("conversation-1", oldRevision, stale); err == nil {
				t.Fatal("old revision commit error=nil, want stale conflict")
			}
			agent.mu.Lock()
			defer agent.mu.Unlock()
			if agent.sessions["conversation-1"] != tc.wantSession || len(agent.pendingPersistedSessions) != 0 {
				t.Fatalf("sessions=%#v pending=%#v", agent.sessions, agent.pendingPersistedSessions)
			}
			if len(agent.sessionGenerations) != 0 || agent.bindingRevisions["conversation-1"] != newRevision {
				t.Fatalf("generations=%#v revisions=%#v", agent.sessionGenerations, agent.bindingRevisions)
			}
			if agent.conversationCwds["conversation-1"] != "/kept/workspace" {
				t.Fatalf("cwd=%q, want retained", agent.conversationCwds["conversation-1"])
			}
		})
	}
}

// TestACPAgentStartRetriesAfterCapabilityFailure 验证同一实例可在握手失败后重新启动。
func TestACPAgentStartRetriesAfterCapabilityFailure(t *testing.T) {
	countFile, pidLog := filepath.Join(t.TempDir(), "count"), filepath.Join(t.TempDir(), "pids")
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "generic", Command: os.Args[0], Args: []string{"-test.run=TestHelperACPRetryInitialize"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
		Env: map[string]string{testACPRetryEnv: "1", testACPRetryCountEnv: countFile, testACPRetryPIDLogEnv: pidLog},
	})
	agent.pendingPersistedSessions["conversation-1"] = "persisted-session"
	if err := agent.Start(context.Background()); err == nil || !strings.Contains(err.Error(), testACPResumeCapability) {
		t.Fatalf("first Start error=%v, want missing resume", err)
	}
	assertRetryState(t, agent, 0, 1, "")
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("second Start error: %v", err)
	}
	assertRetryState(t, agent, 1, 0, "persisted-session")
	agent.Stop()
	assertRetryProcessesExited(t, pidLog, 2)
}

// newIdentitySwitchAgent 创建仅由握手结果决定 Claude 身份的测试 Agent。
func newIdentitySwitchAgent(t *testing.T) *ACPAgent {
	t.Helper()
	return NewACPAgent(ACPAgentConfig{ConfiguredName: "generic", Command: "mock-acp", Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json")})
}

// assertClaudeBinding 同时校验 session 与 cwd，确保绑定提交完整原子。
func assertClaudeBinding(t *testing.T, agent *ACPAgent, conversationID string, sessionID string, cwd string) {
	t.Helper()
	if got, ok := agent.CurrentClaudeSession(conversationID); !ok || got != sessionID {
		t.Fatalf("binding=(%q,%v), want %q", got, ok, sessionID)
	}
	if got := agent.cwdForConversation(conversationID); got != cwd {
		t.Fatalf("cwd=%q, want %q", got, cwd)
	}
}

// assertRetryState 校验握手失败与重试成功后的核心状态。
func assertRetryState(t *testing.T, agent *ACPAgent, generation uint64, pending int, session string) {
	t.Helper()
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.legacyRuntimeGeneration != generation || len(agent.pendingPersistedSessions) != pending || agent.sessions["conversation-1"] != session {
		t.Fatalf("generation=%d pending=%#v sessions=%#v", agent.legacyRuntimeGeneration, agent.pendingPersistedSessions, agent.sessions)
	}
}

// assertRetryProcessesExited 校验每次真实启动产生的子进程均已退出。
func assertRetryProcessesExited(t *testing.T, pidLog string, want int) {
	t.Helper()
	data, err := os.ReadFile(pidLog)
	if err != nil {
		t.Fatalf("read pid log: %v", err)
	}
	lines := strings.Fields(string(data))
	if len(lines) != want {
		t.Fatalf("pid count=%d, want %d", len(lines), want)
	}
	for _, line := range lines {
		pid, parseErr := strconv.Atoi(line)
		if parseErr != nil {
			t.Fatalf("parse pid %q: %v", line, parseErr)
		}
		assertProcessExited(t, pid)
	}
}

// TestHelperACPRetryInitialize 按启动次数返回失败或成功的 initialize 结果。
func TestHelperACPRetryInitialize(t *testing.T) {
	if os.Getenv(testACPRetryEnv) != "1" {
		return
	}
	attempt := incrementRetryAttempt(os.Getenv(testACPRetryCountEnv))
	appendRetryPID(os.Getenv(testACPRetryPIDLogEnv))
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	var request rpcRequest
	if json.Unmarshal(scanner.Bytes(), &request) != nil {
		os.Exit(3)
	}
	payload := genericCapabilityPayload()
	if attempt == 1 {
		payload = json.RawMessage(`{"protocolVersion":1,"agentInfo":{"name":"claude-agent-acp"},"agentCapabilities":{"sessionCapabilities":{"list":{}}}}`)
	}
	if json.NewEncoder(os.Stdout).Encode(rpcResponse{JSONRPC: "2.0", ID: &request.ID, Result: payload}) != nil {
		os.Exit(4)
	}
	for scanner.Scan() {
	}
}

// incrementRetryAttempt 通过计数文件区分两个顺序启动的测试子进程。
func incrementRetryAttempt(path string) int {
	data, _ := os.ReadFile(path)
	attempt, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	attempt++
	if os.WriteFile(path, []byte(strconv.Itoa(attempt)), 0o600) != nil {
		os.Exit(5)
	}
	return attempt
}

// appendRetryPID 记录每次测试子进程 PID，供父进程验证回收结果。
func appendRetryPID(path string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(6)
	}
	_, err = fmt.Fprintln(file, os.Getpid())
	if closeErr := file.Close(); err != nil || closeErr != nil {
		os.Exit(7)
	}
}

// assertProcessExited 使用 signal 0 等待指定测试子进程彻底退出。
func assertProcessExited(t *testing.T, pid int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Fatal("Windows 尚无可靠的 signal 0 进程退出探测")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find process %d: %v", pid, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		signalErr := process.Signal(syscall.Signal(0))
		if errors.Is(signalErr, os.ErrProcessDone) || errors.Is(signalErr, syscall.ESRCH) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("helper process pid=%d still exists", pid)
}
