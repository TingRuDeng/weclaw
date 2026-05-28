package agent

import (
	"context"
	"testing"
	"time"
)

type starterCompanionHandler struct {
	started chan struct{}
}

func (h *starterCompanionHandler) Start(context.Context) error {
	close(h.started)
	return nil
}

func (h *starterCompanionHandler) HandleCompanionRequest(context.Context, CompanionRequest, func(string)) (string, error) {
	return "ok", nil
}

func TestRunCompanionClientHandlesRequest(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	ag := NewCompanionAgent(CompanionAgentConfig{
		Name:    "opencode",
		Command: "opencode",
		Cwd:     t.TempDir(),
	})
	if err := ag.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(ag.Stop)
	endpoint, err := ReadCompanionEndpoint("opencode", ag.Cwd())
	if err != nil {
		t.Fatalf("ReadCompanionEndpoint() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		err := RunCompanionClient(ctx, endpoint, CompanionHandlerFunc(
			func(_ context.Context, req CompanionRequest, progress func(string)) (string, error) {
				progress("progress: " + req.Text)
				return "handled: " + req.Text, nil
			},
		))
		if err != nil && ctx.Err() == nil {
			t.Errorf("RunCompanionClient() error = %v", err)
		}
	}()

	var deltas []string
	reply, err := ag.ChatWithProgress(context.Background(), "u1", "hello", func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("ChatWithProgress() error = %v", err)
	}
	if reply != "handled: hello" {
		t.Fatalf("reply = %q", reply)
	}
	if len(deltas) != 1 || deltas[0] != "progress: hello" {
		t.Fatalf("progress = %#v", deltas)
	}
}

func TestRunCompanionClientStartsRuntimeAfterHello(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	ag := NewCompanionAgent(CompanionAgentConfig{
		Name:    "codex",
		Command: "codex",
		Cwd:     t.TempDir(),
	})
	if err := ag.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(ag.Stop)
	endpoint, err := ReadCompanionEndpoint("codex", ag.Cwd())
	if err != nil {
		t.Fatalf("ReadCompanionEndpoint() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := &starterCompanionHandler{started: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunCompanionClient(ctx, endpoint, handler)
	}()

	select {
	case <-handler.started:
	case <-time.After(time.Second):
		t.Fatal("Companion runtime did not start after hello")
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil && ctx.Err() == nil {
			t.Fatalf("RunCompanionClient() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCompanionClient did not stop after cancel")
	}
}
