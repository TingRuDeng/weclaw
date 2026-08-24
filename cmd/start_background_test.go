package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestRunBackgroundStartStopsWhenCredentialLoadFails(t *testing.T) {
	wantErr := errors.New("凭据目录不可读")
	daemonCalled := false
	err := runBackgroundStartWithOps(config.DefaultConfig(), backgroundStartOps{
		loadAccounts: func() ([]*ilink.Credentials, error) { return nil, wantErr },
		runDaemon: func() error {
			daemonCalled = true
			return nil
		},
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("runBackgroundStartWithOps error=%v, want %v", err, wantErr)
	}
	if daemonCalled {
		t.Fatal("加载失败后不应启动 daemon")
	}
}

func TestRunBackgroundStartWithExistingAccountRunsDaemon(t *testing.T) {
	daemonCalls := 0
	err := runBackgroundStartWithOps(config.DefaultConfig(), backgroundStartOps{
		loadAccounts: func() ([]*ilink.Credentials, error) {
			return []*ilink.Credentials{{BotToken: "token"}}, nil
		},
		runDaemon: func() error {
			daemonCalls++
			return nil
		},
	})

	if err != nil {
		t.Fatalf("runBackgroundStartWithOps error: %v", err)
	}
	if daemonCalls != 1 {
		t.Fatalf("daemonCalls=%d, want 1", daemonCalls)
	}
}

func TestRunBackgroundStartFreshConfigRunsDaemonWithoutLogin(t *testing.T) {
	daemonCalls := 0
	err := runBackgroundStartWithOps(config.DefaultConfig(), backgroundStartOps{
		loadAccounts: func() ([]*ilink.Credentials, error) { return nil, nil },
		runDaemon: func() error {
			daemonCalls++
			return nil
		},
	})

	if err != nil {
		t.Fatalf("runBackgroundStartWithOps error: %v", err)
	}
	if daemonCalls != 1 {
		t.Fatalf("daemonCalls=%d, want 1", daemonCalls)
	}
}

func TestRunBackgroundStartRejectsEnabledWeChatWithoutAccount(t *testing.T) {
	enabled := true
	cfg := config.DefaultConfig()
	cfg.Platforms["wechat"] = config.PlatformConfig{Enabled: &enabled}
	daemonCalled := false
	err := runBackgroundStartWithOps(cfg, backgroundStartOps{
		loadAccounts: func() ([]*ilink.Credentials, error) { return nil, nil },
		runDaemon: func() error {
			daemonCalled = true
			return nil
		},
	})

	if err == nil || !strings.Contains(err.Error(), "weclaw wechat login") {
		t.Fatalf("runBackgroundStartWithOps error=%v, want explicit login guidance", err)
	}
	if daemonCalled {
		t.Fatal("显式启用微信但缺少账号时不应启动 daemon")
	}
}

func TestRunBackgroundStartFeishuOnlyDoesNotLoginWeChat(t *testing.T) {
	enabled := true
	cfg := config.DefaultConfig()
	cfg.Platforms["feishu"] = config.PlatformConfig{Enabled: &enabled}
	err := runBackgroundStartWithOps(cfg, backgroundStartOps{
		loadAccounts: func() ([]*ilink.Credentials, error) {
			t.Fatal("飞书-only 不应读取微信凭证")
			return nil, nil
		},
		runDaemon: func() error { return nil },
	})

	if err != nil {
		t.Fatalf("runBackgroundStartWithOps error: %v", err)
	}
}
