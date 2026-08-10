package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverCodexThreadHandoffRestartsIdleOfficialDaemon(t *testing.T) {
	a := newCodexThreadHandoffTestAgent(t, "idle")
	var actions []string
	a.stopManagedHostCall = func(context.Context, string) error {
		actions = append(actions, "stop")
		return nil
	}
	a.startManagedHostCall = func(context.Context, string) error {
		actions = append(actions, "start")
		return nil
	}
	a.mu.Lock()
	a.threads["conversation-old"] = "thread-old"
	a.mu.Unlock()
	req := CodexRuntimeRequest{
		Ref: CodexThreadRef{ConversationID: "conversation-old", ThreadID: "thread-old"},
		Intent: CodexControlIntent{
			Owner: CodexControlRemote, RouteKey: "route-old",
			ConversationID: "conversation-old", Revision: 1,
		},
	}
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-old"}); err != nil {
		t.Fatal(err)
	}

	attempted, err := a.RecoverCodexThreadHandoff(context.Background(), "thread-old")
	if err != nil || !attempted {
		t.Fatalf("RecoverCodexThreadHandoff() attempted=%v err=%v", attempted, err)
	}
	if len(actions) != 2 || actions[0] != "stop" || actions[1] != "start" {
		t.Fatalf("actions=%v, want stop/start", actions)
	}
	a.mu.Lock()
	resume := a.resumeOnFirstUse["conversation-old"]
	a.mu.Unlock()
	if !resume || a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
		t.Fatalf("resume=%v runtime=%q", resume, a.codexRuntimeModeSnapshot())
	}
	if binding, ok := a.codexOwners.threadBinding("thread-old"); !ok || binding.Runtime != CodexRuntimeUnknown {
		t.Fatalf("binding=%#v ok=%v, restarted Host snapshot must be unknown", binding, ok)
	}
}

func TestRecoverCodexThreadHandoffKeepsHostWhenAnyThreadIsActive(t *testing.T) {
	a := newCodexThreadHandoffTestAgent(t, "active")
	stopped := false
	a.stopManagedHostCall = func(context.Context, string) error {
		stopped = true
		return nil
	}

	attempted, err := a.RecoverCodexThreadHandoff(context.Background(), "thread-old")
	if !attempted || !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("RecoverCodexThreadHandoff() attempted=%v err=%v", attempted, err)
	}
	if stopped {
		t.Fatal("active thread must prevent Host restart")
	}
}

func TestRecoverCodexThreadHandoffSkipsWhenDesktopIsAbsent(t *testing.T) {
	a := newCodexThreadHandoffTestAgent(t, "idle")
	a.desktopRuntime.presence = func() (bool, bool) { return false, false }
	stopped := false
	a.stopManagedHostCall = func(context.Context, string) error {
		stopped = true
		return nil
	}

	attempted, err := a.RecoverCodexThreadHandoff(context.Background(), "thread-old")
	if err != nil || attempted || stopped {
		t.Fatalf("attempted=%v stopped=%v err=%v", attempted, stopped, err)
	}
}

func TestRecoverCodexThreadHandoffDoesNotRestartForUnreachableDesktop(t *testing.T) {
	a := newCodexThreadHandoffTestAgent(t, "idle")
	a.desktopRuntime.presence = func() (bool, bool) { return false, true }
	stopped := false
	a.stopManagedHostCall = func(context.Context, string) error {
		stopped = true
		return nil
	}

	attempted, err := a.RecoverCodexThreadHandoff(context.Background(), "thread-old")
	if !attempted || !errors.Is(err, ErrCodexDesktopUnavailable) || stopped {
		t.Fatalf("attempted=%v stopped=%v err=%v", attempted, stopped, err)
	}
}

func TestRecoverCodexThreadHandoffFailsClosedWhenDaemonRestartFails(t *testing.T) {
	a := newCodexThreadHandoffTestAgent(t, "idle")
	a.stopManagedHostCall = func(context.Context, string) error { return nil }
	a.startManagedHostCall = func(context.Context, string) error { return errors.New("start failed") }

	attempted, err := a.RecoverCodexThreadHandoff(context.Background(), "thread-old")
	if !attempted || err == nil {
		t.Fatalf("attempted=%v err=%v", attempted, err)
	}
	if state := a.ensureCodexAppServerGate().stateSnapshot(); state != codexAppServerFailed {
		t.Fatalf("gate state=%q, want failed", state)
	}
}

func TestRecoverCodexThreadHandoffRestartsAfterCallerCancellationOnceStopped(t *testing.T) {
	a := newCodexThreadHandoffTestAgent(t, "idle")
	ctx, cancel := context.WithCancel(context.Background())
	a.stopManagedHostCall = func(context.Context, string) error {
		cancel()
		return nil
	}
	restartContextErr := errors.New("restart not called")
	a.startManagedHostCall = func(ctx context.Context, _ string) error {
		restartContextErr = ctx.Err()
		return nil
	}

	attempted, err := a.RecoverCodexThreadHandoff(ctx, "thread-old")
	if !attempted || err != nil || restartContextErr != nil {
		t.Fatalf("attempted=%v err=%v restart context err=%v", attempted, err, restartContextErr)
	}
}

func newCodexThreadHandoffTestAgent(t *testing.T, threadStatus string) *ACPAgent {
	t.Helper()
	home := newShortCodexHome(t)
	if err := os.MkdirAll(filepath.Dir(codexDaemonSocketPath(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := newCodexDesktopRuntime()
	runtime.presence = func() (bool, bool) { return true, true }
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeDaemon,
		CodexDesktopBridge: true,
		Env:                map[string]string{"CODEX_HOME": home}, StateFile: filepath.Join(home, "state.json"),
	}, acpAgentOptions{desktopProbe: runtime})
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/list" {
			t.Fatalf("unexpected RPC method %q", method)
		}
		return json.RawMessage(`{"data":[{"id":"thread-1","status":{"type":"` + threadStatus + `"}}],"nextCursor":null}`), nil
	}
	return a
}
