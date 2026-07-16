package agent

import (
	"bufio"
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestReadLoopSuppressesKnownCodexNoiseMethods(t *testing.T) {
	input := strings.Join([]string{
		`{"method":"item/commandExecution/outputDelta","params":{"threadId":"thread-1","turnId":"turn-1","delta":"noise"}}`,
		`{"method":"turn/diff/updated","params":{"threadId":"thread-1","turnId":"turn-1","diff":"noise"}}`,
		`{"method":"remoteControl/status/changed","params":{"threadId":"thread-1","status":"remote"}}`,
		`{"method":"unknown/newEvent","params":{"threadId":"thread-1"}}`,
	}, "\n") + "\n"

	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldOutput) })

	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.scanner = bufio.NewScanner(strings.NewReader(input))

	a.readLoop()

	got := logs.String()
	if strings.Contains(got, "item/commandExecution/outputDelta") {
		t.Fatalf("known command output delta should not be logged: %s", got)
	}
	if strings.Contains(got, "turn/diff/updated") {
		t.Fatalf("known diff update should not be logged: %s", got)
	}
	if strings.Contains(got, "remoteControl/status/changed") {
		t.Fatalf("known remote control status should not be logged: %s", got)
	}
	if !strings.Contains(got, "unknown/newEvent") {
		t.Fatalf("unknown method should still be logged: %s", got)
	}
}

func TestShouldLogUnhandledMethodRateLimitsByMethod(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	if !a.shouldLogUnhandledMethod("unknown/newEvent", now) {
		t.Fatal("first unknown method should be logged")
	}
	if a.shouldLogUnhandledMethod("unknown/newEvent", now.Add(time.Minute)) {
		t.Fatal("same unknown method should be rate limited")
	}
	if !a.shouldLogUnhandledMethod("unknown/other", now.Add(time.Minute)) {
		t.Fatal("different unknown method should be logged independently")
	}
	if !a.shouldLogUnhandledMethod("unknown/newEvent", now.Add(acpUnhandledMethodLogInterval)) {
		t.Fatal("same unknown method should be logged after interval")
	}
	if a.shouldLogUnhandledMethod("  ", now.Add(acpUnhandledMethodLogInterval)) {
		t.Fatal("blank method should not be logged")
	}
}

func TestCodexTurnMetricsRecordsFirstEventOnce(t *testing.T) {
	started := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	metrics := newCodexTurnMetrics(started)

	firstLatency, first := metrics.markFirstEvent(started.Add(2 * time.Second))
	if !first || firstLatency != 2*time.Second {
		t.Fatalf("first event=(%v,%v), want first latency 2s", firstLatency, first)
	}
	secondLatency, second := metrics.markFirstEvent(started.Add(3 * time.Second))
	if second || secondLatency != 0 {
		t.Fatalf("second event=(%v,%v), want ignored", secondLatency, second)
	}
	if got := metrics.elapsed(started.Add(5 * time.Second)); got != 5*time.Second {
		t.Fatalf("elapsed=%s, want 5s", got)
	}
}
