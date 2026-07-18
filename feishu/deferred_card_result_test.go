package feishu

import (
	"context"
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
