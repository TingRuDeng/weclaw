package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexUnboundMessageDoesNotOpenProgress(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "test"}}}
	r := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	h.startCodexAgentTask(codexAgentTaskOptions{ctx: context.Background(), userID: "user", routeUserID: "user", agentName: "codex", agent: ag, reply: r, progressCfg: config.ProgressConfig{Mode: "stream"}})
	if r.OpenStreamCalls != 0 || h.ActiveTaskCount() != 0 || ag.wasChatCalled() || !containsText(r.TextsSnapshot(), "/cx ls") {
		t.Fatalf("streams=%d tasks=%d replies=%v", r.OpenStreamCalls, h.ActiveTaskCount(), r.TextsSnapshot())
	}
}

func TestCodexUnboundBroadcastRejectedBeforeAdmission(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "test"}}}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	results := make(chan broadcastAgentResult, 1)
	runtime, ok := h.beginCodexBroadcastRuntime(broadcastAgentsRequest{ctx: context.Background(), userID: "user", routeUserID: "user"}, "codex", ag, context.Background(), reply, results)
	if ok {
		runtime.finish()
		t.Fatal("unbound request admitted as task")
	}
	result := <-results
	if !strings.Contains(result.reply, "/cx ls") || h.ActiveTaskCount() != 0 || ag.wasChatCalled() {
		t.Fatalf("result=%+v", result)
	}
}

func TestCodexPendingBindingClearsStaleAgentMapping(t *testing.T) {
	for _, entry := range []string{"message", "broadcast", "prepare"} {
		t.Run(entry, func(t *testing.T) {
			t.Setenv("WECLAW_HOME", t.TempDir())
			h := NewHandler(nil, nil)
			ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "test"}}, threadID: "stale-thread"}
			route := h.codexConversationRouteForSession("user", "user", "codex", ag)
			h.ensureCodexSessions().setPendingNew(route.bindingKey, route.workspaceRoot)
			reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
			switch entry {
			case "message":
				h.startCodexAgentTask(codexAgentTaskOptions{ctx: context.Background(), userID: "user", routeUserID: "user", agentName: "codex", agent: ag, reply: reply, progressCfg: config.ProgressConfig{Mode: "stream"}})
			case "broadcast":
				results := make(chan broadcastAgentResult, 1)
				if runtime, ok := h.beginCodexBroadcastRuntime(broadcastAgentsRequest{ctx: context.Background(), userID: "user", routeUserID: "user"}, "codex", ag, context.Background(), reply, results); ok {
					runtime.finish()
					t.Fatal("pending binding admitted")
				}
			case "prepare":
				if err := h.prepareCodexConversation(context.Background(), route, ag); err == nil {
					t.Fatal("pending binding accepted")
				}
			}
			if ag.clearCalledWith != route.conversationID || ag.threadID != "" {
				t.Fatalf("stale mapping retained: cleared=%q thread=%q", ag.clearCalledWith, ag.threadID)
			}
			if h.ActiveTaskCount() != 0 || reply.OpenStreamCalls != 0 || ag.wasChatCalled() {
				t.Fatal("pending input created a task or executed")
			}
		})
	}
}
