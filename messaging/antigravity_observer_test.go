package messaging

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/internal/antigravity"
	"github.com/fastclaw-ai/weclaw/platform"
)

type antigravityFakePlatform struct {
	name    platform.PlatformName
	account string
	mu      sync.Mutex
	access  platform.AccessControl
	reply   platform.Replier
}

func (p *antigravityFakePlatform) Name() platform.PlatformName { return p.name }
func (p *antigravityFakePlatform) AccountID() string           { return p.account }
func (p *antigravityFakePlatform) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Buttons: true, Streaming: true}
}
func (p *antigravityFakePlatform) Run(context.Context, platform.DispatchFunc) error { return nil }
func (p *antigravityFakePlatform) SetAccessControl(access platform.AccessControl) {
	p.mu.Lock()
	p.access = access
	p.mu.Unlock()
}
func (p *antigravityFakePlatform) NewReplierForRoute(platform.DeliveryRoute) platform.Replier {
	return p.reply
}

func TestAntigravityObserverSyncsDesktopTurn(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	reply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	conversation := antigravity.Conversation{ID: "conv-a", Title: "Alpha one", ProjectID: "p1", ProjectName: "Project One", RunState: "RUN_STATE_RUNNING"}
	client := &antigravityFakeClient{}
	calls := 0
	client.observeFn = func(conversationID, fileID string, offset int64) (antigravity.ObserveResult, error) {
		calls++
		switch calls {
		case 1:
			return antigravity.ObserveResult{
				Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_RUNNING"},
				Events: []antigravity.Event{
					{Kind: antigravity.EventTurnInput, Text: "desktop question", Offset: 0},
					{Kind: antigravity.EventProgress, Text: "desktop progress", Offset: 1},
				},
				NextFileID: "f1", NextOffset: 10,
			}, nil
		case 2:
			return antigravity.ObserveResult{
				Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"},
				Events:       []antigravity.Event{{Kind: antigravity.EventReply, Text: "desktop answer", Offset: 11}},
				NextFileID:   "f1", NextOffset: 20,
			}, nil
		default:
			return antigravity.ObserveResult{
				Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"},
				NextFileID:   "f1", NextOffset: 20,
			}, nil
		}
	}
	h := newAntigravityTestHandler(client)
	defer h.StopAntigravityObservers()
	h.installAntigravityBinding(msg, route, reply, conversation, antigravity.Baseline{})
	waitForAntigravity(t, func() bool { return len(reply.snapshotResults()) == 1 })
	result := reply.snapshotResults()[0]
	if result.text != "desktop answer" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.title, "Antigravity") || !strings.Contains(result.title, "Project One") || !strings.Contains(result.title, "Alpha one") {
		t.Fatalf("result title = %q", result.title)
	}
	if !strings.Contains(result.key, ":conv-a:0") {
		t.Fatalf("delivery key = %q", result.key)
	}
	updates, completed, _, _, _ := reply.stream.snapshot()
	if len(updates) == 0 || !strings.Contains(strings.Join(updates, "\n"), "desktop progress") {
		t.Fatalf("stream updates = %q", updates)
	}
	if completed == "" {
		t.Fatal("progress card must complete")
	}
}

func TestAntigravityObserverSyncsFeishuTurn(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	reply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	conversation := antigravity.Conversation{ID: "conv-a", Title: "Alpha one", ProjectID: "p1", ProjectName: "Project One", RunState: "RUN_STATE_RUNNING"}
	client := &antigravityFakeClient{}
	calls := 0
	client.observeFn = func(conversationID, fileID string, offset int64) (antigravity.ObserveResult, error) {
		calls++
		switch calls {
		case 1:
			return antigravity.ObserveResult{
				Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_RUNNING"},
				Events: []antigravity.Event{
					{Kind: antigravity.EventTurnInput, Text: antigravityFeishuPrefix + "from feishu", FromFeishu: true, Offset: 0},
					{Kind: antigravity.EventProgress, Text: "feishu progress", Offset: 1},
				},
				NextFileID: "f1", NextOffset: 10,
			}, nil
		case 2:
			return antigravity.ObserveResult{
				Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"},
				Events:       []antigravity.Event{{Kind: antigravity.EventReply, Text: "feishu answer", Offset: 11}},
				NextFileID:   "f1", NextOffset: 20,
			}, nil
		default:
			return antigravity.ObserveResult{Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"}, NextFileID: "f1", NextOffset: 20}, nil
		}
	}
	h := newAntigravityTestHandler(client)
	defer h.StopAntigravityObservers()
	h.installAntigravityBinding(msg, route, reply, conversation, antigravity.Baseline{})
	waitForAntigravity(t, func() bool { return len(reply.snapshotResults()) == 1 })
	if result := reply.snapshotResults()[0]; result.text != "feishu answer" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAntigravityObserverAuthorizationRevocationAndUnionID(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	msg.UserAliases = []string{"union_1"}
	baseReply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	fakePlatform := &antigravityFakePlatform{
		name: platform.PlatformFeishu, account: msg.AccountID, reply: baseReply,
		access: platform.NewAccessControl([]string{"union_1"}),
	}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: fakePlatform, Access: platform.NewAccessControl([]string{"union_1"}),
	}})
	authorized, ok := registry.AuthorizeIncomingMessage(msg)
	if !ok {
		t.Fatal("message must authorize through union_id")
	}
	if identity, ok := authorized.AuthorizedIdentity(); !ok || identity != "union_1" {
		t.Fatalf("authorized identity = %q ok=%v", identity, ok)
	}
	client := &antigravityFakeClient{}
	h := newAntigravityTestHandler(client)
	h.SetPlatformRegistry(registry)
	defer h.StopAntigravityObservers()
	conversation := antigravity.Conversation{ID: "conv-a", Title: "Alpha", ProjectName: "Project One"}
	h.installAntigravityBinding(authorized, route, baseReply, conversation, antigravity.Baseline{})
	binding, ok := h.antigravityBindingSnapshot(msg.AccountID, route)
	if !ok {
		t.Fatal("binding missing")
	}
	binding.observer.deliverTurn("first answer", 0)
	if results := baseReply.snapshotResults(); len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	binding.observer.deliverTurn("first answer duplicate", 0)
	if results := baseReply.snapshotResults(); len(results) != 1 {
		t.Fatalf("duplicate cursor must be idempotent: %+v", results)
	}
	registry.UpdateAccessForAccount(platform.PlatformFeishu, msg.AccountID, []string{"someone-else"})
	binding.observer.deliverTurn("second answer", 10)
	if results := baseReply.snapshotResults(); len(results) != 1 {
		t.Fatalf("revoked identity must stop delivery: %+v", results)
	}
	binding.observer.updateStream("should not open")
	if _, _, _, _, _ = baseReply.stream.snapshot(); len(baseReply.snapshotResults()) != 1 {
		t.Fatal("revoked identity must not receive progress")
	}
}

func TestAntigravityObserverReportsUnconfirmedIdleAndDiscontinuity(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	for _, testCase := range []struct {
		name   string
		script func(call int, conversationID string, offset int64) (antigravity.ObserveResult, error)
	}{
		{
			name: "idle without final reply",
			script: func(call int, conversationID string, offset int64) (antigravity.ObserveResult, error) {
				if call == 1 {
					return antigravity.ObserveResult{
						Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_RUNNING"},
						Events:       []antigravity.Event{{Kind: antigravity.EventProgress, Text: "work", Offset: offset}},
						NextFileID:   "f1", NextOffset: offset + 1,
					}, nil
				}
				return antigravity.ObserveResult{
					Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"},
					NextFileID:   "f1", NextOffset: offset + 1,
				}, nil
			},
		},
		{
			name: "cancelling then idle",
			script: func(call int, conversationID string, offset int64) (antigravity.ObserveResult, error) {
				switch call {
				case 1:
					return antigravity.ObserveResult{
						Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_RUNNING"},
						Events:       []antigravity.Event{{Kind: antigravity.EventProgress, Text: "work", Offset: offset}},
						NextFileID:   "f1", NextOffset: offset + 1,
					}, nil
				case 2:
					return antigravity.ObserveResult{
						Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_CANCELLING"},
						NextFileID:   "f1", NextOffset: offset + 1,
					}, nil
				default:
					return antigravity.ObserveResult{
						Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"},
						NextFileID:   "f1", NextOffset: offset + 1,
					}, nil
				}
			},
		},
		{
			name: "discontinuous transcript",
			script: func(call int, conversationID string, offset int64) (antigravity.ObserveResult, error) {
				if call == 1 {
					return antigravity.ObserveResult{
						Conversation:  antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"},
						Discontinuous: true, NextFileID: "f2", NextOffset: 0,
					}, nil
				}
				return antigravity.ObserveResult{
					Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"},
					NextFileID:   "f2", NextOffset: 0,
				}, nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
			client := &antigravityFakeClient{}
			call := 0
			client.observeFn = func(conversationID, fileID string, offset int64) (antigravity.ObserveResult, error) {
				call++
				return testCase.script(call, conversationID, offset)
			}
			h := newAntigravityTestHandler(client)
			defer h.StopAntigravityObservers()
			h.installAntigravityBinding(msg, route, reply, antigravity.Conversation{ID: "conv-a", Title: "Alpha"}, antigravity.Baseline{})
			waitForAntigravity(t, func() bool {
				_, _, failed, _, _ := reply.stream.snapshot()
				if strings.Contains(failed, "本轮结果未确认") {
					return true
				}
				for _, text := range reply.snapshotTexts() {
					if strings.Contains(text, "本轮结果未确认") {
						return true
					}
				}
				return false
			})
			if results := reply.snapshotResults(); len(results) != 0 {
				t.Fatalf("unconfirmed turn must not produce a result: %+v", results)
			}
		})
	}
}

func TestAntigravityObserverAttributesConsecutiveTurns(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	reply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	client := &antigravityFakeClient{}
	calls := 0
	client.observeFn = func(conversationID, fileID string, offset int64) (antigravity.ObserveResult, error) {
		calls++
		if calls == 1 {
			return antigravity.ObserveResult{
				Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"},
				Events: []antigravity.Event{
					{Kind: antigravity.EventTurnInput, Text: "first", Offset: 0},
					{Kind: antigravity.EventReply, Text: "answer one", Offset: 1},
					{Kind: antigravity.EventTurnInput, Text: "second", Offset: 2},
					{Kind: antigravity.EventReply, Text: "answer two", Offset: 3},
				},
				NextFileID: "f1", NextOffset: 4,
			}, nil
		}
		return antigravity.ObserveResult{Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"}, NextFileID: "f1", NextOffset: 4}, nil
	}
	h := newAntigravityTestHandler(client)
	defer h.StopAntigravityObservers()
	h.installAntigravityBinding(msg, route, reply, antigravity.Conversation{ID: "conv-a", Title: "Alpha"}, antigravity.Baseline{})
	waitForAntigravity(t, func() bool { return len(reply.snapshotResults()) == 2 })
	results := reply.snapshotResults()
	if results[0].text != "answer one" || results[1].text != "answer two" {
		t.Fatalf("results = %+v", results)
	}
	if results[0].key == results[1].key {
		t.Fatalf("consecutive turns must use distinct delivery keys: %+v", results)
	}
}
