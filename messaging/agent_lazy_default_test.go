package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestLazyDefaultAgentSelectionDoesNotStartRuntime(t *testing.T) {
	calls := 0
	h := NewHandlerWithErrorFactory(func(context.Context, string) (agent.Agent, error) {
		calls++
		return nil, nil
	}, nil)
	h.SetDefaultAgentName("codex")
	if got := h.defaultAgentNameForAccount("", ""); got != "codex" || calls != 0 || h.AgentByName("codex") != nil {
		t.Fatalf("default=%q calls=%d, want lazy configured default", got, calls)
	}
}

func TestNativeForceUsesMaintenanceControllerWithoutStartingDaemon(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	h := NewHandlerWithErrorFactory(func(context.Context, string) (agent.Agent, error) {
		t.Fatal("force must not require starting the daemon first")
		return nil, nil
	}, nil)
	h.SetAgentMetas([]AgentMeta{{Name: "codex", Type: "acp", Command: "codex", CodexNativeClient: true}})
	controller := &optionsFakeCodexRestartAgent{fakeCodexRestartAgent: newFakeCodexRestartAgent()}
	h.SetCodexRestartFactory(func(name string) (agent.CodexRestartController, error) {
		if name != "codex" {
			t.Fatalf("name=%q", name)
		}
		return controller, nil
	})
	result, err := h.PrepareRuntimeRestartWithOptions(context.Background(), true, false, true)
	if err != nil || !result.Codex || !controller.forceTerminate || controller.prepareCalls != 1 {
		t.Fatalf("result=%+v err=%v force=%v prepares=%d", result, err, controller.forceTerminate, controller.prepareCalls)
	}
	if err := h.CancelRuntimeRestart(context.Background()); err != nil {
		t.Fatal(err)
	}
}
