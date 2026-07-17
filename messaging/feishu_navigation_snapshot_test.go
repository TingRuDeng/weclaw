package messaging

import (
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestFeishuNavigationSnapshotIsReusableAndScopeBound(t *testing.T) {
	store := feishuNavigationSnapshotStore{}
	scope := feishuNavigationSnapshotScope{
		AccountID: "cli_a", ActorUserID: "ou_user", BindingKey: "route-a",
		AgentKind: feishuWorkspaceChoiceCodex, Section: feishuNavigationSectionWorkspaces,
	}
	token := store.issue(scope, []platform.Choice{{ID: "/workspace/a", Label: "A"}})
	first, ok := store.load(token, scope)
	if !ok || len(first) != 1 || first[0].ID != "/workspace/a" {
		t.Fatalf("first=(%#v,%t), want snapshot", first, ok)
	}
	first[0].ID = "/mutated"
	second, ok := store.load(token, scope)
	if !ok || second[0].ID != "/workspace/a" {
		t.Fatalf("second=(%#v,%t), snapshot load must return a copy", second, ok)
	}
	other := scope
	other.AccountID = "cli_other"
	if _, ok := store.load(token, other); ok {
		t.Fatal("different bot account must not load the snapshot")
	}
	other = scope
	other.ActorUserID = "ou_other"
	if _, ok := store.load(token, other); ok {
		t.Fatal("different actor must not load the snapshot")
	}
	other = scope
	other.BindingKey = "route-b"
	if _, ok := store.load(token, other); ok {
		t.Fatal("different window must not load the snapshot")
	}
}

func TestFeishuNavigationSnapshotExpires(t *testing.T) {
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	store := feishuNavigationSnapshotStore{now: func() time.Time { return now }}
	scope := feishuNavigationSnapshotScope{
		AccountID: "cli_a", ActorUserID: "ou_user", BindingKey: "route-a",
		AgentKind: feishuWorkspaceChoiceClaude, Section: feishuNavigationSectionSessions,
		WorkspaceRoot: "/workspace/a",
	}
	token := store.issue(scope, []platform.Choice{{ID: "/cc switch session-a", Label: "A"}})
	now = now.Add(feishuNavigationSnapshotTTL)
	if _, ok := store.load(token, scope); ok {
		t.Fatal("expired navigation snapshot must be rejected")
	}
}
