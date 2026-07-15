package messaging

import (
	"path/filepath"
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

func TestAcquireCodexSessionRolloutOnlyActiveStillStartsReadOnlyObserver(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	codexDir := t.TempDir()
	writeLocalCodexSession(
		t, codexDir, "thread-b", t.TempDir(), "Desktop 断联任务", "2026-07-15T08:00:00Z",
	)
	rolloutPath := filepath.Join(
		codexDir, "sessions", "2026", "04", "29", "rollout-thread-b.jsonl",
	)
	appendCodexRolloutRecord(t, rolloutPath, rolloutTaskStartedRecord("turn-b"))
	fixture.h.SetCodexLocalSessionDir(codexDir)
	fixture.agent.setThreadBinding("thread-b", desktopAcquireBinding("thread-b"))
	fixture.agent.threadState = agent.CodexThreadState{ThreadID: "thread-b"}

	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.externalActive || result.externalState.Controllable {
		t.Fatalf("result=%#v，rollout-only active 应启动只读观察", result)
	}
	appendCodexRolloutRecord(t, rolloutPath, rolloutTaskCompleteRecord("turn-b", "任务完成"))
	waitUntil(t, func() bool {
		_, active := fixture.h.activeTask(result.route.conversationID)
		return !active
	})
}
