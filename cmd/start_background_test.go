package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestRunBackgroundStartStopsWhenCredentialLoadFails(t *testing.T) {
	wantErr := errors.New("凭据目录不可读")
	loginCalled := false
	daemonCalled := false
	err := runBackgroundStartWithOps(config.DefaultConfig(), backgroundStartOps{
		loadAccounts: func() ([]*ilink.Credentials, error) { return nil, wantErr },
		login: func(context.Context) (*ilink.Credentials, error) {
			loginCalled = true
			return nil, nil
		},
		runDaemon: func() error {
			daemonCalled = true
			return nil
		},
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("runBackgroundStartWithOps error=%v, want %v", err, wantErr)
	}
	if loginCalled || daemonCalled {
		t.Fatalf("加载失败后 loginCalled=%v daemonCalled=%v，均应为 false", loginCalled, daemonCalled)
	}
}

func TestRunBackgroundStartWithExistingAccountRunsDaemon(t *testing.T) {
	daemonCalls := 0
	err := runBackgroundStartWithOps(config.DefaultConfig(), backgroundStartOps{
		loadAccounts: func() ([]*ilink.Credentials, error) {
			return []*ilink.Credentials{{BotToken: "token"}}, nil
		},
		login: func(context.Context) (*ilink.Credentials, error) {
			t.Fatal("已有账号时不应登录")
			return nil, nil
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
