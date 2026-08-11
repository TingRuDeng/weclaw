package messaging

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestFeishuIdentityAuthorizationDoesNotReuseAnotherBotsOpenID(t *testing.T) {
	record := feishuIdentityRecord{
		Key: "on_same_person", UnionID: "on_same_person", UserID: "user_same_person",
		OpenID: "ou_a", OpenIDs: map[string]string{"cli_a": "ou_a", "cli_b": "ou_b"},
		Accounts: []string{"cli_a", "cli_b"},
	}
	bot := config.FeishuBotConfig{AppID: "cli_a", AllowedUsers: []string{"ou_b"}}
	if feishuIdentityAllowedByBot(record, "cli_a", bot) {
		t.Fatal("cli_a was authorized by cli_b open_id")
	}
	keys := feishuIdentityAuthKeys(record, "cli_a")
	if stringSliceContains(keys, "ou_b") {
		t.Fatalf("cli_a authorization keys leaked cli_b open_id: %#v", keys)
	}
	if !stringSliceContains(keys, "ou_a") || !stringSliceContains(keys, "on_same_person") {
		t.Fatalf("cli_a authorization keys lost current/stable identity: %#v", keys)
	}
}

func TestFeishuIdentityLegacyOpenIDFallbackRequiresCurrentAccount(t *testing.T) {
	record := feishuIdentityRecord{
		Key: "ou_b", OpenID: "ou_b", Accounts: []string{"cli_b"},
	}
	bot := config.FeishuBotConfig{AppID: "cli_a", AllowedUsers: []string{"ou_b"}}
	if feishuIdentityAllowedByBot(record, "cli_a", bot) {
		t.Fatal("cli_a was authorized by a legacy open_id known only on cli_b")
	}
	if keys := feishuIdentityAuthKeys(record, "cli_a"); stringSliceContains(keys, "ou_b") {
		t.Fatalf("legacy fallback leaked another account open_id: %#v", keys)
	}
}
