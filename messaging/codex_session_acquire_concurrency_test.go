package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexAcquireConcurrentResult struct {
	err error
}

func TestAcquireCodexSessionConcurrentClaimHasSingleRuntimeWinner(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexSessions = newCodexSessionStore()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	requests := []codexSessionAcquireRequest{
		concurrentCodexAcquireRequest(t, h, ag, "route-a", "/workspace/a", "thread-a"),
		concurrentCodexAcquireRequest(t, h, ag, "route-b", "/workspace/c", "thread-c"),
	}
	claimDesktopControlForAcquireTest(t, h, "thread-target")
	h.codexSessions.setThread(requests[0].route.bindingKey, "/workspace/target", "thread-target")
	h.codexSessions.setThread(requests[1].route.bindingKey, "/workspace/target", "thread-target")
	for _, threadID := range []string{"thread-a", "thread-c", "thread-target"} {
		ag.setThreadBinding(threadID, desktopAcquireBinding(threadID))
	}
	for index := range requests {
		requests[index].route.workspaceRoot = "/workspace/target"
		requests[index].route.conversationID = buildCodexConversationID(
			requests[index].routeUserID, "codex", "/workspace/target",
		)
		requests[index].route.threadID = "thread-target"
	}
	results := runCodexAcquireConcurrently(h, requests)
	if (results[0].err == nil) == (results[1].err == nil) {
		t.Fatalf("results=%#v，必须只有一个成功", results)
	}
	loser := 0
	if results[0].err == nil {
		loser = 1
	}
	if !errors.Is(results[loser].err, errCodexRemoteSelectionOtherRoute) {
		t.Fatalf("loser error=%v", results[loser].err)
	}
	targetHandoffs := 0
	for _, request := range ag.handoffRequests() {
		if request.Ref.ThreadID == "thread-target" {
			targetHandoffs++
		}
	}
	if targetHandoffs != 1 {
		t.Fatalf("target handoffs=%d, want 1", targetHandoffs)
	}
}

func concurrentCodexAcquireRequest(t *testing.T, h *Handler, ag *fakeCodexLiveAgent, routeUser string, workspace string, threadID string) codexSessionAcquireRequest {
	t.Helper()
	bindingKey := codexBindingKey(routeUser, "codex")
	h.codexSessions.setThread(bindingKey, workspace, threadID)
	h.codexSessions.setActiveWorkspace(bindingKey, workspace)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: routeUser, agentName: "codex", bindingKey: bindingKey,
		workspace: workspace, threadID: threadID,
	})
	return codexSessionAcquireRequest{
		ctx: context.Background(), actorUserID: routeUser, routeUserID: routeUser,
		agentName: "codex", agent: ag,
		route: codexConversationRoute{
			bindingKey: bindingKey, workspaceRoot: workspace,
			conversationID: buildCodexConversationID(routeUser, "codex", workspace),
			threadID:       threadID,
		},
		platform: platform.PlatformWeChat,
		reply:    platformtest.NewReplier(platform.Capabilities{Text: true}),
	}
}

func runCodexAcquireConcurrently(h *Handler, requests []codexSessionAcquireRequest) []codexAcquireConcurrentResult {
	start := make(chan struct{})
	results := make([]codexAcquireConcurrentResult, len(requests))
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, results[index].err = h.acquireCodexSessionWithBindingLocked(requests[index])
		}(index)
	}
	close(start)
	wait.Wait()
	return results
}
