package feishu

import "testing"

func TestBuildFeishuSessionKeyIsolatesDMBySender(t *testing.T) {
	first := FeishuSessionScope{
		TenantID:     "tenant_1",
		ChatID:       "oc_dm",
		SenderOpenID: "ou_a",
		ChatType:     "p2p",
		MessageID:    "om_1",
	}
	second := first
	second.SenderOpenID = "ou_b"

	if got, want := BuildFeishuSessionKey(first), BuildFeishuSessionKey(second); got == want {
		t.Fatalf("DM session key should isolate sender_open_id, got same key %q", got)
	}
}

func TestBuildFeishuSessionKeyIgnoresDMThread(t *testing.T) {
	first := FeishuSessionScope{
		TenantID:     "tenant_1",
		ChatID:       "oc_dm",
		SenderOpenID: "ou_user",
		ChatType:     "p2p",
		RootID:       "om_root_a",
		MessageID:    "om_1",
	}
	second := first
	second.RootID = "om_root_b"

	firstKey := BuildFeishuSessionKey(first)
	secondKey := BuildFeishuSessionKey(second)
	if firstKey != secondKey {
		t.Fatalf("DM thread should share chat session key, got %q and %q", firstKey, secondKey)
	}
	if firstKey != "feishu:tenant_1:dm:oc_dm:ou_user" {
		t.Fatalf("first key=%q, want DM chat key", firstKey)
	}
}

func TestBuildFeishuSessionKeyIgnoresGroupThread(t *testing.T) {
	first := FeishuSessionScope{
		TenantID:     "tenant_1",
		ChatID:       "oc_group",
		RootID:       "om_root_a",
		SenderOpenID: "ou_user",
		ChatType:     "group",
		MessageID:    "om_1",
	}
	second := first
	second.RootID = "om_root_b"

	if got, want := BuildFeishuSessionKey(first), BuildFeishuSessionKey(second); got != want {
		t.Fatalf("group thread should share chat session key, got %q and %q", got, want)
	}
}

func TestResolveThreadKeyFallbackOrder(t *testing.T) {
	scope := FeishuSessionScope{RootID: "om_root", ThreadID: "omt_thread", MessageID: "om_msg"}
	if got := ResolveThreadKey(scope); got != "om_root" {
		t.Fatalf("ResolveThreadKey root priority = %q, want om_root", got)
	}

	scope.RootID = ""
	if got := ResolveThreadKey(scope); got != "omt_thread" {
		t.Fatalf("ResolveThreadKey thread fallback = %q, want omt_thread", got)
	}

	scope.ThreadID = ""
	if got := ResolveThreadKey(scope); got != "om_msg" {
		t.Fatalf("ResolveThreadKey message fallback = %q, want om_msg", got)
	}
}

func TestBuildFeishuSessionKeyUsesGroupChatKey(t *testing.T) {
	first := FeishuSessionScope{TenantID: "tenant_1", ChatID: "oc_group", RootID: "om_root_a", ChatType: "group", MessageID: "om_1"}

	if got := BuildFeishuSessionKey(first); got != "feishu:tenant_1:group:oc_group" {
		t.Fatalf("group session key=%q, want chat key", got)
	}
}
