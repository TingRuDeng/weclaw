package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/messaging"
)

func TestRunFeishuUsersListPrintsContactName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuUserForTest(t, "on_approved")

	oldLookup := lookupFeishuIdentityNames
	lookupFeishuIdentityNames = func(context.Context, []messaging.FeishuIdentityView, []feishuIdentityNameLookupAccount) feishuIdentityNameLookupResult {
		return feishuIdentityNameLookupResult{
			Names: map[string]string{
				"on_approved": "张三",
			},
		}
	}
	t.Cleanup(func() {
		lookupFeishuIdentityNames = oldLookup
	})

	output := captureStdout(t, func() {
		if err := runFeishuUsers("list"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if !strings.Contains(output, "张三 (on_approved)") {
		t.Fatalf("output=%q, want contact name before stable id", output)
	}
}

func TestRunFeishuUsersListPrintsContactLookupWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuUserForTest(t, "on_approved")

	oldLookup := lookupFeishuIdentityNames
	lookupFeishuIdentityNames = func(context.Context, []messaging.FeishuIdentityView, []feishuIdentityNameLookupAccount) feishuIdentityNameLookupResult {
		return feishuIdentityNameLookupResult{
			Warnings: []string{"卡片管家 查询 on_approved 失败: 缺少通讯录权限"},
		}
	}
	t.Cleanup(func() {
		lookupFeishuIdentityNames = oldLookup
	})

	output := captureStdout(t, func() {
		if err := runFeishuUsers("list"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if !strings.Contains(output, "提示: 部分姓名未能从飞书通讯录获取") ||
		!strings.Contains(output, "weclaw feishu users rename <id> <显示名>") {
		t.Fatalf("output=%q, want manual rename hint", output)
	}
}

func TestLookupFeishuIdentityNameHidesRecoveredWarning(t *testing.T) {
	oldFetch := fetchFeishuContactNameFn
	calls := 0
	fetchFeishuContactNameFn = func(context.Context, feishuContactUserQuery) (string, error) {
		calls++
		if calls == 1 {
			return "", fmt.Errorf("缺少通讯录权限")
		}
		return "张三", nil
	}
	t.Cleanup(func() {
		fetchFeishuContactNameFn = oldFetch
	})

	view := messaging.FeishuIdentityView{
		Key:      "on_same_person",
		UnionID:  "on_same_person",
		Accounts: []string{"cli_a", "cli_b"},
		OpenIDs: map[string]string{
			"cli_a": "ou_a",
			"cli_b": "ou_b",
		},
	}
	queries := feishuContactQueriesForView(view, []feishuIdentityNameLookupAccount{
		{Name: "project-a", AppID: "cli_a", Label: "项目 A"},
		{Name: "project-b", AppID: "cli_b", Label: "项目 B"},
	})
	name, warnings := lookupFeishuIdentityName(context.Background(), view, []feishuIdentityNameLookupAccount{
		{Name: "project-a", AppID: "cli_a", Label: "项目 A"},
		{Name: "project-b", AppID: "cli_b", Label: "项目 B"},
	})

	if len(queries) != 2 || name != "张三" || len(warnings) != 0 {
		t.Fatalf("queries=%#v name=%q warnings=%#v, want recovered name without warnings", queries, name, warnings)
	}
}

func TestFeishuContactQueriesUseLegacyOpenID(t *testing.T) {
	view := messaging.FeishuIdentityView{
		Key:      "ou_legacy",
		OpenID:   "ou_legacy",
		Accounts: []string{"cli_a"},
	}

	queries := feishuContactQueriesForView(view, []feishuIdentityNameLookupAccount{
		{Name: "project-a", AppID: "cli_a", Label: "项目 A"},
	})

	if len(queries) != 1 || queries[0].UserID != "ou_legacy" || queries[0].UserIDType != "open_id" {
		t.Fatalf("queries=%#v, want legacy open_id lookup", queries)
	}
}
