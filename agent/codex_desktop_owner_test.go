package agent

import (
	"context"
	"errors"
	"path/filepath"
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
}

func (f *codexDesktopOwnerProbeFake) Discover(context.Context, CodexThreadRef) (bool, error) {
	f.discoverCalls++
	return f.discoverResult, f.discoverErr
}

func (f *codexDesktopOwnerProbeFake) LoadHistory(context.Context, CodexThreadRef) error {
	f.loadCalls++
	return f.loadErr
}

func (f *codexDesktopOwnerProbeFake) Presence() (bool, bool) {
	return f.socketExists, f.processExists
}

func TestCodexDesktopSnapshotClaimsUnownedThread(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
	state := CodexThreadState{ThreadID: "thread-1", Model: "gpt-test"}
	binding := registry.observeDesktopSnapshot("thread-1", 7, state)
	if binding.Owner != CodexOwnerDesktopLive || !binding.Connected || binding.OwnerRevision != 7 {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestCodexDesktopSnapshotDoesNotStealActiveWeClawThread(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
	registry.claimWeClawThread("thread-1", CodexThreadState{ThreadID: "thread-1", Active: true})
	binding := registry.observeDesktopSnapshot("thread-1", 2, CodexThreadState{ThreadID: "thread-1"})
	if binding.Owner != CodexOwnerWeClawRuntime {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestCodexRuntimeOwnerDisconnectDoesNotReleaseThread(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
	registry.observeDesktopSnapshot("thread-1", 1, CodexThreadState{ThreadID: "thread-1"})
	registry.markDesktopDisconnected()
	binding, ok := registry.threadBinding("thread-1")
	if !ok || binding.Owner != CodexOwnerDesktopDisconnected || binding.ReleaseConfirmed {
		t.Fatalf("binding = %#v, ok = %v", binding, ok)
	}
}

func TestCodexRuntimeOwnerDiscoveryTimeoutRemainsUnknown(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{
		discoverErr: context.DeadlineExceeded, loadErr: context.DeadlineExceeded,
		socketExists: true, processExists: true,
	}
	registry := newCodexRuntimeOwnerRegistry(probe)
	binding, err := registry.bind(context.Background(), CodexThreadRef{ConversationID: "c-1", ThreadID: "thread-1"})
	if !errors.Is(err, ErrCodexDesktopOwnershipUnknown) || binding.Owner != CodexOwnerUnknown {
		t.Fatalf("binding = %#v, error = %v", binding, err)
	}
	if probe.discoverCalls != 1 || probe.loadCalls != 1 {
		t.Fatalf("calls = discover:%d load:%d", probe.discoverCalls, probe.loadCalls)
	}
}

func TestCodexRuntimeOwnerNoClientFoundConfirmsRelease(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient, socketExists: true, processExists: true}
	registry := newCodexRuntimeOwnerRegistry(probe)
	binding, err := registry.bind(context.Background(), CodexThreadRef{ConversationID: "c-1", ThreadID: "thread-1"})
	if err != nil || binding.Owner != CodexOwnerPersistedOnly || !binding.ReleaseConfirmed {
		t.Fatalf("binding = %#v, error = %v", binding, err)
	}
}

func TestCodexRuntimeOwnerMissingSocketAndProcessConfirmsRelease(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{
		discoverErr: ErrCodexDesktopUnavailable, loadErr: ErrCodexDesktopUnavailable,
	}
	registry := newCodexRuntimeOwnerRegistry(probe)
	binding, err := registry.bind(context.Background(), CodexThreadRef{ConversationID: "c-1", ThreadID: "thread-1"})
	if err != nil || binding.Owner != CodexOwnerPersistedOnly || !binding.ReleaseConfirmed {
		t.Fatalf("binding = %#v, error = %v", binding, err)
	}
}

func TestACPStatePersistsDesktopLiveAsDisconnected(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	probe := &codexDesktopOwnerProbeFake{}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: stateFile,
	}, acpAgentOptions{desktopProbe: probe})
	a.codexOwners.observeDesktopSnapshot("thread-1", 8, CodexThreadState{ThreadID: "thread-1"})
	if _, err := a.codexOwners.bind(context.Background(), CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	}); err != nil {
		t.Fatalf("bind() error = %v", err)
	}
	a.persistState()

	persisted := readACPStateFile(t, stateFile)
	binding := persisted.LiveBindings["conversation-1"]
	if persisted.Version != 2 || binding.Owner != CodexOwnerDesktopDisconnected {
		t.Fatalf("persisted = %#v", persisted)
	}

	restored := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: stateFile,
	}, acpAgentOptions{desktopProbe: probe})
	got, ok := restored.CurrentCodexThreadBinding("conversation-1")
	if !ok || got.Owner != CodexOwnerDesktopDisconnected || len(restored.threads) != 0 {
		t.Fatalf("restored binding = %#v, ok = %v, threads = %#v", got, ok, restored.threads)
	}
}

func TestACPStatePersistsWeClawRuntimeAsRecoverable(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: stateFile,
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	binding := a.codexOwners.claimWeClawThread(
		"thread-1", CodexThreadState{ThreadID: "thread-1"},
	)
	a.codexOwners.bindConversation(CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	}, binding)
	a.persistState()

	persisted := readACPStateFile(t, stateFile)
	got := persisted.LiveBindings["conversation-1"]
	if got.Owner != CodexOwnerPersistedOnly || !got.ReleaseConfirmed {
		t.Fatalf("persisted binding = %#v", got)
	}

	restored := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: stateFile,
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	got, ok := restored.CurrentCodexThreadBinding("conversation-1")
	if !ok || got.Ref.ThreadID != "thread-1" || got.Owner != CodexOwnerPersistedOnly || !got.ReleaseConfirmed {
		t.Fatalf("restored binding = %#v, ok = %v", got, ok)
	}
}

func TestCodexRuntimeOwnerRestoredBindingMustProbeAgain(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{
		loadErr: ErrCodexDesktopNoClient, socketExists: true, processExists: true,
	}
	registry := newCodexRuntimeOwnerRegistry(probe)
	registry.restoreBindings(map[string]CodexThreadBinding{
		"conversation-1": {
			Ref:   CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
			Owner: CodexOwnerDesktopDisconnected,
		},
	})
	binding, err := registry.bind(context.Background(), CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	})
	if err != nil || binding.Owner != CodexOwnerPersistedOnly || probe.loadCalls != 1 {
		t.Fatalf("binding = %#v, error = %v, loadCalls = %d", binding, err, probe.loadCalls)
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
