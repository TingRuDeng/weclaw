package messaging

import (
	"path/filepath"
	"testing"
)

func TestRenameFeishuIdentityPersistsDisplayName(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	store := newFeishuIdentityStore()
	store.SetFilePath(stateFile)
	store.Remember(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))

	result, err := RenameFeishuIdentity(FeishuIdentityRenameRequest{
		Selector:    "on_same_person",
		DisplayName: "张三",
		FilePath:    stateFile,
	})
	if err != nil {
		t.Fatalf("RenameFeishuIdentity error: %v", err)
	}
	if result.Identity != "on_same_person" || result.DisplayName != "张三" {
		t.Fatalf("result=%#v, want renamed identity", result)
	}

	views, loadErr := LoadFeishuIdentityViews(stateFile, false)
	if loadErr != nil {
		t.Fatalf("LoadFeishuIdentityViews error: %v", loadErr)
	}
	if len(views) != 1 || views[0].DisplayName != "张三" {
		t.Fatalf("views=%#v, want persisted display name", views)
	}
}
