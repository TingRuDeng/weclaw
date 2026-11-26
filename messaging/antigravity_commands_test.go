package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/antigravity"
	"github.com/fastclaw-ai/weclaw/platform"
)

func runAntigravityCommand(t *testing.T, h *Handler, client *antigravityFakeClient, msg platform.IncomingMessage, routeUserID, command string) *antigravityFakeReplier {
	t.Helper()
	reply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	h.handleAntigravityCommand(context.Background(), msg, routeUserID, reply, command)
	return reply
}

func TestAntigravityCommandRequiresFeishuPrivateChat(t *testing.T) {
	client := &antigravityFakeClient{}
	h := newAntigravityTestHandler(client)
	cases := []struct {
		name  string
		msg   platform.IncomingMessage
		route string
	}{
		{"feishu group", platform.IncomingMessage{Platform: platform.PlatformFeishu, AccountID: "bot", UserID: "user"}, "feishu:bot:tenant:group:chat"},
		{"wechat", platform.IncomingMessage{Platform: platform.PlatformWeChat, AccountID: "wx", UserID: "user"}, "wx:user"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reply := runAntigravityCommand(t, h, client, testCase.msg, testCase.route, "/ag ls")
			texts := reply.snapshotTexts()
			if len(texts) != 1 || !strings.Contains(texts[0], "仅支持飞书私聊") {
				t.Fatalf("texts = %q", texts)
			}
			if len(reply.snapshotChoices()) != 0 {
				t.Fatal("non-private chat must not receive choices")
			}
		})
	}

	privateMessage := antigravityTestMessage("feishu:bot:tenant:dm:chat:user")
	reply := runAntigravityCommand(t, h, client, privateMessage, "feishu:bot:tenant:dm:chat:user", "/ag ls")
	if texts := reply.snapshotTexts(); len(texts) != 1 || !strings.Contains(texts[0], "暂无") {
		t.Fatalf("private ls texts = %q", texts)
	}
}

func antigravityProjectsFixture() []antigravity.Conversation {
	return []antigravity.Conversation{
		{ID: "conv-a", Title: "Alpha one", ProjectID: "p1", ProjectName: "Project One", RunState: "RUN_STATE_IDLE"},
		{ID: "conv-b", Title: "Alpha two", ProjectID: "p1", ProjectName: "Project One", RunState: "RUN_STATE_IDLE"},
		{ID: "conv-c", Title: "Beta one", ProjectID: "p2", ProjectName: "Project Two", RunState: "RUN_STATE_IDLE"},
	}
}

func TestAntigravityListGroupsProjectsAndUsesSnapshot(t *testing.T) {
	client := &antigravityFakeClient{conversations: antigravityProjectsFixture(), baselines: map[string]antigravity.Baseline{
		"conv-a": {HasHistory: true, LatestReply: "old"},
		"conv-b": {HasHistory: true, LatestReply: "older"},
	}}
	h := newAntigravityTestHandler(client)
	defer h.StopAntigravityObservers()
	msg := antigravityTestMessage("feishu:bot:tenant:dm:chat:user")
	route := "feishu:bot:tenant:dm:chat:user"

	reply := runAntigravityCommand(t, h, client, msg, route, "/ag ls")
	choices := reply.snapshotChoices()
	if len(choices) != 1 {
		t.Fatalf("choices = %+v", choices)
	}
	projectIDs := make([]string, 0, len(choices[0].choices))
	for _, choice := range choices[0].choices {
		if choice.Route.Key != route || choice.Route.IsGroup {
			t.Fatalf("choice=%#v, want first-class private route %q", choice, route)
		}
		if _, duplicated := choice.Metadata[feishuSessionMetadataKey]; duplicated {
			t.Fatalf("choice=%#v, must not duplicate feishu_session_key into Metadata", choice)
		}
		projectIDs = append(projectIDs, choice.ID)
	}
	if strings.Join(projectIDs, ",") != "/ag project p1,/ag project p2" {
		t.Fatalf("project choices = %v", projectIDs)
	}
	if !strings.HasPrefix(choices[0].choices[0].Label, "1. Project One") {
		t.Fatalf("first project label = %q, want 1-based prefix", choices[0].choices[0].Label)
	}

	reply = runAntigravityCommand(t, h, client, msg, route, "/ag project p1")
	choices = reply.snapshotChoices()
	if len(choices) != 1 {
		t.Fatalf("project choices = %+v", choices)
	}
	sessionIDs := make([]string, 0, len(choices[0].choices))
	for _, choice := range choices[0].choices {
		sessionIDs = append(sessionIDs, choice.ID)
	}
	if strings.Join(sessionIDs, ",") != "/ag switch conv-a,/ag switch conv-b,/ag project .." {
		t.Fatalf("session choices = %v", sessionIDs)
	}
	if choices[0].choices[0].Label != "1. Alpha one" || choices[0].choices[1].Label != "2. Alpha two" {
		t.Fatalf("session labels = %q, %q", choices[0].choices[0].Label, choices[0].choices[1].Label)
	}

	reply = runAntigravityCommand(t, h, client, msg, route, "/ag project ..")
	choices = reply.snapshotChoices()
	if len(choices) != 1 || len(choices[0].choices) != 2 || choices[0].choices[0].ID != "/ag project p1" {
		t.Fatalf("back to projects = %+v", choices)
	}

	reply = runAntigravityCommand(t, h, client, msg, route, "/ag project p1")
	_ = reply
	reply = runAntigravityCommand(t, h, client, msg, route, "/ag 2")
	binding, ok := h.antigravityBindingSnapshot(msg.AccountID, route)
	if !ok || binding.conversationID != "conv-b" {
		t.Fatalf("binding = %+v ok=%v", binding, ok)
	}
	if texts := reply.snapshotTexts(); len(texts) == 0 || !strings.Contains(texts[len(texts)-1], "已切换") {
		t.Fatalf("switch texts = %q", texts)
	}

	h.feishuNavSnapshots.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	reply = runAntigravityCommand(t, h, client, msg, route, "/ag 1")
	if texts := reply.snapshotTexts(); len(texts) == 0 || !strings.Contains(texts[len(texts)-1], "已过期") {
		t.Fatalf("expired snapshot texts = %q", texts)
	}
}

func TestAntigravitySwitchFailureKeepsOriginalBinding(t *testing.T) {
	client := &antigravityFakeClient{conversations: antigravityProjectsFixture(), baselines: map[string]antigravity.Baseline{}}
	h := newAntigravityTestHandler(client)
	defer h.StopAntigravityObservers()
	msg := antigravityTestMessage("feishu:bot:tenant:dm:chat:user")
	route := "feishu:bot:tenant:dm:chat:user"
	runAntigravityCommand(t, h, client, msg, route, "/ag switch conv-a")
	before, ok := h.antigravityBindingSnapshot(msg.AccountID, route)
	if !ok || before.conversationID != "conv-a" {
		t.Fatalf("initial binding = %+v", before)
	}
	client.getErr = errors.New("upstream unavailable")
	reply := runAntigravityCommand(t, h, client, msg, route, "/ag switch conv-b")
	after, ok := h.antigravityBindingSnapshot(msg.AccountID, route)
	if !ok || after.conversationID != "conv-a" {
		t.Fatalf("binding after failure = %+v", after)
	}
	if texts := reply.snapshotTexts(); len(texts) == 0 || !strings.Contains(texts[len(texts)-1], "保留原绑定") {
		t.Fatalf("failure texts = %q", texts)
	}
}

func TestAntigravityNewSingleProject(t *testing.T) {
	client := &antigravityFakeClient{conversations: []antigravity.Conversation{{ID: "conv-a", ProjectID: "p1", ProjectName: "Project One"}}}
	h := newAntigravityTestHandler(client)
	defer h.StopAntigravityObservers()
	msg := antigravityTestMessage("feishu:bot:tenant:dm:chat:user")
	route := "feishu:bot:tenant:dm:chat:user"
	runAntigravityCommand(t, h, client, msg, route, "/ag new hello world")
	created := client.snapshotCreated()
	if len(created) != 1 || created[0].ProjectID != "p1" || created[0].Prompt != "hello world" {
		t.Fatalf("created = %+v", created)
	}
	binding, ok := h.antigravityBindingSnapshot(msg.AccountID, route)
	if !ok || binding.conversationID != "created-1" {
		t.Fatalf("binding = %+v", binding)
	}
}

func TestAntigravityNewMultipleProjectsDefersToProjectChoice(t *testing.T) {
	client := &antigravityFakeClient{conversations: antigravityProjectsFixture()}
	h := newAntigravityTestHandler(client)
	defer h.StopAntigravityObservers()
	msg := antigravityTestMessage("feishu:bot:tenant:dm:chat:user")
	route := "feishu:bot:tenant:dm:chat:user"
	reply := runAntigravityCommand(t, h, client, msg, route, "/ag new first word is body")
	if created := client.snapshotCreated(); len(created) != 0 {
		t.Fatalf("create must wait for project choice: %+v", created)
	}
	choices := reply.snapshotChoices()
	if len(choices) != 1 || len(choices[0].choices) != 2 {
		t.Fatalf("project choices = %+v", choices)
	}
	selected := choices[0].choices[0]
	if selected.Route.Key != route || selected.Route.IsGroup {
		t.Fatalf("selected choice=%#v, want first-class private route %q", selected, route)
	}
	if _, duplicated := selected.Metadata[feishuSessionMetadataKey]; duplicated {
		t.Fatalf("selected choice=%#v, must not duplicate feishu_session_key into Metadata", selected)
	}
	fields := strings.Fields(selected.ID)
	if len(fields) != 4 || fields[0] != "/ag" || fields[1] != "new-select" {
		t.Fatalf("choice id = %q", selected.ID)
	}
	runAntigravityCommand(t, h, client, msg, route, selected.ID)
	created := client.snapshotCreated()
	if len(created) != 1 || created[0].ProjectID != "p1" || created[0].Prompt != "first word is body" {
		t.Fatalf("created = %+v", created)
	}
}

func TestAntigravityNewExplicitProjectWins(t *testing.T) {
	client := &antigravityFakeClient{conversations: antigravityProjectsFixture()}
	h := newAntigravityTestHandler(client)
	defer h.StopAntigravityObservers()
	msg := antigravityTestMessage("feishu:bot:tenant:dm:chat:user")
	route := "feishu:bot:tenant:dm:chat:user"
	runAntigravityCommand(t, h, client, msg, route, "/ag new p2 explicit body")
	created := client.snapshotCreated()
	if len(created) != 1 || created[0].ProjectID != "p2" || created[0].Prompt != "explicit body" {
		t.Fatalf("created = %+v", created)
	}
}

func TestAntigravityReleaseDetachesProgressAndMutes(t *testing.T) {
	client := &antigravityFakeClient{conversations: antigravityProjectsFixture(), baselines: map[string]antigravity.Baseline{}}
	started := make(chan struct{})
	var once bool
	client.observeFn = func(conversationID, fileID string, offset int64) (antigravity.ObserveResult, error) {
		if !once {
			once = true
			close(started)
		}
		return antigravity.ObserveResult{
			Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_RUNNING"},
			Events:       []antigravity.Event{{Kind: antigravity.EventProgress, Text: "desktop progress", Offset: offset}},
			NextFileID:   "file-1", NextOffset: offset + 10,
		}, nil
	}
	h := newAntigravityTestHandler(client)
	msg := antigravityTestMessage("feishu:bot:tenant:dm:chat:user")
	route := "feishu:bot:tenant:dm:chat:user"
	reply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	h.installAntigravityBinding(msg, route, reply, client.conversations[0], antigravity.Baseline{})
	select {
	case <-reply.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not open a progress card")
	}
	runAntigravityCommand(t, h, client, msg, route, "/ag release")
	if h.hasAntigravityBinding(msg.AccountID, route) {
		t.Fatal("release must clear the binding")
	}
	waitForAntigravity(t, func() bool {
		_, _, _, detached, _ := reply.stream.snapshot()
		return detached != ""
	})
	before := len(reply.snapshotTexts())
	time.Sleep(150 * time.Millisecond)
	if after := len(reply.snapshotTexts()); after != before {
		t.Fatalf("released window received more messages: %d -> %d", before, after)
	}
}

func TestAntigravityPlainTextFailClosed(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	reply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})

	t.Run("idle with history forwards once", func(t *testing.T) {
		client := &antigravityFakeClient{
			conversations: []antigravity.Conversation{{ID: "conv-a", RunState: "RUN_STATE_IDLE"}},
			baselines:     map[string]antigravity.Baseline{"conv-a": {HasHistory: true}},
		}
		h := newAntigravityTestHandler(client)
		defer h.StopAntigravityObservers()
		h.installAntigravityBinding(msg, route, reply, client.conversations[0], antigravity.Baseline{HasHistory: true})
		h.dispatchParsedAgentMessage(platformMessageRuntime{
			ctx: context.Background(), msg: msg, reply: reply, routeUserID: route, text: "hello",
		})
		sent := client.snapshotSent()
		if len(sent) != 1 || sent[0] != "conv-a\x00[WeClaw/飞书] hello" {
			t.Fatalf("sent = %q", sent)
		}
	})

	t.Run("running and cancelling do not send", func(t *testing.T) {
		for _, state := range []string{"RUN_STATE_RUNNING", "RUN_STATE_CANCELLING"} {
			client := &antigravityFakeClient{
				conversations: []antigravity.Conversation{{ID: "conv-a", RunState: state}},
				baselines:     map[string]antigravity.Baseline{"conv-a": {HasHistory: true}},
			}
			h := newAntigravityTestHandler(client)
			h.installAntigravityBinding(msg, route, reply, client.conversations[0], antigravity.Baseline{HasHistory: true})
			h.dispatchParsedAgentMessage(platformMessageRuntime{
				ctx: context.Background(), msg: msg, reply: reply, routeUserID: route, text: "hello",
			})
			if sent := client.snapshotSent(); len(sent) != 0 {
				t.Fatalf("%s sent = %q", state, sent)
			}
			h.StopAntigravityObservers()
		}
	})

	t.Run("empty session does not send", func(t *testing.T) {
		client := &antigravityFakeClient{conversations: []antigravity.Conversation{{ID: "conv-a", RunState: "RUN_STATE_IDLE"}}}
		h := newAntigravityTestHandler(client)
		h.installAntigravityBinding(msg, route, reply, client.conversations[0], antigravity.Baseline{})
		h.dispatchParsedAgentMessage(platformMessageRuntime{
			ctx: context.Background(), msg: msg, reply: reply, routeUserID: route, text: "hello",
		})
		if sent := client.snapshotSent(); len(sent) != 0 {
			t.Fatalf("sent = %q", sent)
		}
		texts := reply.snapshotTexts()
		if len(texts) == 0 || !strings.Contains(texts[len(texts)-1], "还没有历史轮次") {
			t.Fatalf("texts = %q", texts)
		}
		h.StopAntigravityObservers()
	})

	t.Run("send failure is consumed without retry", func(t *testing.T) {
		client := &antigravityFakeClient{
			conversations: []antigravity.Conversation{{ID: "conv-a", RunState: "RUN_STATE_IDLE"}},
			baselines:     map[string]antigravity.Baseline{"conv-a": {HasHistory: true}},
			sendErr:       errors.New("transport lost"),
		}
		h := newAntigravityTestHandler(client)
		h.installAntigravityBinding(msg, route, reply, client.conversations[0], antigravity.Baseline{HasHistory: true})
		h.dispatchParsedAgentMessage(platformMessageRuntime{
			ctx: context.Background(), msg: msg, reply: reply, routeUserID: route, text: "hello",
		})
		texts := reply.snapshotTexts()
		if len(texts) == 0 || !strings.Contains(texts[len(texts)-1], "提交结果待核对") {
			t.Fatalf("texts = %q", texts)
		}
		if len(client.sent) != 0 {
			t.Fatalf("failed send must not be recorded as delivered: %q", client.sent)
		}
		h.StopAntigravityObservers()
	})
}

func TestAntigravityKnownAgentCommandsClearBinding(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	for _, command := range []string{"/codex hello", "@cx hello", "/cc hello", "/cx switch 1"} {
		t.Run(command, func(t *testing.T) {
			client := &antigravityFakeClient{conversations: antigravityProjectsFixture()}
			h := newAntigravityTestHandler(client)
			h.agentMetas = []AgentMeta{{Name: "codex"}, {Name: "claude"}}
			reply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
			h.installAntigravityBinding(msg, route, reply, client.conversations[0], antigravity.Baseline{})
			if command == "/cx switch 1" {
				h.handleBuiltInPlatformCommand(context.Background(), platformCommandRequest{
					Message: msg, RouteUserID: route, Reply: reply, Trimmed: command,
				})
			} else {
				h.dispatchParsedAgentMessage(platformMessageRuntime{
					ctx: context.Background(), msg: msg, reply: reply, routeUserID: route, text: command,
				})
			}
			if h.hasAntigravityBinding(msg.AccountID, route) {
				t.Fatalf("%s must clear the Antigravity binding", command)
			}
			h.StopAntigravityObservers()
		})
	}
}
