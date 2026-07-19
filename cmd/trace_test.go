package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/spf13/cobra"
)

func TestTraceQueryFromOptionsValidatesAndMapsFilters(t *testing.T) {
	query, err := traceQueryFromOptions([]string{"trace-1"}, traceCommandOptions{
		messageID: "message-1", threadID: "thread-1", since: "2026-07-19T10:00:00Z", limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if query.TraceID != "trace-1" || query.MessageID != "message-1" || query.ThreadID != "thread-1" || query.Limit != 25 || query.Since.IsZero() {
		t.Fatalf("query=%#v", query)
	}
	if _, err := traceQueryFromOptions(nil, traceCommandOptions{limit: 1001}); err == nil {
		t.Fatal("invalid limit was accepted")
	}
}

func TestWriteTracePageRendersStableIdentifiers(t *testing.T) {
	buffer := &bytes.Buffer{}
	command := &cobra.Command{}
	command.SetOut(buffer)
	page := observability.Page{Events: []observability.Event{{
		CreatedAt: time.Date(2026, 7, 19, 10, 0, 0, 0, time.Local),
		Stage:     "task.progress", State: "running", AgentName: "codex",
		TaskID: "task-1", ThreadID: "thread-1", Sequence: 7, Summary: "运行测试",
	}}}
	if err := writeTracePage(command, page, false); err != nil {
		t.Fatal(err)
	}
	output := buffer.String()
	for _, want := range []string{"task.progress", "agent=codex", "task=task-1", "thread=thread-1", "seq=7", "运行测试"} {
		if !bytes.Contains([]byte(output), []byte(want)) {
			t.Fatalf("output=%q missing %q", output, want)
		}
	}
}
