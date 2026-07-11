package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func TestPendingGuideRunsAutomaticallyAfterPreviousTaskFails(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.err = errors.New("上一任务失败")
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)
	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-1"), "codex", "第一条", "client-1")
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), "codex", "第二条", "client-2")

	ag.release <- struct{}{}
	waitForAgentEnter(t, ag)
	started, _ := ag.stats()
	if started != 2 {
		t.Fatalf("上一任务失败后仍应自动执行暂存消息，started=%d", started)
	}
	ag.release <- struct{}{}
	key := h.agentExecutionKeyForRoute("user-1", "user-1", "codex", ag)
	waitUntil(t, func() bool {
		_, ok := h.activeTask(key)
		return !ok
	})
}
