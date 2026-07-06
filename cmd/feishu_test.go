package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/feishu"
)

func TestRunFeishuLoginValidatesBeforeSave(t *testing.T) {
	oldValidator := validateFeishuCreds
	oldSaver := saveFeishuCreds
	defer func() {
		validateFeishuCreds = oldValidator
		saveFeishuCreds = oldSaver
	}()
	var validated bool
	var saved bool
	validateFeishuCreds = func(ctx context.Context, creds feishu.Credentials) error {
		validated = true
		if creds.AppID != "cli_a" || creds.AppSecret != "secret-a" {
			t.Fatalf("creds=%#v, want input credentials", creds)
		}
		return nil
	}
	saveFeishuCreds = func(name string, creds feishu.Credentials) error {
		if !validated {
			t.Fatal("save called before validation")
		}
		if name != "project-a" {
			t.Fatalf("name=%q, want project-a", name)
		}
		saved = true
		return nil
	}

	if err := runFeishuLogin(context.Background(), "project-a", "cli_a", "secret-a"); err != nil {
		t.Fatalf("runFeishuLogin error: %v", err)
	}
	if !saved {
		t.Fatal("credentials were not saved")
	}
}

func TestRunFeishuStatusDoesNotPrintSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret := "secret-should-not-print"
	if err := feishu.SaveCredentialsForBot("project-a", feishu.Credentials{AppID: "cli_a", AppSecret: secret}); err != nil {
		t.Fatalf("SaveCredentialsForBot error: %v", err)
	}
	oldValidator := validateFeishuCreds
	defer func() { validateFeishuCreds = oldValidator }()
	validateFeishuCreds = func(ctx context.Context, creds feishu.Credentials) error {
		return nil
	}

	output := captureStdout(t, func() {
		if err := runFeishuStatus(context.Background(), "project-a"); err != nil {
			t.Fatalf("runFeishuStatus error: %v", err)
		}
	})

	if strings.Contains(output, secret) {
		t.Fatalf("status output leaks secret: %s", output)
	}
	if !strings.Contains(output, "飞书凭证有效") || !strings.Contains(output, "cli_a") {
		t.Fatalf("status output=%q, want valid status with app id", output)
	}
}

// captureStdout 捕获命令输出，确保状态命令不会打印敏感信息。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writeEnd
	defer func() { os.Stdout = oldStdout }()

	fn()
	if err := writeEnd.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, readEnd); err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return buf.String()
}
