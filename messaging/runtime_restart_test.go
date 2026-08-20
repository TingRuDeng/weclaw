package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeCodexRestartAgent struct {
	fakeAgent
	prepareResult agent.CodexRestartSnapshot
	prepareErr    error
	verifyResult  agent.CodexRestartSnapshot
	verifyErr     error
	prepareCalls  int
	cancelCalls   int
	verifyCalls   int
}

type optionsFakeCodexRestartAgent struct {
	*fakeCodexRestartAgent
	stopConflicts bool
}

func (f *optionsFakeCodexRestartAgent) PrepareCodexRestartWithOptions(
	ctx context.Context,
	persist func(agent.CodexRestartSnapshot) error,
	opts agent.CodexRestartOptions,
) (agent.CodexRestartSnapshot, error) {
	f.stopConflicts = opts.StopConflictingCodexHosts
	return f.PrepareCodexRestart(ctx, persist)
}

func newFakeCodexRestartAgent() *fakeCodexRestartAgent {
	return &fakeCodexRestartAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "codex", Type: "acp", Command: "codex",
	}}}
}

func (f *fakeCodexRestartAgent) PrepareCodexRestart(
	_ context.Context,
	persist func(agent.CodexRestartSnapshot) error,
) (agent.CodexRestartSnapshot, error) {
	f.prepareCalls++
	if (f.prepareErr == nil || errors.Is(f.prepareErr, agent.ErrCodexRestartUnsafe)) && persist != nil {
		if err := persist(f.prepareResult); err != nil {
			return agent.CodexRestartSnapshot{}, err
		}
	}
	return f.prepareResult, f.prepareErr
}

func (f *fakeCodexRestartAgent) CancelCodexRestart(context.Context) error {
	f.cancelCalls++
	return nil
}

func (f *fakeCodexRestartAgent) VerifyCodexRestart(_ context.Context, _ agent.CodexRestartSnapshot) (agent.CodexRestartSnapshot, error) {
	f.verifyCalls++
	return f.verifyResult, f.verifyErr
}

func TestPrepareRuntimeRestartRejectsActiveControlledCLIAtServiceBoundary(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	cliLease, err := agent.AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatal(err)
	}
	defer cliLease.Close()
	h := NewHandler(nil, nil)
	h.runtimeRestartStateFile = filepath.Join(t.TempDir(), "runtime-restart.json")
	_, err = h.PrepareRuntimeRestart(context.Background(), false)
	if !errors.Is(err, ErrRuntimeRestartBlocked) || !errors.Is(err, agent.ErrCodexCLIFrontendActive) {
		t.Fatalf("PrepareRuntimeRestart error=%v", err)
	}
	if h.IsDraining() {
		t.Fatal("frontend lease rejection changed message admission")
	}
}

func TestPrepareRuntimeRestartWithOptionsPropagatesConflictAuthorization(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	h := NewHandler(nil, nil)
	h.runtimeRestartStateFile = filepath.Join(t.TempDir(), "runtime-restart.json")
	controller := &optionsFakeCodexRestartAgent{fakeCodexRestartAgent: newFakeCodexRestartAgent()}
	controller.prepareResult = agent.CodexRestartSnapshot{HostGeneration: 3, HostStopped: true}
	h.SetDefaultAgent("codex", controller)
	if _, err := h.PrepareRuntimeRestartWithOptions(context.Background(), false, true); err != nil {
		t.Fatalf("PrepareRuntimeRestartWithOptions: %v", err)
	}
	if !controller.stopConflicts {
		t.Fatal("conflicting-host authorization was not propagated to Codex Agent")
	}
}

func TestPrepareRuntimeRestartPersistsStoppedHostAndIsIdempotent(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	h := NewHandler(nil, nil)
	h.runtimeRestartStateFile = filepath.Join(t.TempDir(), "runtime-restart.json")
	controller := newFakeCodexRestartAgent()
	controller.prepareResult = agent.CodexRestartSnapshot{
		HostMode: "daemon", SocketPath: "/tmp/codex.sock", HostGeneration: 7, HostStopped: true,
	}
	h.SetDefaultAgent("codex", controller)

	result, err := h.PrepareRuntimeRestart(context.Background(), false)
	if err != nil {
		t.Fatalf("PrepareRuntimeRestart: %v", err)
	}
	if !result.Codex || result.CodexHost.HostGeneration != 7 || controller.prepareCalls != 1 {
		t.Fatalf("result=%#v prepareCalls=%d", result, controller.prepareCalls)
	}
	if !h.IsDraining() {
		t.Fatal("prepared restart reopened task admission")
	}
	if _, err := os.Stat(h.runtimeRestartStateFile); err != nil {
		t.Fatalf("restart state not persisted: %v", err)
	}
	if _, err := h.PrepareRuntimeRestart(context.Background(), true); err != nil || controller.prepareCalls != 1 {
		t.Fatalf("idempotent prepare err=%v calls=%d", err, controller.prepareCalls)
	}
	if _, err := agent.AcquireCodexCLIFrontendLease(); !errors.Is(err, agent.ErrCodexRestartInProgress) {
		t.Fatalf("prepared service released frontend fence: %v", err)
	}
}

func TestPrepareRuntimeRestartRestoresAdmissionWhenCodexBlocks(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	h := NewHandler(nil, nil)
	h.runtimeRestartStateFile = filepath.Join(t.TempDir(), "runtime-restart.json")
	controller := newFakeCodexRestartAgent()
	controller.prepareErr = agent.ErrCodexDesktopFrontendActive
	h.SetDefaultAgent("codex", controller)

	_, err := h.PrepareRuntimeRestart(context.Background(), false)
	if !errors.Is(err, ErrRuntimeRestartBlocked) || !errors.Is(err, agent.ErrCodexDesktopFrontendActive) {
		t.Fatalf("PrepareRuntimeRestart error=%v", err)
	}
	if h.IsDraining() {
		t.Fatal("safe Codex preflight rejection left admission closed")
	}
	if _, err := os.Stat(h.runtimeRestartStateFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked restart persisted state: %v", err)
	}
}

func TestPrepareRuntimeRestartKeepsJournalAndAdmissionClosedWhenHostStopIsUnknown(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	h := NewHandler(nil, nil)
	h.runtimeRestartStateFile = filepath.Join(t.TempDir(), "runtime-restart.json")
	controller := newFakeCodexRestartAgent()
	controller.prepareResult = agent.CodexRestartSnapshot{HostGeneration: 7, HostStopped: true}
	controller.prepareErr = agent.ErrCodexRestartUnsafe
	h.SetDefaultAgent("codex", controller)

	_, err := h.PrepareRuntimeRestart(context.Background(), false)
	if !errors.Is(err, agent.ErrCodexRestartUnsafe) {
		t.Fatalf("PrepareRuntimeRestart error=%v", err)
	}
	if !h.IsDraining() {
		t.Fatal("unknown Host stop outcome reopened admission")
	}
	if _, err := os.Stat(h.runtimeRestartStateFile); err != nil {
		t.Fatalf("unknown Host stop outcome lost recovery journal: %v", err)
	}
	state, exists, err := h.readRuntimeRestartState()
	if err != nil || !exists || !state.CodexHost.HostStopped || state.CodexHost.HostGeneration != 7 {
		t.Fatalf("recovery state=%#v exists=%v err=%v", state, exists, err)
	}
	if err := h.CancelRuntimeRestart(context.Background()); err != nil {
		t.Fatalf("CancelRuntimeRestart: %v", err)
	}
	if controller.cancelCalls != 1 || h.IsDraining() {
		t.Fatalf("cancelCalls=%d draining=%v", controller.cancelCalls, h.IsDraining())
	}
}

func TestCancelRuntimeRestartRestoresHostBeforeAdmission(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	h := NewHandler(nil, nil)
	h.runtimeRestartStateFile = filepath.Join(t.TempDir(), "runtime-restart.json")
	controller := newFakeCodexRestartAgent()
	controller.prepareResult = agent.CodexRestartSnapshot{HostGeneration: 7, HostStopped: true}
	h.SetDefaultAgent("codex", controller)
	if _, err := h.PrepareRuntimeRestart(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := h.CancelRuntimeRestart(context.Background()); err != nil {
		t.Fatalf("CancelRuntimeRestart: %v", err)
	}
	if controller.cancelCalls != 1 || h.IsDraining() {
		t.Fatalf("cancelCalls=%d draining=%v", controller.cancelCalls, h.IsDraining())
	}
	if _, err := os.Stat(h.runtimeRestartStateFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart state remains after cancel: %v", err)
	}
	cliLease, err := agent.AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatalf("cancelled restart still blocks controlled CLI: %v", err)
	}
	_ = cliLease.Close()
}

func TestRecoverRuntimeRestartVerifiesGenerationBeforeRemovingState(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	stateFile := filepath.Join(t.TempDir(), "runtime-restart.json")
	first := NewHandler(nil, nil)
	first.runtimeRestartStateFile = stateFile
	controller := newFakeCodexRestartAgent()
	controller.prepareResult = agent.CodexRestartSnapshot{HostGeneration: 7, HostStopped: true}
	first.SetDefaultAgent("codex", controller)
	if _, err := first.PrepareRuntimeRestart(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	secondController := newFakeCodexRestartAgent()
	secondController.verifyResult = agent.CodexRestartSnapshot{HostGeneration: 8}
	second := NewHandler(nil, nil)
	second.runtimeRestartStateFile = stateFile
	second.SetDefaultAgent("codex", secondController)
	if err := second.RecoverRuntimeRestart(context.Background()); err != nil {
		t.Fatalf("RecoverRuntimeRestart: %v", err)
	}
	if secondController.verifyCalls != 1 {
		t.Fatalf("verifyCalls=%d", secondController.verifyCalls)
	}
	if _, err := os.Stat(stateFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified restart state remains: %v", err)
	}
}

func TestRecoverRuntimeRestartKeepsStateWhenHostVerificationFails(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	stateFile := filepath.Join(t.TempDir(), "runtime-restart.json")
	first := NewHandler(nil, nil)
	first.runtimeRestartStateFile = stateFile
	controller := newFakeCodexRestartAgent()
	controller.prepareResult = agent.CodexRestartSnapshot{HostGeneration: 7, HostStopped: true}
	first.SetDefaultAgent("codex", controller)
	if _, err := first.PrepareRuntimeRestart(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	secondController := newFakeCodexRestartAgent()
	secondController.verifyErr = errors.New("generation unchanged")
	second := NewHandler(nil, nil)
	second.runtimeRestartStateFile = stateFile
	second.SetDefaultAgent("codex", secondController)
	if err := second.RecoverRuntimeRestart(context.Background()); err == nil {
		t.Fatal("RecoverRuntimeRestart error=nil, want fail-closed verification")
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("failed verification removed recovery state: %v", err)
	}
}
