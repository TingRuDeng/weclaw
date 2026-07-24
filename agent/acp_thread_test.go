package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testCodexAppServerEnv = "WECLAW_TEST_CODEX_APP_SERVER"
	testACPExitEnv        = "WECLAW_TEST_ACP_EXIT"
	testCodexThreadID     = "thread-new"
)

// createCodexThreadForTest 显式建立测试会话，避免测试依赖普通消息隐式创建。
func createCodexThreadForTest(t *testing.T, ctx context.Context, a *ACPAgent, conversationID string) {
	t.Helper()
	if _, err := a.createThread(ctx, conversationID); err != nil {
		t.Fatalf("createThread(%s) error: %v", conversationID, err)
	}
}

func TestACPAgentPersistsAndRestoresCodexThread(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	if a.protocol != protocolCodexAppServer {
		t.Fatalf("protocol = %q, want %q", a.protocol, protocolCodexAppServer)
	}

	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	threadID, err := a.createThread(ctx, "user-1")
	if err != nil {
		t.Fatalf("createThread error: %v", err)
	}
	if threadID != "thread-1" {
		t.Fatalf("threadID = %q, want %q", threadID, "thread-1")
	}

	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "thread-1" {
		t.Fatalf("persisted thread for user-1 = %q, want %q", got, "thread-1")
	}

	b := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	calls := map[string]int{}
	b.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		calls[method]++
		switch method {
		case "thread/resume":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method on restore: %s", method)
		}
	}

	restoredThreadID, err := b.requireThread(ctx, "user-1")
	if err != nil {
		t.Fatalf("requireThread(restore) error: %v", err)
	}
	if restoredThreadID != "thread-1" {
		t.Fatalf("restored threadID = %q, want %q", restoredThreadID, "thread-1")
	}
	if calls["thread/resume"] != 1 {
		t.Fatalf("thread/resume calls after first restore = %d, want 1", calls["thread/resume"])
	}

	_, err = b.requireThread(ctx, "user-1")
	if err != nil {
		t.Fatalf("requireThread(second restore call) error: %v", err)
	}
	if calls["thread/resume"] != 1 {
		t.Fatalf("thread/resume calls after second restore = %d, want 1", calls["thread/resume"])
	}
}

func TestACPAgentCodexThreadStartIncludesEffort(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
		Model:   "gpt-5.4",
		Effort:  "high",
	})
	a.SetCodexServiceTier("fast")

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/start" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p, ok := params.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected thread/start params type %T", params)
		}
		if p["model"] != "gpt-5.4" || p["effort"] != "high" || p["serviceTier"] != CodexServiceTierFast {
			return nil, fmt.Errorf("model/effort/serviceTier params=%#v", p)
		}
		return json.RawMessage(`{"thread":{"id":"thread-1"},"serviceTier":"priority"}`), nil
	}

	if _, err := a.createThread(ctx, "user-1"); err != nil {
		t.Fatalf("createThread error: %v", err)
	}
	config, err := a.CodexThreadConfig(ctx, "user-1", "thread-1")
	if err != nil || config.Model != "gpt-5.4" || config.Effort != "high" ||
		!config.ServiceTierKnown || config.ServiceTier != CodexServiceTierFast {
		t.Fatalf("CodexThreadConfig=(%#v,%v), want thread/start defaults", config, err)
	}
}

func TestACPAgentCodexThreadStartExplicitStandardUsesNull(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}, Cwd: t.TempDir(),
	})
	a.SetCodexServiceTier(CodexServiceTierStandard)
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/start" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p := params.(map[string]interface{})
		value, exists := p["serviceTier"]
		if !exists || value != nil {
			return nil, fmt.Errorf("serviceTier=%#v exists=%v, want explicit null", value, exists)
		}
		return json.RawMessage(`{"thread":{"id":"thread-standard"},"serviceTier":null}`), nil
	}

	if _, err := a.createThread(context.Background(), "conversation-standard"); err != nil {
		t.Fatalf("createThread error: %v", err)
	}
	config, err := a.CodexThreadConfig(context.Background(), "", "thread-standard")
	if err != nil || !config.ServiceTierKnown || config.ServiceTier != "" {
		t.Fatalf("CodexThreadConfig=(%#v,%v), want explicit standard", config, err)
	}
}

func TestACPAgentCodexTurnStartPreservesThreadSettings(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
		Model:   "gpt-5.4",
		Effort:  "high",
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}
			if p.Model != "" || p.Effort != "" {
				return nil, fmt.Errorf("model=%q effort=%q", p.Model, p.Effort)
			}
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			ch <- &codexTurnEvent{Delta: "ok"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}
	if _, err := a.createThread(ctx, "user-1"); err != nil {
		t.Fatalf("createThread error: %v", err)
	}

	if _, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello"}); err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
}

func TestACPAgentUpdatesCurrentCodexThreadConfig(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/settings/update" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p, ok := params.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected params type %T", params)
		}
		if p["threadId"] != "thread-current" || p["model"] != "gpt-5.6-sol" ||
			p["effort"] != "max" || p["serviceTier"] != CodexServiceTierFast {
			return nil, fmt.Errorf("thread/settings/update params=%#v", p)
		}
		return json.RawMessage(`{}`), nil
	}

	serviceTier := CodexServiceTierFast
	err := a.SetCodexThreadConfig(context.Background(), CodexThreadConfigUpdate{
		ConversationID: "conversation-1",
		ThreadID:       "thread-current",
		Model:          "gpt-5.6-sol",
		Effort:         "max",
		ServiceTier:    &serviceTier,
	})
	if err != nil {
		t.Fatalf("SetCodexThreadConfig error: %v", err)
	}
	config, err := a.CodexThreadConfig(context.Background(), "conversation-1", "thread-current")
	if err != nil || config.Model != "gpt-5.6-sol" || config.Effort != "max" ||
		!config.ServiceTierKnown || config.ServiceTier != CodexServiceTierFast {
		t.Fatalf("CodexThreadConfig=(%#v,%v), want updated settings", config, err)
	}
}

func TestACPAgentDisablesCurrentCodexFastModeWithExplicitNull(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.setCodexThreadConfigAt("thread-current", CodexThreadConfig{
		Model: "gpt-current", ServiceTier: CodexServiceTierFast, ServiceTierKnown: true,
	}, 1)
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/settings/update" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p := params.(map[string]interface{})
		value, exists := p["serviceTier"]
		if !exists || value != nil {
			return nil, fmt.Errorf("serviceTier=%#v exists=%v, want explicit null", value, exists)
		}
		return json.RawMessage(`{}`), nil
	}

	serviceTier := CodexServiceTierStandard
	if err := a.SetCodexThreadConfig(context.Background(), CodexThreadConfigUpdate{
		ThreadID: "thread-current", ServiceTier: &serviceTier,
	}); err != nil {
		t.Fatalf("SetCodexThreadConfig error: %v", err)
	}
	config, err := a.CodexThreadConfig(context.Background(), "", "thread-current")
	if err != nil || !config.ServiceTierKnown || config.ServiceTier != CodexServiceTierStandard {
		t.Fatalf("CodexThreadConfig=(%#v,%v), want explicit standard", config, err)
	}
}

func TestACPAgentCodexThreadConfigPropagatesUpdateFailure(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/settings/update" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		return nil, fmt.Errorf("rejected")
	}

	err := a.SetCodexThreadConfig(context.Background(), CodexThreadConfigUpdate{
		ThreadID: "thread-current",
		Model:    "gpt-5.6-sol",
	})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("SetCodexThreadConfig error=%v, want rejected detail", err)
	}
}

func TestACPAgentCodexLifecycleConfigPreservesExplicitDefaultEffort(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.cacheCodexThreadConfigFromLifecycleResult(
		json.RawMessage(`{"model":"gpt-current","reasoningEffort":null}`),
		"thread-current",
		CodexThreadConfig{Model: "gpt-fallback", Effort: "high"},
		1,
	)
	config, err := a.CodexThreadConfig(context.Background(), "conversation-1", "thread-current")
	if err != nil || config.Model != "gpt-current" || config.Effort != "" {
		t.Fatalf("CodexThreadConfig=(%#v,%v), explicit null effort must not use fallback", config, err)
	}
}

func TestACPAgentCodexLifecycleConfigPreservesExplicitStandardServiceTier(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.cacheCodexThreadConfigFromLifecycleResult(
		json.RawMessage(`{"model":"gpt-current","serviceTier":null}`),
		"thread-current",
		CodexThreadConfig{ServiceTier: CodexServiceTierFast, ServiceTierKnown: true},
		1,
	)
	config, err := a.CodexThreadConfig(context.Background(), "", "thread-current")
	if err != nil || !config.ServiceTierKnown || config.ServiceTier != "" {
		t.Fatalf("CodexThreadConfig=(%#v,%v), explicit null must remain standard", config, err)
	}
}

func TestACPAgentConversationCwdOverridesGlobalCwdForCodexThreadAndTurn(t *testing.T) {
	ctx := context.Background()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspace A: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspace B: %v", err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     workspaceB,
		Model:   "gpt-5.4",
		Effort:  "high",
	})
	a.SetConversationCwd("conversation-a", workspaceA)
	a.SetCwd(workspaceB)

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			p, ok := params.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("unexpected thread/start params type %T", params)
			}
			if p["cwd"] != workspaceA {
				return nil, fmt.Errorf("thread/start cwd=%q, want %q", p["cwd"], workspaceA)
			}
			return json.RawMessage(`{"thread":{"id":"thread-a"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}
			if p.Cwd != workspaceA {
				return nil, fmt.Errorf("turn/start cwd=%q, want %q", p.Cwd, workspaceA)
			}
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			ch <- &codexTurnEvent{Delta: "ok"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}
	if _, err := a.createThread(ctx, "conversation-a"); err != nil {
		t.Fatalf("createThread error: %v", err)
	}

	if _, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "conversation-a", message: "hello"}); err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
}

func TestACPAgentConversationCwdOverridesGlobalCwdForCodexResume(t *testing.T) {
	ctx := context.Background()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspace A: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspace B: %v", err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     workspaceB,
	})
	a.SetConversationCwd("conversation-a", workspaceA)
	a.SetCwd(workspaceB)

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/resume" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p, ok := params.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected thread/resume params type %T", params)
		}
		if p["cwd"] != workspaceA {
			return nil, fmt.Errorf("thread/resume cwd=%q, want %q", p["cwd"], workspaceA)
		}
		if _, ok := p["model"]; ok {
			return nil, fmt.Errorf("thread/resume must preserve thread model: %#v", p)
		}
		if _, ok := p["effort"]; ok {
			return nil, fmt.Errorf("thread/resume must preserve thread effort: %#v", p)
		}
		return json.RawMessage(`{"model":"gpt-thread","reasoningEffort":"max","thread":{"id":"thread-a"}}`), nil
	}

	if err := a.UseCodexThread(ctx, "conversation-a", "thread-a"); err != nil {
		t.Fatalf("UseCodexThread error: %v", err)
	}
	config, err := a.CodexThreadConfig(ctx, "conversation-a", "thread-a")
	if err != nil || config.Model != "gpt-thread" || config.Effort != "max" {
		t.Fatalf("CodexThreadConfig=(%#v,%v), want thread/resume settings", config, err)
	}
}

func TestACPAgentCodexThreadControls(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       t.TempDir(),
		StateFile: stateFile,
	})
	resumed := ""
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/resume" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p := params.(map[string]interface{})
		resumed = p["threadId"].(string)
		return json.RawMessage(`{"thread":{"id":"thread-2"}}`), nil
	}

	if err := a.UseCodexThread(ctx, "conversation-1", "thread-2"); err != nil {
		t.Fatalf("UseCodexThread error: %v", err)
	}
	if resumed != "thread-2" {
		t.Fatalf("resumed thread=%q, want thread-2", resumed)
	}
	threadID, ok := a.CurrentCodexThread("conversation-1")
	if !ok || threadID != "thread-2" {
		t.Fatalf("CurrentCodexThread=(%q,%v), want thread-2 true", threadID, ok)
	}

	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["conversation-1"]; got != "thread-2" {
		t.Fatalf("persisted thread=%q, want thread-2", got)
	}

	a.ClearCodexThread("conversation-1")
	if _, ok := a.CurrentCodexThread("conversation-1"); ok {
		t.Fatal("ClearCodexThread should remove current thread")
	}
	persisted = readACPStateFile(t, stateFile)
	if _, ok := persisted.Threads["conversation-1"]; ok {
		t.Fatalf("cleared thread should not persist, got %q", persisted.Threads["conversation-1"])
	}
}

func TestACPAgentReadsActiveCodexThreadState(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p := params.(map[string]interface{})
		if p["threadId"] != "thread-active" || p["includeTurns"] != true {
			return nil, fmt.Errorf("thread/read params=%#v", p)
		}
		return json.RawMessage(`{
			"thread": {
				"id": "thread-active",
				"status": {"type": "active", "activeFlags": ["waitingOnUserInput"]},
				"turns": [
					{"id": "turn-old", "status": "completed", "items": [{"id": "agent-old", "type": "agentMessage", "text": "旧回复"}]},
					{"id": "turn-active", "status": "inProgress", "items": [{"id": "user-1", "type": "userMessage", "content": [{"type": "text", "text": "本地 App 发起的任务"}]}]}
				]
			}
		}`), nil
	}

	state, err := a.ReadCodexThreadState(ctx, "conversation-1", "thread-active")
	if err != nil {
		t.Fatalf("ReadCodexThreadState error: %v", err)
	}
	if !state.Active || state.ActiveTurnID != "turn-active" {
		t.Fatalf("active state=%#v, want active turn-active", state)
	}
	if !state.WaitingOnUserInput {
		t.Fatalf("waiting flag=false, state=%#v", state)
	}
	if state.Preview != "本地 App 发起的任务" {
		t.Fatalf("preview=%q", state.Preview)
	}
}

func TestACPAgentSteersActiveCodexTurn(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "turn/steer" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p := params.(map[string]interface{})
		if p["threadId"] != "thread-active" || p["expectedTurnId"] != "turn-active" {
			return nil, fmt.Errorf("turn/steer params=%#v", p)
		}
		input := p["input"].([]codexUserInput)
		if len(input) != 1 || input[0].Text != "补充要求" {
			return nil, fmt.Errorf("turn/steer input=%#v", input)
		}
		return json.RawMessage(`{"ok":true}`), nil
	}

	if err := a.SteerCodexThread(ctx, "conversation-1", "thread-active", "turn-active", "补充要求"); err != nil {
		t.Fatalf("SteerCodexThread error: %v", err)
	}
}

func TestACPAgentInterruptsActiveCodexTurn(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "turn/interrupt" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p := params.(map[string]interface{})
		if p["threadId"] != "thread-active" || p["turnId"] != "turn-active" {
			return nil, fmt.Errorf("turn/interrupt params=%#v", p)
		}
		return json.RawMessage(`{"ok":true}`), nil
	}

	if err := a.InterruptCodexThread(ctx, "conversation-1", "thread-active", "turn-active"); err != nil {
		t.Fatalf("InterruptCodexThread error: %v", err)
	}
}

func TestACPAgentWatchesAttachedCodexThread(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			a.notifyMu.Lock()
			ch := a.turnCh["thread-active"]
			a.notifyMu.Unlock()
			if ch != nil {
				ch <- &codexTurnEvent{ItemID: "agent-1", Delta: "接管后的最终回复"}
				ch <- &codexTurnEvent{Kind: "completed"}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	reply, err := a.WatchCodexThread(ctx, "conversation-1", "thread-active", nil)
	if err != nil {
		t.Fatalf("WatchCodexThread error: %v", err)
	}
	if reply != "接管后的最终回复" {
		t.Fatalf("reply=%q", reply)
	}
	<-done
}

func TestACPAgentWatchReturnsCompletedStateAfterRegistration(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return json.RawMessage(`{"thread":{"id":"thread-finished","status":{"type":"idle"},"turns":[{"id":"turn-1","status":"completed","items":[{"id":"msg-1","type":"agentMessage","text":"任务已经完成"}]}]}}`), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	reply, err := a.WatchCodexThread(ctx, "conversation-1", "thread-finished", nil)
	if err != nil {
		t.Fatalf("WatchCodexThread error: %v", err)
	}
	if reply != "任务已经完成" {
		t.Fatalf("reply=%q", reply)
	}
}
