package antigravity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadTranscriptIncrementalFiltersToolPayloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path,
		"{\"type\":\"USER_INPUT\",\"content\":\"[WeClaw/飞书] 请总结\"}\n",
		"{\"type\":\"PLANNER_RESPONSE\",\"content\":\"正在处理\",\"tool_calls\":["+
			"{\"name\":\"update_focus\",\"args\":{\"focus\":\"梳理仓库结构\"}},"+
			"{\"name\":\"ask_question\",\"args\":{\"question\":\"继续吗\"}},"+
			"{\"name\":\"run_command\",\"args\":{\"command\":\"cat secret.txt\"}}]}\n",
		"{\"type\":\"PLANNER_RESPONSE\",\"content\":\"完成\"}\n",
		"{\"type\":\"PLANNER_RESPONSE\",\"content\":\"半行",
	)
	result, err := ReadTranscript(path, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Discontinuous {
		t.Fatal("initial cursor must be continuous")
	}
	kinds := make([]string, 0, len(result.Events))
	for _, event := range result.Events {
		kinds = append(kinds, event.Kind)
	}
	want := []string{EventTurnInput, EventProgress, EventFocus, EventAwaitDesktop, EventTool, EventReply}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}
	if !result.Events[0].FromFeishu {
		t.Fatal("turn input must record the Feishu prefix")
	}
	if result.Events[4].Tool != "run_command" {
		t.Fatalf("tool event = %q", result.Events[4].Tool)
	}
	for _, event := range result.Events {
		text := event.Text + event.Focus + event.Tool
		if strings.Contains(text, "secret.txt") {
			t.Fatalf("tool payload leaked into event: %+v", event)
		}
	}
	if strings.Contains(result.Events[5].Text, "半行") {
		t.Fatal("half line must not be consumed")
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("续行\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := ReadTranscript(path, result.NextFileID, result.NextOffset)
	if err != nil {
		t.Fatal(err)
	}
	if next.Discontinuous {
		t.Fatal("appending complete lines must stay continuous")
	}
	if len(next.Events) != 1 || next.Events[0].Kind != EventReply || next.Events[0].Text != "半行续行" {
		t.Fatalf("next events = %+v", next.Events)
	}
}

func TestReadTranscriptMarksRotationAndTruncationDiscontinuous(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	writeTranscript(t, path, "{\"type\":\"PLANNER_RESPONSE\",\"content\":\"old\"}\n")
	baseline, err := ReadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.HasHistory || baseline.LatestReply != "old" {
		t.Fatalf("baseline = %+v", baseline)
	}

	replaced := filepath.Join(dir, "replacement.jsonl")
	writeTranscript(t, replaced, "{\"type\":\"PLANNER_RESPONSE\",\"content\":\"new\"}\n")
	if err := os.Rename(replaced, path); err != nil {
		t.Fatal(err)
	}
	rotated, err := ReadTranscript(path, baseline.FileID, baseline.Offset)
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.Discontinuous {
		t.Fatal("replaced inode must be discontinuous")
	}
	if rotated.NextOffset == 0 || len(rotated.Events) != 1 || rotated.Events[0].Text != "new" {
		t.Fatalf("rotated result = %+v", rotated)
	}

	if err := os.WriteFile(path, []byte("{\"type\":\"PLANNER_RESPONSE\",\"content\":\"new\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	truncated, err := ReadTranscript(path, rotated.NextFileID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated.Discontinuous || len(truncated.Events) != 1 || truncated.Events[0].Text != "new" {
		t.Fatalf("truncated result = %+v", truncated)
	}
}

func TestReadTranscriptFastFirstTurnAfterCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, "{\"type\":\"USER_INPUT\",\"content\":\"[WeClaw/飞书] 第一句\"}\n")
	result, err := ReadTranscript(path, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Discontinuous {
		t.Fatal("an empty create baseline must accept the first file identity")
	}
	if len(result.Events) != 1 || result.Events[0].Kind != EventTurnInput {
		t.Fatalf("fast first turn events = %+v", result.Events)
	}
}

func TestReadTranscriptSanitizesErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, "{\"type\":\"ERROR_MESSAGE\",\"content\":\"token=abc\\n"+strings.Repeat("x", 400)+"\"}\n")
	result, err := ReadTranscript(path, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Kind != EventError {
		t.Fatalf("error events = %+v", result.Events)
	}
	if strings.Contains(result.Events[0].Text, "\n") || len([]rune(result.Events[0].Text)) > 201 {
		t.Fatalf("error text is not a short single line: %q", result.Events[0].Text)
	}
	if strings.Contains(result.Events[0].Text, "abc") || !strings.Contains(result.Events[0].Text, "[redacted]") {
		t.Fatalf("error text is not redacted: %q", result.Events[0].Text)
	}
}

func TestTranscriptPathRejectsTraversal(t *testing.T) {
	for _, id := range []string{"", "../escape", "a/b", ".", "..", "a b", "a\\b"} {
		if _, err := TranscriptPath(t.TempDir(), id); err == nil {
			t.Fatalf("conversation id %q must be rejected", id)
		}
	}
	if _, err := TranscriptPath(t.TempDir(), "0f5b4a8e-1c2d_3e4f"); err != nil {
		t.Fatalf("safe id rejected: %v", err)
	}
}
