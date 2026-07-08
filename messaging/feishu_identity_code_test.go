package messaging

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObserveFeishuIdentityReturnsAuthCodeNotice(t *testing.T) {
	handler := NewHandler(nil, nil)
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	handler.SetFeishuIdentityFile(stateFile)

	notice := handler.ObserveDeniedFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))

	if !strings.Contains(notice, "当前账号无权限，请联系管理员授权") ||
		!strings.Contains(notice, "授权码: ") {
		t.Fatalf("notice=%q, want auth code notice", notice)
	}
	records, err := LoadFeishuIdentityViews(stateFile, true)
	if err != nil {
		t.Fatalf("LoadFeishuIdentityViews error: %v", err)
	}
	if len(records) != 1 || records[0].AuthCode == "" {
		t.Fatalf("records=%#v, want persisted auth code", records)
	}
}

func TestApproveFeishuIdentityByCodeWritesDisplayName(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	store := newFeishuIdentityStore()
	store.SetFilePath(stateFile)
	store.Remember(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	record, ok := store.IssueAuthCode("on_same_person", time.Now())
	if !ok {
		t.Fatal("IssueAuthCode ok=false, want true")
	}

	result, err := ApproveFeishuIdentityByCode(FeishuIdentityApproveCodeRequest{
		Code:        record.AuthCode,
		DisplayName: "张三",
		FilePath:    stateFile,
	})
	if err != nil {
		t.Fatalf("ApproveFeishuIdentityByCode error: %v", err)
	}
	if result.Identity != "on_same_person" || result.DisplayName != "张三" {
		t.Fatalf("result=%#v, want approved identity with display name", result)
	}

	views, loadErr := LoadFeishuIdentityViews(stateFile, false)
	if loadErr != nil {
		t.Fatalf("LoadFeishuIdentityViews error: %v", loadErr)
	}
	if len(views) != 1 || views[0].AuthCode != "" || views[0].DisplayName != "张三" {
		t.Fatalf("views=%#v, want used code cleared and display name persisted", views)
	}
}

func TestApproveFeishuIdentityByCodeRejectsExpiredCode(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	store := newFeishuIdentityStore()
	store.SetFilePath(stateFile)
	store.records["on_same_person"] = feishuIdentityRecord{
		Key:               "on_same_person",
		UnionID:           "on_same_person",
		OpenID:            "ou_a",
		Accounts:          []string{"cli_a"},
		AuthCode:          "123456",
		AuthCodeExpiresAt: "2000-01-01T00:00:00Z",
		Pending:           true,
	}
	store.save()

	_, err := ApproveFeishuIdentityByCode(FeishuIdentityApproveCodeRequest{
		Code:     "123456",
		FilePath: stateFile,
	})

	if err == nil || !strings.Contains(err.Error(), "授权码无效或已过期") {
		t.Fatalf("error=%v, want expired code rejection", err)
	}
}
