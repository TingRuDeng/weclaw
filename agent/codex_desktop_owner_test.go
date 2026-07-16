package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type codexDesktopOwnerProbeFake struct {
	discoverResult bool
	discoverErr    error
	loadErr        error
	socketExists   bool
	processExists  bool
	discoverCalls  int
	loadCalls      int
	loadHook       func(CodexThreadRef)
}

func (f *codexDesktopOwnerProbeFake) Discover(context.Context, CodexThreadRef) (bool, error) {
	f.discoverCalls++
	return f.discoverResult, f.discoverErr
}

func (f *codexDesktopOwnerProbeFake) LoadHistory(_ context.Context, ref CodexThreadRef) error {
	f.loadCalls++
	if f.loadHook != nil {
		f.loadHook(ref)
	}
	return f.loadErr
}

func (f *codexDesktopOwnerProbeFake) Presence() (bool, bool) {
	return f.socketExists, f.processExists
}

func TestCodexDesktopSnapshotClaimsUnownedThread(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
	state := CodexThreadState{ThreadID: "thread-1", Model: "gpt-test"}
	binding := registry.observeDesktopSnapshot("thread-1", 7, state)
	if binding.Runtime != CodexRuntimeDesktop || binding.RuntimeGeneration != 1 || binding.State.Model != "gpt-test" {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestCodexDesktopSnapshotReconcilesIdleWeClawRuntime(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
	registry.claimWeClawThread("thread-1", CodexThreadState{ThreadID: "thread-1"})
	binding := registry.observeDesktopSnapshot("thread-1", 2, CodexThreadState{ThreadID: "thread-1"})
	if binding.Runtime != CodexRuntimeDesktop {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestCodexRuntimeOwnerDisconnectDoesNotReleaseThread(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
	registry.observeDesktopSnapshot("thread-1", 1, CodexThreadState{ThreadID: "thread-1"})
	registry.markDesktopDisconnected()
	binding, ok := registry.threadBinding("thread-1")
	if !ok || binding.Runtime != CodexRuntimeUnknown {
		t.Fatalf("binding = %#v, ok = %v", binding, ok)
	}
}

func TestACPStateV3DoesNotPersistActualRuntime(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: stateFile,
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	a.codexOwners.claimWeClawConversation(CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	}, CodexThreadState{ThreadID: "thread-1"})
	a.persistState()

	persisted := readACPStateFile(t, stateFile)
	if persisted.Version != 3 || len(persisted.LiveBindings) != 0 {
		t.Fatalf("persisted=%#v，want v3 without live bindings", persisted)
	}
}

func TestACPStateV2RuntimeRestoresAsUnknown(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	writeACPStateFile(t, stateFile, acpPersistedState{
		Version: 2, Protocol: protocolCodexAppServer,
		LiveBindings: map[string]CodexThreadBinding{
			"conversation-1": {
				Ref: CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
			},
		},
	})

	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: stateFile,
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	binding, ok := a.runtimeBindingForThread("conversation-1", "thread-1")
	if !ok || binding.Runtime != CodexRuntimeUnknown {
		t.Fatalf("binding=%#v ok=%v，want unknown", binding, ok)
	}
}

func TestCodexRuntimeOwnerRestoredBindingMustProbeAgain(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{
		loadErr: ErrCodexDesktopNoClient, socketExists: true, processExists: true,
	}
	registry := newCodexRuntimeOwnerRegistry(probe)
	registry.restoreBindings(map[string]CodexThreadBinding{
		"conversation-1": {
			Ref: CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
		},
	})
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	a.codexOwners = registry
	binding, err := a.InspectCodexRuntime(context.Background(), CodexRuntimeRequest{
		Ref:    CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
		Intent: CodexControlIntent{Owner: CodexControlDesktop, Revision: 1},
	})
	if err != nil || binding.Runtime != CodexRuntimeUnknown || probe.loadCalls != 1 {
		t.Fatalf("binding = %#v, error = %v, loadCalls = %d", binding, err, probe.loadCalls)
	}
}

func TestInspectCodexRuntimeLoadsHistoryWithoutClientDiscovery(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	req := CodexRuntimeRequest{
		Ref:    CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
		Intent: CodexControlIntent{Owner: CodexControlDesktop, Revision: 1},
	}

	for range 2 {
		binding, err := a.InspectCodexRuntime(context.Background(), req)
		if err != nil || binding.Runtime != CodexRuntimeUnknown {
			t.Fatalf("binding=%#v error=%v", binding, err)
		}
	}
	if probe.loadCalls != 2 {
		t.Fatalf("loadCalls=%d，want 2", probe.loadCalls)
	}
	if probe.discoverCalls != 0 {
		t.Fatalf("Inspect 不应向 Desktop Router 发送 client discovery: calls=%d", probe.discoverCalls)
	}
}

func TestCodexProbeErrorPreservesFailureCause(t *testing.T) {
	loadErr := errors.New("load failed")
	tests := []struct {
		name     string
		load     error
		contains string
	}{
		{name: "history", load: loadErr, contains: "load failed"},
		{name: "unknown", contains: ErrCodexDesktopOwnershipUnknown.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := codexProbeError(test.load)
			if !errors.Is(err, ErrCodexDesktopOwnershipUnknown) || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDesktopReleaseConfirmedRequiresNoReachableDesktop(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{socketExists: true, processExists: true}
	if !desktopReleaseConfirmed(probe, ErrCodexDesktopNoClient) {
		t.Fatal("明确无人处理应视为已释放")
	}
	if desktopReleaseConfirmed(probe, nil) {
		t.Fatal("Desktop 仍存在时不应视为已释放")
	}
	probe.socketExists = false
	probe.processExists = false
	if !desktopReleaseConfirmed(probe, nil) {
		t.Fatal("Desktop 进程和 socket 均消失时应视为已释放")
	}
}

func TestInspectCodexRuntimeRejectsUnsupportedProtocol(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "claude-agent-acp", StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{})
	_, err := a.InspectCodexRuntime(context.Background(), CodexRuntimeRequest{
		Ref:    CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
		Intent: CodexControlIntent{Owner: CodexControlDesktop},
	})
	if !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("error=%v，want runtime unavailable", err)
	}
}

func TestProbeCodexRuntimeRequiresDesktopProbe(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	a.desktopProbe = nil
	runtime, _, err := a.probeCodexRuntime(context.Background(), remoteCodexRuntimeRequest("thread-1", "route-1", 1), codexRuntimeProbeOptions{})
	if runtime != CodexRuntimeUnknown || !errors.Is(err, ErrCodexDesktopOwnershipUnknown) {
		t.Fatalf("runtime=%q error=%v", runtime, err)
	}
}

func TestHandoffCodexRuntimeRemoteUsesLiveDesktop(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{socketExists: true, processExists: true}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	probe.loadHook = func(ref CodexThreadRef) {
		a.codexOwners.observeDesktopSnapshot(ref.ThreadID, 7, CodexThreadState{ThreadID: ref.ThreadID})
	}
	req := remoteCodexRuntimeRequest("thread-1", "route-1", 1)

	binding, err := a.HandoffCodexRuntime(context.Background(), req)

	if err != nil || binding.Runtime != CodexRuntimeDesktop || binding.Control.Revision != 1 {
		t.Fatalf("binding=%#v error=%v", binding, err)
	}
}

func TestHandoffCodexRuntimeRemoteReusesKnownWeClawWithoutDesktopProbe(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{socketExists: true, processExists: true}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	ref := CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"}
	a.codexOwners.claimWeClawConversation(ref, CodexThreadState{ThreadID: ref.ThreadID})
	req := remoteCodexRuntimeRequest(ref.ThreadID, "route-1", 1)
	req.Ref = ref

	binding, err := a.HandoffCodexRuntime(context.Background(), req)

	if err != nil || binding.Runtime != CodexRuntimeWeClaw || binding.Control != req.Intent {
		t.Fatalf("binding=%#v error=%v", binding, err)
	}
	if probe.loadCalls != 0 || probe.discoverCalls != 0 {
		t.Fatalf("known WeClaw runtime should not probe Desktop: load=%d discover=%d", probe.loadCalls, probe.discoverCalls)
	}
}

func TestHandoffCodexRuntimeRemoteRefreshesReleasedThread(t *testing.T) {
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	content := []byte("{\"type\":\"event_msg\"}\n")
	if err := os.WriteFile(rollout, content, 0o600); err != nil {
		t.Fatal(err)
	}
	probe := &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	a.restartCodexAppServerCall = func(context.Context) error { return nil }
	a.rpcCall = codexHandoffRPCFake(t, "thread-1", "turn-1")
	req := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	req.Checkpoint = CodexRolloutCheckpoint{
		Path: rollout, Offset: int64(len(content)), Size: int64(len(content)), TurnID: "turn-1",
	}

	binding, err := a.HandoffCodexRuntime(context.Background(), req)

	if err != nil || binding.Runtime != CodexRuntimeWeClaw || a.threads[req.Ref.ConversationID] != "thread-1" {
		t.Fatalf("binding=%#v threads=%#v error=%v", binding, a.threads, err)
	}
}

func TestHandoffCodexRuntimeRejectsActiveWriter(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	remote := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(remote, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := a.codexOwners.beginTurn(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()
	desktop := CodexRuntimeRequest{
		Ref: remote.Ref, Intent: CodexControlIntent{Owner: CodexControlDesktop, Revision: 2},
	}

	_, err = a.HandoffCodexRuntime(context.Background(), desktop)

	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("error=%v，want writer busy", err)
	}
}

func codexHandoffRPCFake(t *testing.T, threadID string, turnID string) func(context.Context, string, interface{}) (json.RawMessage, error) {
	t.Helper()
	return func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/resume":
			return json.RawMessage(`{"thread":{"id":"` + threadID + `"}}`), nil
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"` + threadID + `","status":{"type":"idle"},"turns":[{"id":"` + turnID + `","status":"completed"}]}}`), nil
		default:
			t.Fatalf("unexpected rpc method %s", method)
			return nil, nil
		}
	}
}

func TestCodexAppServerConstructorCreatesDesktopRuntimeLazily(t *testing.T) {
	codex := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	if codex.desktopRuntime == nil || codex.desktopRuntime.client != nil {
		t.Fatalf("desktop runtime = %#v", codex.desktopRuntime)
	}
	legacy := NewACPAgent(ACPAgentConfig{Command: "claude-agent-acp"})
	if legacy.desktopRuntime != nil || legacy.codexOwners != nil {
		t.Fatalf("legacy desktop runtime = %#v", legacy.desktopRuntime)
	}
}
