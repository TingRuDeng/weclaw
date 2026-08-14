package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

type failingPatchSender struct {
	fakeMessageSender
	patchErr error
}

func (s *failingPatchSender) PatchCard(context.Context, string, string) error {
	return s.patchErr
}

func TestDeferredCardResultFallsBackToMessageWhenPatchFails(t *testing.T) {
	sender := &failingPatchSender{patchErr: errors.New("patch unavailable")}
	base := NewReplier(sender, "oc_chat")
	reply := newDeferredCardResultReplier(base, sender, "om_card")
	if err := reply.SendText(context.Background(), "切换失败：目标会话不可用"); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 1 || !strings.Contains(sender.texts[0], "切换失败") {
		t.Fatalf("texts=%#v，原卡更新失败时必须保留单独消息兜底", sender.texts)
	}
}

func TestDeferredChoiceCardFallsBackToMessageWhenPatchFails(t *testing.T) {
	sender := &failingPatchSender{patchErr: errors.New("patch unavailable")}
	base := NewReplier(sender, "oc_chat")
	reply := newDeferredCardResultReplierWithTitle(
		base, sender, "om_card", "会话切换结果", "/cx cd @workspace-token",
	)
	patcher, ok := reply.(deferredChoiceCardPatcher)
	if !ok {
		t.Fatalf("reply=%T, want deferred choice card patcher", reply)
	}
	if err := patcher.patchChoiceCard(context.Background(), "请选择会话", []platform.Choice{
		{ID: "/cx switch thread-a", Label: "会话 A"},
	}, "feishu:oc_chat"); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 1 || !strings.Contains(sender.texts[0], "请选择会话") ||
		!strings.Contains(sender.texts[0], "会话 A") {
		t.Fatalf("texts=%#v，原卡更新失败时必须保留会话列表消息兜底", sender.texts)
	}
}

func TestDeferredSwitchResultUpdatesOriginalCardAsGreen(t *testing.T) {
	sender := &fakeMessageSender{}
	base := NewReplier(sender, "oc_chat")
	reply := newDeferredCardResultReplierWithTitle(
		base, sender, "om_card", "会话切换结果", "/cx switch thread-1",
	)

	if err := reply.SendText(context.Background(), "已切换并绑定。\n工作空间: weclaw"); err != nil {
		t.Fatal(err)
	}
	if len(sender.patchCards) != 1 {
		t.Fatalf("patchCards=%#v, want one original-card update", sender.patchCards)
	}
	var card map[string]any
	cardJSON := strings.TrimPrefix(sender.patchCards[0], "om_card:")
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatal(err)
	}
	header, _ := card["header"].(map[string]any)
	if header["template"] != "green" {
		t.Fatalf("header=%#v, want successful deferred switch in green", header)
	}
}

func TestDeferredSwitchResultReferenceRebuildsOriginalCardUpdate(t *testing.T) {
	sender := &fakeMessageSender{}
	base := NewReplier(sender, "oc_chat")
	reply := newDeferredCardResultReplierWithTitle(
		base, sender, "om_card", "会话切换结果", "/cx switch thread-1",
	)
	inline := newInlineCardReplier(reply, "feishu:oc_chat", "/cx switch thread-1")
	reporter, ok := any(inline).(platform.DurableCommandResultReferenceReporter)
	if !ok {
		t.Fatalf("reply=%T, want durable command result reference", inline)
	}
	reference, err := reporter.DurableCommandResultReference()
	if err != nil {
		t.Fatal(err)
	}
	if !reference.Valid() {
		t.Fatalf("reference=%#v, want valid original-card reference", reference)
	}

	rebuilt := NewReplier(sender, "oc_chat")
	deliverer, ok := any(rebuilt).(platform.DurableCommandResultReplier)
	if !ok {
		t.Fatalf("reply=%T, want durable command result replier", rebuilt)
	}
	if err := deliverer.DeliverCommandResult(
		context.Background(), reference,
		"已切换并绑定。\n工作空间: weclaw\n运行通道: 已恢复",
	); err != nil {
		t.Fatal(err)
	}
	if len(sender.patchCards) != 1 || !strings.HasPrefix(sender.patchCards[0], "om_card:") {
		t.Fatalf("patchCards=%#v, want recovered result to patch original card", sender.patchCards)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, recovered result must not create a new message", sender.texts)
	}
}
