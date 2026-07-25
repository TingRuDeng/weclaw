package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
