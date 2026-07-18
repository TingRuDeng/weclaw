package messaging

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexConcurrentFrontendsCanBindSameSharedThread(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexSessions.SetFilePath(filepath.Join(t.TempDir(), "codex-sessions.json"))
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-shared"})
	ag.setThreadBinding("thread-shared", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeWeClaw,
		State:   agent.CodexThreadState{ThreadID: "thread-shared"},
	})
	routes := []codexConversationRoute{
		{bindingKey: codexBindingKey("route-a", "codex"), workspaceRoot: "/workspace/a", conversationID: "conversation-a", threadID: "thread-shared"},
		{bindingKey: codexBindingKey("route-b", "codex"), workspaceRoot: "/workspace/b", conversationID: "conversation-b", threadID: "thread-shared"},
	}

	start := make(chan struct{})
	errs := make(chan error, len(routes))
	var wg sync.WaitGroup
	for _, route := range routes {
		route := route
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			unlock, err := h.lockCodexSessionBinding(context.Background(), route.bindingKey, "test-shared-bind")
			if err != nil {
				errs <- err
				return
			}
			defer unlock()
			_, err = h.acquireCodexSessionWithBindingLocked(codexSessionAcquireRequest{
				ctx: context.Background(), actorUserID: route.bindingKey, routeUserID: route.bindingKey,
				agentName: "codex", agent: ag, route: route,
				platform: platform.PlatformWeChat,
				reply:    platformtest.NewReplier(platform.Capabilities{Text: true}),
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("shared binding error=%v", err)
		}
	}
	for _, route := range routes {
		threadID, pending := h.codexSessions.getThread(route.bindingKey, route.workspaceRoot)
		if pending || threadID != "thread-shared" {
			t.Fatalf("route %q thread=%q pending=%v", route.bindingKey, threadID, pending)
		}
	}

	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(h.codexSessions.filePath)
	for _, route := range routes {
		threadID, _ := reloaded.getThread(route.bindingKey, route.workspaceRoot)
		if threadID != "thread-shared" {
			t.Fatalf("persisted route %q thread=%q", route.bindingKey, threadID)
		}
	}
}
