package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/messaging"
)

func TestRunFeishuUsersRenameUpdatesDisplayName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuUserForTest(t, "on_approved")

	output := captureStdout(t, func() {
		err := runFeishuUsersRename(feishuUsersRenameOptions{
			Selector:    "on_approved",
			DisplayName: "张三",
		})
		if err != nil {
			t.Fatalf("runFeishuUsersRename error: %v", err)
		}
	})

	if !strings.Contains(output, "已更新飞书用户显示名: 张三 (on_approved)") {
		t.Fatalf("output=%q, want rename completion message", output)
	}

	listOutput := captureStdout(t, func() {
		if err := runFeishuUsers("list"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})
	if !strings.Contains(listOutput, "张三 (on_approved)") {
		t.Fatalf("output=%q, want manual display name in list", listOutput)
	}
}

func TestFeishuIdentityDisplayLabelPrefersManualName(t *testing.T) {
	label := feishuIdentityDisplayLabelForNames("on_same_person", "李四", "张三")

	if label != "李四 (on_same_person)" {
		t.Fatalf("label=%q, want manual name first", label)
	}
}

func TestRunFeishuUsersApproveCodeUsesContactName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateWithAuthCodeForTest(t)
	writeFeishuBotsConfigForTest(t)

	oldLookup := lookupFeishuIdentityNames
	lookupFeishuIdentityNames = func(_ context.Context, views []messaging.FeishuIdentityView, _ []feishuIdentityNameLookupAccount) feishuIdentityNameLookupResult {
		if len(views) != 1 || views[0].AuthCode != "" || !views[0].Approved {
			t.Fatalf("views=%#v, want approved identity view with cleared auth code", views)
		}
		return feishuIdentityNameLookupResult{Names: map[string]string{"on_same_person": "张三"}}
	}
	t.Cleanup(func() {
		lookupFeishuIdentityNames = oldLookup
	})

	output := captureStdout(t, func() {
		err := runFeishuUsersApproveCode(feishuUsersApproveCodeOptions{Code: "123456"})
		if err != nil {
			t.Fatalf("runFeishuUsersApproveCode error: %v", err)
		}
	})

	if !strings.Contains(output, "已授权飞书用户: 张三 (on_same_person)") {
		t.Fatalf("output=%q, want contact name in approval result", output)
	}
}
