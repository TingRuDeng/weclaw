package feishu

import "testing"

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
