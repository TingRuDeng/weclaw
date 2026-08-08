package messaging

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuQueuedCodexMessageUsesBoundTaskControlCard(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), "task-key", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex",
	})
	if !started || !h.storePendingGuide("task-key", pendingAgentTask{message: "补充要求"}) {
		t.Fatal("failed to prepare active task with pending message")
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.replyAgentTaskAdmission(agentTaskAdmissionNotice{
		ctx: context.Background(), platformName: platform.PlatformFeishu, accountID: "app-1",
		reply: reply, userID: "user-1", routeUserID: "route-1",
		agentName: "codex", executionKey: "task-key", task: task, guideSupported: true,
	}, activeTaskQueued)

	if len(reply.Texts) != 0 || len(reply.Choices) != 1 {
		t.Fatalf("texts=%#v choices=%#v, want one contextual card", reply.Texts, reply.Choices)
	}
	card := reply.Choices[0]
	if !strings.Contains(card.Prompt, "补充要求") || len(card.Choices) != 3 {
		t.Fatalf("card=%#v, want pending preview and three controls", card)
	}
	if !strings.Contains(card.Prompt, "无需操作") ||
		!strings.Contains(card.Prompt, "当前任务结束后会自动执行") ||
		!strings.Contains(card.Prompt, "如需改变默认处理方式") ||
		strings.Contains(card.Prompt, "请选择如何处理") {
		t.Fatalf("prompt=%q, want automatic execution presented as the default instead of a required choice", card.Prompt)
	}
	wantIDs := []string{"/guide", "/cancel", "/stop"}
	token := ""
	for index, choice := range card.Choices {
		if choice.ID != wantIDs[index] {
			t.Fatalf("choice[%d]=%#v, want %q", index, choice, wantIDs[index])
		}
		if choice.Metadata[platform.ChoiceMetadataInteractionKind] != platform.ChoiceInteractionTaskControl {
			t.Fatalf("choice[%d] kind=%q", index, choice.Metadata[platform.ChoiceMetadataInteractionKind])
		}
		current := choice.Metadata[platform.ChoiceMetadataTaskControlToken]
		if !strings.HasPrefix(current, pendingTaskControlTokenPrefix) {
			t.Fatalf("choice[%d] token=%q", index, current)
		}
		if token != "" && current != token {
			t.Fatalf("choice tokens differ: %q != %q", current, token)
		}
		token = current
	}
}

func TestPendingTaskControlOldCardCannotClearReplacementMessage(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["codex"] = &fakeAgent{info: agent.AgentInfo{Name: "codex"}}
	task, _, started := h.beginActiveTask(context.Background(), "task-key", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex",
	})
	if !started || !h.storePendingGuide("task-key", pendingAgentTask{message: "相同内容"}) {
		t.Fatal("failed to prepare first pending message")
	}
	cardReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.replyAgentTaskAdmission(agentTaskAdmissionNotice{
		ctx: context.Background(), platformName: platform.PlatformFeishu, accountID: "app-1",
		reply: cardReply, userID: "user-1", routeUserID: "route-1",
		agentName: "codex", executionKey: "task-key", task: task, guideSupported: true,
	}, activeTaskQueued)
	token := cardReply.Choices[0].Choices[0].Metadata[platform.ChoiceMetadataTaskControlToken]

	firstReply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), pendingTaskControlMessage("event-1", "user-1", "route-1", "app-1", "/cancel", token), firstReply)
	if task.pendingGuide() != "" || !containsText(firstReply.Texts, "已撤回") {
		t.Fatalf("pending=%q texts=%#v, first card action should clear pending", task.pendingGuide(), firstReply.Texts)
	}
	if !h.storePendingGuide("task-key", pendingAgentTask{message: "相同内容"}) {
		t.Fatal("failed to prepare replacement pending message")
	}

	staleReply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), pendingTaskControlMessage("event-2", "user-1", "route-1", "app-1", "/cancel", token), staleReply)
	if task.pendingGuide() != "相同内容" {
		t.Fatalf("old card cleared replacement pending message: %q", task.pendingGuide())
	}
	if !containsText(staleReply.Texts, "已处理") && !containsText(staleReply.Texts, "已经过期") {
		t.Fatalf("texts=%#v, want explicit stale-card result", staleReply.Texts)
	}
}

func TestClaudePendingTaskControlCardDoesNotOfferGuide(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), "task-key", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "claude",
	})
	if !started || !h.storePendingGuide("task-key", pendingAgentTask{message: "下一条"}) {
		t.Fatal("failed to prepare pending Claude message")
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.replyAgentTaskAdmission(agentTaskAdmissionNotice{
		ctx: context.Background(), platformName: platform.PlatformFeishu, accountID: "app-1",
		reply: reply, userID: "user-1", routeUserID: "route-1",
		agentName: "claude", executionKey: "task-key", task: task,
	}, activeTaskQueued)

	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("choices=%#v, want cancel and stop only", reply.Choices)
	}
	for _, choice := range reply.Choices[0].Choices {
		if choice.ID == "/guide" {
			t.Fatalf("Claude card must not advertise /guide: %#v", reply.Choices[0].Choices)
		}
	}
}

func TestPendingTaskControlRejectsMismatchedScopeAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	h := NewHandler(nil, nil)
	h.pendingTaskControls.now = func() time.Time { return now }
	h.agents["codex"] = &fakeAgent{info: agent.AgentInfo{Name: "codex"}}
	task, _, started := h.beginActiveTask(context.Background(), "task-key", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex",
	})
	if !started || !h.storePendingGuide("task-key", pendingAgentTask{message: "受保护消息"}) {
		t.Fatal("failed to prepare pending message")
	}
	cardReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.replyAgentTaskAdmission(agentTaskAdmissionNotice{
		ctx: context.Background(), platformName: platform.PlatformFeishu, accountID: "app-1",
		reply: cardReply, userID: "user-1", routeUserID: "route-1",
		agentName: "codex", executionKey: "task-key", task: task, guideSupported: true,
	}, activeTaskQueued)
	token := cardReply.Choices[0].Choices[0].Metadata[platform.ChoiceMetadataTaskControlToken]

	tests := []struct {
		name      string
		userID    string
		routeID   string
		accountID string
	}{
		{name: "wrong account", userID: "user-1", routeID: "route-1", accountID: "app-2"},
		{name: "wrong actor", userID: "user-2", routeID: "route-1", accountID: "app-1"},
		{name: "wrong route", userID: "user-1", routeID: "route-2", accountID: "app-1"},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := platformtest.NewReplier(platform.Capabilities{Text: true})
			h.HandleMessage(
				context.Background(),
				pendingTaskControlMessage("scope-event-"+string(rune('a'+index)), tt.userID, tt.routeID, tt.accountID, "/cancel", token),
				reply,
			)
			if !containsText(reply.Texts, "已经过期") && !containsText(reply.Texts, "已处理") {
				t.Fatalf("texts=%#v, want rejected scope", reply.Texts)
			}
			if task.pendingGuide() != "受保护消息" {
				t.Fatalf("scope mismatch changed pending message: %q", task.pendingGuide())
			}
		})
	}

	now = now.Add(pendingTaskControlTTL + time.Second)
	expiredReply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(
		context.Background(),
		pendingTaskControlMessage("expired-event", "user-1", "route-1", "app-1", "/cancel", token),
		expiredReply,
	)
	if !containsText(expiredReply.Texts, "已经过期") || task.pendingGuide() != "受保护消息" {
		t.Fatalf("texts=%#v pending=%q, expired token must not mutate task", expiredReply.Texts, task.pendingGuide())
	}
}

func TestPendingTaskControlConcurrentClicksMutateOnce(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["codex"] = &fakeAgent{info: agent.AgentInfo{Name: "codex"}}
	task, _, started := h.beginActiveTask(context.Background(), "task-key", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex",
	})
	if !started || !h.storePendingGuide("task-key", pendingAgentTask{message: "只撤回一次"}) {
		t.Fatal("failed to prepare pending message")
	}
	cardReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.replyAgentTaskAdmission(agentTaskAdmissionNotice{
		ctx: context.Background(), platformName: platform.PlatformFeishu, accountID: "app-1",
		reply: cardReply, userID: "user-1", routeUserID: "route-1",
		agentName: "codex", executionKey: "task-key", task: task, guideSupported: true,
	}, activeTaskQueued)
	token := cardReply.Choices[0].Choices[0].Metadata[platform.ChoiceMetadataTaskControlToken]

	replies := []*platformtest.Replier{
		platformtest.NewReplier(platform.Capabilities{Text: true}),
		platformtest.NewReplier(platform.Capabilities{Text: true}),
	}
	var wg sync.WaitGroup
	for index := range replies {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			h.HandleMessage(
				context.Background(),
				pendingTaskControlMessage("concurrent-event-"+string(rune('a'+index)), "user-1", "route-1", "app-1", "/cancel", token),
				replies[index],
			)
		}(index)
	}
	wg.Wait()

	successes := 0
	for _, reply := range replies {
		if containsText(reply.Texts, "已撤回") {
			successes++
		}
	}
	if successes != 1 || task.pendingGuide() != "" {
		t.Fatalf("successes=%d pending=%q replies=%#v", successes, task.pendingGuide(), replies)
	}
}

func issuePendingGuideControl(t *testing.T, fixture liveGuideRelayFixture, message string) string {
	t.Helper()
	if !fixture.h.storePendingGuide(fixture.route.conversationID, pendingAgentTask{message: message, run: func() {}}) {
		t.Fatal("failed to store pending guide")
	}
	cardReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	fixture.h.replyAgentTaskAdmission(agentTaskAdmissionNotice{
		ctx: context.Background(), platformName: platform.PlatformFeishu, accountID: "app-1",
		reply: cardReply, userID: fixture.opts.userID, routeUserID: fixture.opts.routeUserID,
		agentName: "codex", executionKey: fixture.route.conversationID,
		task: fixture.task, guideSupported: true,
	}, activeTaskQueued)
	if len(cardReply.Choices) != 1 || len(cardReply.Choices[0].Choices) == 0 {
		t.Fatalf("control card=%#v", cardReply.Choices)
	}
	return cardReply.Choices[0].Choices[0].Metadata[platform.ChoiceMetadataTaskControlToken]
}

func TestPendingTaskGuideControlRevisionSteersAndReanchorsOnce(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, true)
	token := issuePendingGuideControl(t, fixture, "只发送一次")
	replies := []*guideRelayTestReplier{
		newGuideRelayTestReplier("card-control-a"),
		newGuideRelayTestReplier("card-control-b"),
	}
	var wg sync.WaitGroup
	for index := range replies {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			fixture.h.HandleMessage(
				context.Background(),
				pendingTaskControlMessage("guide-event-"+string(rune('a'+index)), fixture.opts.userID, fixture.opts.routeUserID, "app-1", "/guide", token),
				replies[index],
			)
		}(index)
	}
	wg.Wait()

	if calls := fixture.agent.guideSnapshot(); len(calls) != 1 || calls[0].message != "只发送一次" {
		t.Fatalf("steer calls=%#v", calls)
	}
	opened := 0
	for _, reply := range replies {
		opened += reply.openAttempts
	}
	if opened != 1 || fixture.task.pendingGuide() != "" || fixture.oldReply.stream.supersededCount() != 1 {
		t.Fatalf("opened=%d pending=%q superseded=%d", opened, fixture.task.pendingGuide(), fixture.oldReply.stream.supersededCount())
	}
}

func TestPendingTaskGuideSteerFailureDoesNotCreateRelayCard(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, true)
	token := issuePendingGuideControl(t, fixture, "会失败的引导")
	fixture.agent.fakeCodexThreadAgent.steerErr = errors.New("steer denied")
	reply := newGuideRelayTestReplier("card-never-created")
	fixture.h.HandleMessage(
		context.Background(),
		pendingTaskControlMessage("guide-steer-failed", fixture.opts.userID, fixture.opts.routeUserID, "app-1", "/guide", token),
		reply,
	)

	if reply.openAttempts != 0 || fixture.oldReply.stream.supersededCount() != 0 || fixture.task.pendingGuide() != "会失败的引导" {
		t.Fatalf("open=%d superseded=%d pending=%q", reply.openAttempts, fixture.oldReply.stream.supersededCount(), fixture.task.pendingGuide())
	}
	if text := strings.Join(reply.textsSnapshot(), "\n"); !strings.Contains(text, "steer denied") {
		t.Fatalf("failure reply=%q", text)
	}
}

func TestPendingTaskGuideReanchorFailureDoesNotRepeatSteer(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, true)
	token := issuePendingGuideControl(t, fixture, "已送达但迁卡失败")
	reply := newGuideRelayTestReplier("card-create-failed")
	reply.openErr = errors.New("card create rejected")
	fixture.h.HandleMessage(
		context.Background(),
		pendingTaskControlMessage("guide-reanchor-failed", fixture.opts.userID, fixture.opts.routeUserID, "app-1", "/guide", token),
		reply,
	)
	if text := strings.Join(reply.textsSnapshot(), "\n"); !strings.Contains(text, "引导已送达，但任务卡迁移失败") {
		t.Fatalf("warning=%q", text)
	}
	if fixture.task.pendingGuide() != "" {
		t.Fatalf("delivered guide was restored: %q", fixture.task.pendingGuide())
	}

	staleReply := newGuideRelayTestReplier("card-stale")
	fixture.h.HandleMessage(
		context.Background(),
		pendingTaskControlMessage("guide-reanchor-retry", fixture.opts.userID, fixture.opts.routeUserID, "app-1", "/guide", token),
		staleReply,
	)
	if calls := fixture.agent.guideSnapshot(); len(calls) != 1 {
		t.Fatalf("steer repeated after reanchor failure: %#v", calls)
	}
	if staleReply.openAttempts != 0 || (!containsText(staleReply.textsSnapshot(), "已处理") && !containsText(staleReply.textsSnapshot(), "已经过期")) {
		t.Fatalf("stale open=%d texts=%#v", staleReply.openAttempts, staleReply.textsSnapshot())
	}
}

func pendingTaskControlMessage(messageID, userID, routeUserID, accountID, choice, token string) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: accountID, UserID: userID,
		Route: platform.SessionRoute{Key: routeUserID}, MessageID: messageID,
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{
			"choice":                                choice,
			platform.ChoiceMetadataInteractionKind:  platform.ChoiceInteractionTaskControl,
			platform.ChoiceMetadataTaskControlToken: token,
		}},
	}
}
