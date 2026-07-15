package messaging

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestAcquireCodexSessionActiveProbeThenConfirmedTerminalSucceeds(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.setThreadBinding("thread-b", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeDesktop,
		State: agent.CodexThreadState{
			ThreadID: "thread-b", Active: true, ActiveTurnID: "turn-b",
		},
	})
	fixture.agent.threadState = agent.CodexThreadState{
		ThreadID: "thread-b", Active: false,
		LastTurnID: "turn-b", LastTurnStatus: "completed",
	}
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil {
		t.Fatal(err)
	}
	if result.externalActive {
		t.Fatalf("result=%#v，终态不应启动 watcher", result)
	}
	if _, active := fixture.h.activeTask(result.route.conversationID); active {
		t.Fatal("确认 terminal 后不应残留观察任务")
	}
	if fixture.h.codexSessions.controlIntent("thread-b").Owner != codexControlRemote {
		t.Fatal("确认 terminal 后仍应提交目标所有权")
	}
}
