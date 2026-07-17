package messaging

import (
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestFeishuWorkspaceChoiceTokenIsOpaqueBoundAndOneShot(t *testing.T) {
	store := feishuWorkspaceChoiceStore{}
	workspaceRoot := "/private/workspaces/secret-project"
	token := store.issue(feishuWorkspaceChoiceCodex, "user-a", "route-a", workspaceRoot)
	if !isFeishuWorkspaceChoiceToken(token) || strings.Contains(token, workspaceRoot) {
		t.Fatalf("token=%q, want opaque workspace token", token)
	}
	for _, invalid := range []string{"@ws_", "@ws_token", "@ws_" + strings.Repeat("a", 31), "@ws_" + strings.Repeat("g", 32)} {
		if isFeishuWorkspaceChoiceToken(invalid) {
			t.Fatalf("invalid token %q must not shadow a workspace name", invalid)
		}
	}
	if _, ok := store.consume(token, feishuWorkspaceChoiceCodex, "user-b", "route-a"); ok {
		t.Fatal("different actor must not consume workspace token")
	}
	if _, ok := store.consume(token, feishuWorkspaceChoiceCodex, "user-a", "route-b"); ok {
		t.Fatal("different binding must not consume workspace token")
	}
	if _, ok := store.consume(token, feishuWorkspaceChoiceClaude, "user-a", "route-a"); ok {
		t.Fatal("different agent kind must not consume workspace token")
	}
	if got, ok := store.consume(token, feishuWorkspaceChoiceCodex, "user-a", "route-a"); !ok || got != workspaceRoot {
		t.Fatalf("consume=(%q,%t), want original workspace", got, ok)
	}
	if _, ok := store.consume(token, feishuWorkspaceChoiceCodex, "user-a", "route-a"); ok {
		t.Fatal("workspace token must be one-shot")
	}
}

func TestFeishuWorkspaceChoiceTokenExpires(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	store := feishuWorkspaceChoiceStore{now: func() time.Time { return now }}
	token := store.issue(feishuWorkspaceChoiceClaude, "user-a", "route-a", "/workspace/a")
	now = now.Add(feishuWorkspaceChoiceTTL)
	if _, ok := store.consume(token, feishuWorkspaceChoiceClaude, "user-a", "route-a"); ok {
		t.Fatal("expired workspace token must be rejected")
	}
}

func TestLegacyFeishuWorkspaceChoiceOnlyMatchesNumericCardCallbacks(t *testing.T) {
	card := platform.IncomingMessage{
		Platform:   platform.PlatformFeishu,
		RawCommand: &platform.CardAction{Action: "choice"},
	}
	if !isLegacyFeishuWorkspaceChoice(card, "/cx cd 0") || !isLegacyFeishuWorkspaceChoice(card, "/cc cd 12") {
		t.Fatal("numeric Feishu workspace card must be treated as expired")
	}
	if isLegacyFeishuWorkspaceChoice(card, "/cx cd @ws_token") || isLegacyFeishuWorkspaceChoice(card, "/cx cd ..") {
		t.Fatal("opaque token and navigation must not be treated as legacy")
	}
	card.RawCommand = nil
	if isLegacyFeishuWorkspaceChoice(card, "/cx cd 0") {
		t.Fatal("typed numeric command must remain compatible")
	}
}
