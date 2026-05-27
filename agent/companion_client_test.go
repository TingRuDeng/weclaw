package agent

import (
	"context"
	"testing"
)

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
