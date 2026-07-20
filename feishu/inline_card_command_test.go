package feishu

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestClaudeQuotaIsInlineCardCommand(t *testing.T) {
	if !isInlineCardCommand("/cc quota") {
		t.Fatal("/cc quota should execute directly from an inline help card")
	}
}

func TestCodexAccountCardsAreInlineAndConfirmationIsDeferred(t *testing.T) {
	if !isInlineCardCommand("/cx account") || !isInlineCardCommand("/cx account select profile 7") {
		t.Fatal("Codex 账号列表和选择必须在原卡内处理")
	}
	if isDeferredCardResultCommand("/cx account select profile 7") {
		t.Fatal("账号选择只生成确认卡，不应进入长任务模式")
	}
	if !isDeferredCardResultCommand("/cx account confirm @acct_token") {
		t.Fatal("账号确认必须把最终结果回写原卡")
	}
	if got := deferredCardResultTitle("/cx account confirm @acct_token"); got != "Codex 账号切换结果" {
		t.Fatalf("title=%q", got)
	}
}

func TestInlineCardProgressReplierKeepsCommandResultInline(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardID: "card-progress"}
	base := newReplierWithTaskCards(sender, "oc_chat", cardKit, newTaskCardRegistry())
	deferred := newDeferredCardResultReplier(base, sender, "om_navigation")
	reply := newInlineCardReplier(deferred, "feishu:oc_chat")

	progress := reply.ProgressReplier()
	provider, ok := progress.(platform.ProgressReplierProvider)
	if !ok || provider.ProgressReplier() != base {
		t.Fatalf("deferred wrapper must expose the durable base replier: %#v", progress)
	}
	progress = provider.ProgressReplier()
	if _, err := progress.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex · project-a", InitialContent: "最新进展",
	}); err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if err := reply.SendText(context.Background(), "已切换并绑定。"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if card := reply.finish(); card == nil {
		t.Fatal("switch result should still be captured for the original navigation card")
	}
	if len(sender.cards) != 1 || len(sender.texts) != 0 {
		t.Fatalf("cards=%#v texts=%#v", sender.cards, sender.texts)
	}
}
