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

	if got, want := BuildFeishuSessionKey(first, true), BuildFeishuSessionKey(second, true); got == want {
		t.Fatalf("DM session key should isolate sender_open_id, got same key %q", got)
	}
}

func TestBuildFeishuSessionKeyIsolatesGroupThread(t *testing.T) {
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

	if got, want := BuildFeishuSessionKey(first, true), BuildFeishuSessionKey(second, true); got == want {
		t.Fatalf("group thread session key should isolate thread_key, got same key %q", got)
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

func TestBuildFeishuSessionKeyMergesThreadWhenIsolationDisabled(t *testing.T) {
	first := FeishuSessionScope{TenantID: "tenant_1", ChatID: "oc_group", RootID: "om_root_a", ChatType: "group", MessageID: "om_1"}
	second := first
	second.RootID = "om_root_b"

	if got, want := BuildFeishuSessionKey(first, false), BuildFeishuSessionKey(second, false); got != want {
		t.Fatalf("thread_isolation=false should merge group threads, got %q and %q", got, want)
	}
}
