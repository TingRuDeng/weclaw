package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeACPProgressSuppressesInternalUpdates(t *testing.T) {
	state := newClaudeACPProgressState()
	updates := []sessionUpdate{
		{SessionUpdate: "agent_thought_chunk", Content: json.RawMessage(`{"type":"text","text":"正在检查依赖"}`)},
		{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "运行单元测试", Status: "pending"},
		{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Title: "运行单元测试", Status: "in_progress"},
		{SessionUpdate: "plan", Entries: []acpPlanEntry{{Content: "修复失败测试", Status: "in_progress"}}},
		{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"image","text":"raw-value"}`)},
		{SessionUpdate: "unknown", Content: json.RawMessage(`{"text":"raw-value"}`)},
	}
	for _, update := range updates {
		if event, ok := state.progressEvent(&update); ok {
			t.Fatalf("update=%s event=%#v, want hidden", update.SessionUpdate, event)
		}
	}
}

func TestClaudeACPProgressBuffersVisibleMessageUntilToolBoundary(t *testing.T) {
	state := newClaudeACPProgressState()
	chunks := []sessionUpdate{
		{
			SessionUpdate: "agent_message_chunk", MessageID: "message-1", Sequence: 41,
			Content: json.RawMessage(`{"type":"text","text":"我先检查\n"}`),
		},
		{
			SessionUpdate: "agent_message_chunk", MessageID: "message-1", Sequence: 42,
			Content: json.RawMessage(`{"type":"text","text":"**当前实现**。"}`),
		},
	}
	for index := range chunks {
		if event, ok := state.progressEvent(&chunks[index]); ok {
			t.Fatalf("chunk %d emitted early: %#v", index, event)
		}
	}
	event, ok := state.progressEvent(&sessionUpdate{
		SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "读取代码", Sequence: 43,
	})
	if !ok {
		t.Fatal("tool boundary must flush the completed user-visible message")
	}
	if event.ID != "agent-message:message-1" || event.Kind != ProgressKindMessage || event.State != ProgressStateCompleted || event.Sequence != 42 {
		t.Fatalf("event=%#v", event)
	}
	if event.Text != "我先检查\n**当前实现**。" {
		t.Fatalf("text=%q", event.Text)
	}
}

func TestClaudeACPProgressFlushesPreviousMessageWhenMessageIDChanges(t *testing.T) {
	state := newClaudeACPProgressState()
	first := &sessionUpdate{
		SessionUpdate: "agent_message_chunk", MessageID: "message-1", Sequence: 10,
		Content: json.RawMessage(`{"type":"text","text":"第一条可见消息"}`),
	}
	if event, ok := state.progressEvent(first); ok {
		t.Fatalf("first chunk emitted early: %#v", event)
	}
	second := &sessionUpdate{
		SessionUpdate: "agent_message_chunk", MessageID: "message-2", Sequence: 11,
		Content: json.RawMessage(`{"type":"text","text":"第二条可见消息"}`),
	}
	event, ok := state.progressEvent(second)
	if !ok || event.ID != "agent-message:message-1" || event.Text != "第一条可见消息" || event.Sequence != 10 {
		t.Fatalf("first boundary event=(%#v,%v)", event, ok)
	}
	event, ok = state.progressEvent(&sessionUpdate{SessionUpdate: "tool_call", ToolCallID: "call-2"})
	if !ok || event.ID != "agent-message:message-2" || event.Text != "第二条可见消息" || event.Sequence != 11 {
		t.Fatalf("second boundary event=(%#v,%v)", event, ok)
	}
}

func TestClaudeACPProgressUsesToolBoundaryWithoutMessageID(t *testing.T) {
	state := newClaudeACPProgressState()
	chunks := []sessionUpdate{
		{SessionUpdate: "agent_message_chunk", Sequence: 20, Content: json.RawMessage(`{"type":"text","text":"兼容"}`)},
		{SessionUpdate: "agent_message_chunk", Sequence: 21, Content: json.RawMessage(`{"type":"text","text":"旧版 ACP"}`)},
	}
	for index := range chunks {
		if event, ok := state.progressEvent(&chunks[index]); ok {
			t.Fatalf("chunk %d emitted early: %#v", index, event)
		}
	}
	event, ok := state.progressEvent(&sessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-legacy"})
	if !ok || event.ID != "" || event.Text != "兼容旧版 ACP" || event.Sequence != 21 {
		t.Fatalf("legacy boundary event=(%#v,%v)", event, ok)
	}
}

func TestClaudeACPProgressChatEmitsOnlyCompletedVisibleMessages(t *testing.T) {
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
		updates <- &sessionUpdate{SessionUpdate: "agent_thought_chunk", MessageID: "thought-1", Content: json.RawMessage(`{"type":"text","text":"正在分析"}`)}
		updates <- &sessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: "message-1", Content: json.RawMessage(`{"type":"text","text":"我先检查"}`)}
		updates <- &sessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: "message-1", Content: json.RawMessage(`{"type":"text","text":"当前实现。"}`)}
		updates <- &sessionUpdate{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "读取代码", Status: "in_progress"}
		updates <- &sessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Title: "读取代码", Status: "completed"}
		updates <- &sessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: "message-2", Content: json.RawMessage(`{"type":"text","text":"最终结果"}`)}
		return json.RawMessage(`{"text":"最终结果"}`), nil
	}
	var progress []string
	reply, err := ag.chatLegacyACP(context.Background(), "conversation-1", "开始", func(text string) {
		progress = append(progress, text)
	})
	if err != nil {
		t.Fatalf("chatLegacyACP error: %v", err)
	}
	if reply != "我先检查当前实现。最终结果" || len(progress) != 1 || progress[0] != "我先检查当前实现。" {
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
