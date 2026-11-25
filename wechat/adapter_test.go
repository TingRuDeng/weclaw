package wechat

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestAdapterIdentityAndCapabilities(t *testing.T) {
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})

	if adapter.Name() != platform.PlatformWeChat {
		t.Fatalf("Name=%q, want wechat", adapter.Name())
	}
	if adapter.AccountID() != "bot-1" {
		t.Fatalf("AccountID=%q, want bot-1", adapter.AccountID())
	}
	caps := adapter.Capabilities()
	if !caps.Text || !caps.Typing || !caps.Image || !caps.File || !caps.LongText {
		t.Fatalf("wechat capabilities missing expected true values: %#v", caps)
	}
	if caps.Card || caps.Streaming || caps.Buttons {
		t.Fatalf("wechat capabilities should not enable card/streaming/buttons: %#v", caps)
	}
}

type fakeWechatMonitor struct {
	started      chan struct{}
	stopped      chan struct{}
	lastActivity time.Time
}

func (f *fakeWechatMonitor) Run(ctx context.Context) error {
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
	return ctx.Err()
}

func (f *fakeWechatMonitor) LastActivity() time.Time {
	return f.lastActivity
}

func (f *fakeWechatMonitor) SetAggregationWindow(window time.Duration) {
}

func TestAdapterWatchdogCancelsIdleMonitor(t *testing.T) {
	monitor := &fakeWechatMonitor{
		started:      make(chan struct{}),
		stopped:      make(chan struct{}),
		lastActivity: time.Now().Add(-time.Minute),
	}
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	adapter.newMonitor = func(client *ilink.Client, handler ilink.MessageHandler) (monitorRunner, error) {
		return monitor, nil
	}
	adapter.watchdogInterval = time.Millisecond
	adapter.watchdogMaxIdle = time.Millisecond

	errCh := make(chan error, 1)
	go func() {
		errCh <- adapter.Run(context.Background(), func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})
	}()
	<-monitor.started
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("Run error=%v, want watchdog context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not cancel idle monitor")
	}
}

func TestAdapterRunReturnsNilOnOuterCancel(t *testing.T) {
	monitor := &fakeWechatMonitor{
		started:      make(chan struct{}),
		stopped:      make(chan struct{}),
		lastActivity: time.Now(),
	}
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	adapter.newMonitor = func(client *ilink.Client, handler ilink.MessageHandler) (monitorRunner, error) {
		return monitor, nil
	}
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- adapter.Run(ctx, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})
	}()
	<-monitor.started
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run error=%v, want nil on outer cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after outer cancel")
	}
}

func TestAdapterSkipsSelfEchoMessage(t *testing.T) {
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot-1"})
	dispatched := 0
	handler := adapter.handleWeixinMessage(func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched++
	})

	handler(context.Background(), client, ilink.WeixinMessage{
		FromUserID:   "user-1",
		ToUserID:     "bot-1",
		ClientID:     "weclaw:echo",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
	})
	handler(context.Background(), client, ilink.WeixinMessage{
		FromUserID:   "user-1",
		ToUserID:     "bot-1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
	})

	if dispatched != 1 {
		t.Fatalf("dispatched=%d, want only normal user message", dispatched)
	}
}

func TestAdapterPersistsAuthorizedContextTokenBeforeDispatch(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	adapter.SetAccessControl(platform.NewAccessControl([]string{"user-1"}))
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot-1"})
	dispatched := false
	handler := adapter.handleWeixinMessage(func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched = true
		if got := adapter.tokenStore.Get("user-1"); got != "ctx-1" {
			t.Fatalf("token at dispatch=%q, want ctx-1", got)
		}
	})

	handler(context.Background(), client, ilink.WeixinMessage{
		FromUserID:   "user-1",
		ToUserID:     "bot-1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx-1",
	})

	if !dispatched {
		t.Fatal("message was not dispatched")
	}
	loaded := newContextTokenStore("bot-1")
	if err := loaded.loadError(); err != nil {
		t.Fatalf("reload context token: %v", err)
	}
	if got := loaded.Get("user-1"); got != "ctx-1" {
		t.Fatalf("persisted token=%q, want ctx-1", got)
	}
}

func TestAdapterDoesNotPersistUnauthorizedContextToken(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	adapter.SetAccessControl(platform.NewAccessControl([]string{"allowed-user"}))
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot-1"})
	dispatched := false
	handler := adapter.handleWeixinMessage(func(_ context.Context, _ platform.IncomingMessage, reply platform.Replier) {
		dispatched = true
		wechatReply, ok := reply.(*Replier)
		if !ok || wechatReply.ContextToken != "untrusted-context" {
			t.Fatalf("current-message reply=%#v, want immediate context token", reply)
		}
	})

	handler(context.Background(), client, ilink.WeixinMessage{
		FromUserID: "unknown-user", ToUserID: "bot-1",
		MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish,
		ContextToken: "untrusted-context",
	})

	if !dispatched {
		t.Fatal("adapter should leave authoritative denial to registry")
	}
	if got := adapter.tokenStore.Get("unknown-user"); got != "" {
		t.Fatalf("in-memory token=%q, unauthorized identity must not persist", got)
	}
	if _, err := os.Stat(contextTokenStorePath("bot-1")); !os.IsNotExist(err) {
		t.Fatalf("unauthorized token file stat error=%v, want not exist", err)
	}
}

func TestAdapterDownloadsAuthorizedCDNAttachmentAndCleansTempFile(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-a"})
	adapter.SetAccessControl(platform.NewAccessControl([]string{"user-1"}))
	downloaded := false
	adapter.downloadCDN = func(_ context.Context, queryParam string, aesKey string) ([]byte, error) {
		downloaded = true
		if queryParam != "enc-param" || aesKey != "aes-key" {
			t.Fatalf("download args=(%q,%q)", queryParam, aesKey)
		}
		return []byte("微信附件正文"), nil
	}
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot-a"})
	var tempPath string
	handler := adapter.handleWeixinMessage(func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		if msg.AccountID != "bot-a" || msg.SessionRouteKey() != "wechat:bot-a:user-1" {
			t.Fatalf("routing=(%q,%q), want bot-a route", msg.AccountID, msg.SessionRouteKey())
		}
		if len(msg.Attachments) != 1 {
			t.Fatalf("attachments=%#v, want 1", msg.Attachments)
		}
		att := msg.Attachments[0]
		if att.Path == "" || att.Metadata["temporary"] != "true" {
			t.Fatalf("attachment=%#v, want temporary staged path", att)
		}
		if att.Metadata["encrypt_query_param"] != "" || att.Metadata["aes_key"] != "" {
			t.Fatalf("attachment=%#v, CDN parameters must not leak to messaging layer", att)
		}
		data, err := os.ReadFile(att.Path)
		if err != nil || string(data) != "微信附件正文" {
			t.Fatalf("read staged file=%q err=%v", string(data), err)
		}
		info, err := os.Stat(att.Path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("staged file mode=%v err=%v, want 0600", info.Mode(), err)
		}
		tempPath = att.Path
	})

	handler(context.Background(), client, ilink.WeixinMessage{
		MessageID:    77,
		FromUserID:   "user-1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList: []ilink.MessageItem{{
			Type: ilink.ItemTypeFile,
			FileItem: &ilink.FileItem{
				FileName: "report.txt",
				Media:    &ilink.MediaInfo{EncryptQueryParam: "enc-param", AESKey: "aes-key"},
			},
		}},
	})

	if !downloaded || tempPath == "" {
		t.Fatalf("downloaded=%v tempPath=%q", downloaded, tempPath)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("unconsumed temp attachment %q should be cleaned up, stat err=%v", tempPath, err)
	}
}

func TestAdapterSkipsCDNDownloadForUnauthorizedUserAndRecordsFailure(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-a"})
	adapter.SetAccessControl(platform.NewAccessControl([]string{"allowed-user"}))
	adapter.downloadCDN = func(context.Context, string, string) ([]byte, error) {
		t.Fatal("unauthorized user must not trigger CDN download")
		return nil, nil
	}
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot-a"})
	handler := adapter.handleWeixinMessage(func(context.Context, platform.IncomingMessage, platform.Replier) {})
	handler(context.Background(), client, ilink.WeixinMessage{
		MessageID: 78, FromUserID: "stranger", ToUserID: "bot-a",
		MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish,
		ItemList: []ilink.MessageItem{{
			Type:     ilink.ItemTypeFile,
			FileItem: &ilink.FileItem{FileName: "a.txt", Media: &ilink.MediaInfo{EncryptQueryParam: "p", AESKey: "k"}},
		}},
	})

	adapter.downloadCDN = func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("cdn unavailable")
	}
	var gotErr string
	handler = adapter.handleWeixinMessage(func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		if len(msg.Attachments) == 1 {
			gotErr = msg.Attachments[0].Metadata["download_error"]
		}
	})
	handler(context.Background(), client, ilink.WeixinMessage{
		MessageID: 79, FromUserID: "allowed-user", ToUserID: "bot-a",
		MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish,
		ItemList: []ilink.MessageItem{{
			Type:     ilink.ItemTypeFile,
			FileItem: &ilink.FileItem{FileName: "a.txt", Media: &ilink.MediaInfo{EncryptQueryParam: "p", AESKey: "k"}},
		}},
	})
	if gotErr != "cdn unavailable" {
		t.Fatalf("download_error=%q, want cdn unavailable", gotErr)
	}
}
