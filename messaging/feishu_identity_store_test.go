package messaging

import (
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestFeishuIdentityStoreRemembersPendingUnionID(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	store := newFeishuIdentityStore()
	store.SetFilePath(stateFile)

	store.Remember(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))

	pending := store.ListPending()
	if len(pending) != 1 {
		t.Fatalf("pending count=%d, want 1", len(pending))
	}
	record := pending[0]
	if record.Key != "on_same_person" || record.UnionID != "on_same_person" {
		t.Fatalf("record=%#v, want stable union_id key", record)
	}
	if record.OpenIDs["cli_a"] != "ou_a" {
		t.Fatalf("open ids=%#v, want cli_a -> ou_a", record.OpenIDs)
	}
	if !record.Pending || record.Approved {
		t.Fatalf("pending=%v approved=%v, want pending only", record.Pending, record.Approved)
	}
}

func TestFeishuIdentityStorePersistsRecords(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	first := newFeishuIdentityStore()
	first.SetFilePath(stateFile)
	first.Remember(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	first.Remember(feishuIdentityMessage("cli_b", "ou_b", "user_a", "on_same_person"))

	restored := newFeishuIdentityStore()
	restored.SetFilePath(stateFile)

	pending := restored.ListPending()
	if len(pending) != 1 {
		t.Fatalf("pending count=%d, want 1", len(pending))
	}
	if pending[0].OpenIDs["cli_a"] != "ou_a" || pending[0].OpenIDs["cli_b"] != "ou_b" {
		t.Fatalf("open ids=%#v, want both bot open_ids", pending[0].OpenIDs)
	}
}

func TestFeishuIdentityStoreApproveRemovesPendingRecord(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	store := newFeishuIdentityStore()
	store.SetFilePath(stateFile)
	store.Remember(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))

	record, ok := store.Approve("on_same_person")
	if !ok {
		t.Fatal("Approve ok=false, want true")
	}
	if !record.Approved || record.Pending {
		t.Fatalf("record=%#v, want approved and not pending", record)
	}
	if pending := store.ListPending(); len(pending) != 0 {
		t.Fatalf("pending=%#v, want empty after approve", pending)
	}
}

func feishuIdentityMessage(accountID string, openID string, userID string, unionID string) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform:    platform.PlatformFeishu,
		AccountID:   accountID,
		UserID:      openID,
		UserAliases: []string{openID, userID, unionID},
		Metadata: map[string]string{
			"feishu_open_id":  openID,
			"feishu_user_id":  userID,
			"feishu_union_id": unionID,
		},
	}
}
