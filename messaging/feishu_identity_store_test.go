package messaging

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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

func TestLoadFeishuIdentityViewsIncludesOpenIDs(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	store := newFeishuIdentityStore()
	store.SetFilePath(stateFile)
	store.Remember(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))

	views, err := LoadFeishuIdentityViews(stateFile, false)
	if err != nil {
		t.Fatalf("LoadFeishuIdentityViews error: %v", err)
	}
	if len(views) != 1 || views[0].OpenIDs["cli_a"] != "ou_a" {
		t.Fatalf("views=%#v, want cli_a open_id", views)
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

func TestFeishuIdentityStoreSkipsDuplicateSave(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	store := newFeishuIdentityStore()
	store.SetFilePath(stateFile)
	msg := feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person")
	store.Remember(msg)
	first, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read first state: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)
	store.Remember(msg)

	second, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read second state: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("duplicate identity should not rewrite state file")
	}
}

func TestFeishuIdentityStoreCapsDiscoveredRecords(t *testing.T) {
	const wantMaxRecords = 200
	store := newFeishuIdentityStore()
	for i := 0; i < wantMaxRecords+5; i++ {
		suffix := strconv.Itoa(i)
		store.Remember(feishuIdentityMessage("cli_a", "ou_"+suffix, "user_"+suffix, "on_"+suffix))
	}

	records := store.ListRecords()
	if len(records) > wantMaxRecords {
		t.Fatalf("record count=%d, want <= %d", len(records), wantMaxRecords)
	}
}

func TestFeishuIdentityStorePurgesStalePendingRecords(t *testing.T) {
	store := newFeishuIdentityStore()
	old := time.Now().Add(-31 * 24 * time.Hour).UTC().Format(time.RFC3339)
	store.records["old_pending"] = feishuIdentityRecord{Key: "old_pending", OpenID: "old_pending", Pending: true, LastSeen: old}
	store.records["old_approved"] = feishuIdentityRecord{Key: "old_approved", OpenID: "old_approved", Approved: true, LastSeen: old}

	store.Remember(feishuIdentityMessage("cli_a", "ou_new", "user_new", "on_new"))

	if _, ok := store.Find("old_pending"); ok {
		t.Fatal("stale pending identity should be purged")
	}
	if _, ok := store.Find("old_approved"); !ok {
		t.Fatal("approved identity should not be purged by pending TTL")
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
