package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeACPProgressMapsStructuredUpdates(t *testing.T) {
	tests := []struct {
		name   string
		update sessionUpdate
		want   string
	}{
		{name: "思考", update: sessionUpdate{SessionUpdate: "agent_thought_chunk", Content: json.RawMessage(`{"type":"text","text":"正在检查依赖"}`)}, want: "思考：正在检查依赖"},
		{name: "工具开始", update: sessionUpdate{SessionUpdate: "tool_call", Title: "运行单元测试", Status: "pending"}, want: "工具：运行单元测试（等待中）"},
		{name: "工具更新", update: sessionUpdate{SessionUpdate: "tool_call_update", Title: "运行单元测试", Status: "in_progress"}, want: "工具：运行单元测试（进行中）"},
		{name: "计划", update: sessionUpdate{SessionUpdate: "plan", Entries: []acpPlanEntry{{Content: "修复失败测试", Status: "in_progress"}}}, want: "计划：修复失败测试（进行中）"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := claudeACPProgressText(&test.update)
			if !ok || got != test.want {
				t.Fatalf("progress=(%q,%v), want (%q,true)", got, ok, test.want)
			}
		})
	}
}

func TestClaudeACPProgressRejectsBodyAndRawJSON(t *testing.T) {
	updates := []sessionUpdate{
		{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"text","text":"最终正文"}`)},
		{SessionUpdate: "agent_thought_chunk", Content: json.RawMessage(`{"type":"image","text":"raw-value"}`)},
		{SessionUpdate: "tool_call", Content: json.RawMessage(`{"secret":"raw-value"}`)},
		{SessionUpdate: "unknown", Content: json.RawMessage(`{"text":"raw-value"}`)},
	}
	for _, update := range updates {
		if got, ok := claudeACPProgressText(&update); ok || strings.Contains(got, "raw-value") {
			t.Fatalf("update=%s progress=(%q,%v), want hidden", update.SessionUpdate, got, ok)
		}
	}
}

func TestClaudeACPProgressCarriesToolTitleAndSuppressesDuplicate(t *testing.T) {
	state := newClaudeACPProgressState()
	started := &sessionUpdate{
		SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "运行测试", Status: "in_progress",
	}
	if text, ok := state.progressText(started); !ok || text != "工具：运行测试（进行中）" {
		t.Fatalf("started=(%q,%v)", text, ok)
	}
	duplicate := &sessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Status: "in_progress"}
	if text, ok := state.progressText(duplicate); ok || text != "" {
		t.Fatalf("duplicate=(%q,%v), want suppressed", text, ok)
	}
	completed := &sessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Status: "completed"}
	if text, ok := state.progressText(completed); !ok || text != "工具：运行测试（已完成）" {
		t.Fatalf("completed=(%q,%v)", text, ok)
	}
}

func TestClaudeACPProgressSuppressesNonAdjacentDuplicate(t *testing.T) {
	state := newClaudeACPProgressState()
	tool := &sessionUpdate{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "运行测试", Status: "in_progress"}
	if _, ok := state.progressText(tool); !ok {
		t.Fatal("first tool progress must emit")
	}
	plan := &sessionUpdate{SessionUpdate: "plan", Entries: []acpPlanEntry{{Content: "检查结果", Status: "in_progress"}}}
	if _, ok := state.progressText(plan); !ok {
		t.Fatal("intermediate plan progress must emit")
	}
	if text, ok := state.progressText(tool); ok || text != "" {
		t.Fatalf("repeated tool=(%q,%v), want suppressed", text, ok)
	}
}

func TestClaudeACPProgressBoundsStructuredHistory(t *testing.T) {
	state := newClaudeACPProgressState()
	for index := 0; index <= claudeProgressHistoryLimit; index++ {
		state.progressText(&sessionUpdate{
			SessionUpdate: "tool_call", ToolCallID: fmt.Sprintf("call-%d", index),
			Title: fmt.Sprintf("工具-%d", index), Status: "in_progress",
		})
	}
	if len(state.emitted) != claudeProgressHistoryLimit || len(state.toolTitles) != claudeProgressHistoryLimit {
		t.Fatalf("history=%d tools=%d", len(state.emitted), len(state.toolTitles))
	}
	if _, exists := state.toolTitles["call-0"]; exists {
		t.Fatal("oldest tool title must be evicted")
	}
	if state.emitted[len(state.emitted)-1] != "工具：工具-128（进行中）" {
		t.Fatalf("latest progress=%q", state.emitted[len(state.emitted)-1])
	}
}

func TestClaudeACPProgressAccumulatesThoughtChunks(t *testing.T) {
	state := newClaudeACPProgressState()
	first := &sessionUpdate{
		SessionUpdate: "agent_thought_chunk", MessageID: "thought-1",
		Content: json.RawMessage(`{"type":"text","text":"正在"}`),
	}
	second := &sessionUpdate{
		SessionUpdate: "agent_thought_chunk", MessageID: "thought-1",
		Content: json.RawMessage(`{"type":"text","text":"分析"}`),
	}
	if text, ok := state.progressText(first); !ok || text != "思考：正在" {
		t.Fatalf("first=(%q,%v)", text, ok)
	}
	if text, ok := state.progressText(second); !ok || text != "思考：正在分析" {
		t.Fatalf("second=(%q,%v)", text, ok)
	}
	state.progressText(&sessionUpdate{
		SessionUpdate: "agent_thought_chunk", MessageID: "thought-1",
		Content: json.RawMessage(fmt.Sprintf(`{"type":"text","text":%q}`, strings.Repeat("长", claudeThoughtBufferMaxRunes+1))),
	})
	if got := len([]rune(state.thoughtText)); got != claudeThoughtBufferMaxRunes {
		t.Fatalf("thought buffer runes=%d", got)
	}
}

func TestClaudeACPProgressSelectsPlanAndTranslatesStatuses(t *testing.T) {
	entries := []acpPlanEntry{
		{Content: "第一步", Status: "completed"},
		{Content: "第二步", Status: "pending"},
	}
	if text, ok := planProgressText(entries); !ok || text != "计划：第一步（已完成）" {
		t.Fatalf("completed plan=(%q,%v)", text, ok)
	}
	entries[0].Status = "unknown"
	if text, ok := planProgressText(entries); !ok || text != "计划：第二步（等待中）" {
		t.Fatalf("pending plan=(%q,%v)", text, ok)
	}
	for status, want := range map[string]string{
		"failed": "失败", "cancelled": "已取消", "unknown": "",
	} {
		if got := claudeProgressStatus(status); got != want {
			t.Fatalf("status %s=%q, want %q", status, got, want)
		}
	}
}

func TestClaudeACPProgressUsesLatestLineAndLimitsLength(t *testing.T) {
	longLine := strings.Repeat("进", claudeProgressMaxRunes+10)
	text := progressSummary("旧行\n\n" + longLine)
	if !strings.HasSuffix(text, "…") || len([]rune(text)) != claudeProgressMaxRunes {
		t.Fatalf("summary runes=%d suffix=%q", len([]rune(text)), text[len(text)-3:])
	}
	if text, ok := planProgressText(nil); ok || text != "" {
		t.Fatalf("empty plan=(%q,%v)", text, ok)
	}
}

func TestClaudeACPProgressChatEmitsFinalOnce(t *testing.T) {
	ag := NewACPAgent(ACPAgentConfig{ConfiguredName: "claude", StateFile: filepath.Join(t.TempDir(), "state.json")})
	ag.sessions["conversation-1"] = "session-1"
	ag.started = true
	ag.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "session/prompt" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		ag.notifyMu.Lock()
		updates := ag.notifyCh["session-1"]
		ag.notifyMu.Unlock()
		updates <- &sessionUpdate{SessionUpdate: "agent_thought_chunk", Content: json.RawMessage(`{"type":"text","text":"正在分析"}`)}
		updates <- &sessionUpdate{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"text","text":"完成"}`)}
		return json.RawMessage(`{"text":"完成"}`), nil
	}
	var progress []string
	reply, err := ag.chatLegacyACP(context.Background(), "conversation-1", "开始", func(text string) {
		progress = append(progress, text)
	})
	if err != nil {
		t.Fatalf("chatLegacyACP error: %v", err)
	}
	if reply != "完成" || !reflect.DeepEqual(progress, []string{"思考：正在分析"}) {
		t.Fatalf("reply=%q progress=%#v", reply, progress)
	}
}

func TestClaudeACPProgressDoesNotLogRawPayloads(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	ag := NewACPAgent(ACPAgentConfig{ConfiguredName: "claude", StateFile: filepath.Join(t.TempDir(), "state.json")})
	ag.rpcCall = func(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
		return json.RawMessage(`{"text":"sensitive-result"}`), nil
	}
	<-ag.startLegacyPrompt(context.Background(), "session-1", "开始")
	ag.handleSessionUpdate(json.RawMessage(`{"sensitive-update":`))
	if strings.Contains(logs.String(), "sensitive-result") || strings.Contains(logs.String(), "sensitive-update") {
		t.Fatalf("logs expose raw payload: %s", logs.String())
	}
}
