package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

func TestWriteTerminalOutboxStatusRendersOperationalSummary(t *testing.T) {
	buffer := &bytes.Buffer{}
	command := &cobra.Command{}
	command.SetOut(buffer)
	status := messaging.TerminalOutboxStatus{
		Pending: 2, Preparing: 1, RecentError: "temporary failure",
		OldestCreatedAt: time.Now().Add(-5 * time.Minute),
		NextAttempt:     time.Now().Add(time.Minute),
		Entries: []messaging.TerminalOutboxEntryStatus{{
			ID: "entry-1", AgentName: "codex", Attempts: 3, NextAttempt: time.Now().Add(time.Minute),
		}},
	}
	if err := writeTerminalOutboxStatus(command, status, false); err != nil {
		t.Fatal(err)
	}
	output := buffer.String()
	for _, want := range []string{"待投递: 2", "准备中 1", "最近错误: temporary failure", "entry-1", "attempts=3"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output=%q missing %q", output, want)
		}
	}
}

func TestWriteTerminalOutboxStatusEmptyAndJSON(t *testing.T) {
	buffer := &bytes.Buffer{}
	command := &cobra.Command{}
	command.SetOut(buffer)
	if err := writeTerminalOutboxStatus(command, messaging.TerminalOutboxStatus{}, false); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "终态 outbox 无积压。\n" {
		t.Fatalf("output=%q", got)
	}
	buffer.Reset()
	if err := writeTerminalOutboxStatus(command, messaging.TerminalOutboxStatus{Pending: 2}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), `"pending": 2`) {
		t.Fatalf("json=%q", buffer.String())
	}
}
