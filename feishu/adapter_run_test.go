package feishu

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
)

type fakeWSRunner struct {
	started chan struct{}
	closed  chan struct{}
}

// Start 记录启动状态，并阻塞到 Close 被调用。
func (f *fakeWSRunner) Start(ctx context.Context) error {
	close(f.started)
	<-f.closed
	return nil
}

// Close 关闭测试长连接。
func (f *fakeWSRunner) Close() {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
}

func TestAdapterCapabilities(t *testing.T) {
	caps := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"}).Capabilities()

	if !caps.Text || !caps.Typing || !caps.Image || !caps.File || !caps.Card || !caps.Streaming || !caps.Buttons {
		t.Fatalf("capabilities=%#v, want feishu rich capabilities", caps)
	}
	if caps.LongText {
		t.Fatalf("LongText=%v, want false", caps.LongText)
	}
}

func TestAdapterRunValidatesAndStartsWS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws := &fakeWSRunner{started: make(chan struct{}), closed: make(chan struct{})}
	var validated bool
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(ctx context.Context, creds Credentials) error {
		validated = true
		return nil
	}
	adapter.wsFactory = func(eventDispatcher *dispatcher.EventDispatcher) wsRunner {
		if eventDispatcher == nil {
			t.Fatal("event dispatcher is nil")
		}
		return ws
	}

	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})
	}()
	<-ws.started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !validated {
		t.Fatal("credentials were not validated")
	}
}

func TestAdapterRunLogsPermissionGuideAfterValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws := &fakeWSRunner{started: make(chan struct{}), closed: make(chan struct{})}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(ctx context.Context, creds Credentials) error { return nil }
	adapter.wsFactory = func(eventDispatcher *dispatcher.EventDispatcher) wsRunner { return ws }
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})
	}()
	<-ws.started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run error: %v", err)
	}

	output := logs.String()
	if !strings.Contains(output, "https://open.feishu.cn/app/cli_a/permission") ||
		!strings.Contains(output, "im:message") ||
		!strings.Contains(output, "cardkit:card") {
		t.Fatalf("logs=%q, want permission guide", output)
	}
}

func TestAdapterRunStopsWhenValidationFails(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(ctx context.Context, creds Credentials) error {
		return context.Canceled
	}
	adapter.wsFactory = func(eventDispatcher *dispatcher.EventDispatcher) wsRunner {
		t.Fatal("ws should not start when validation fails")
		return nil
	}

	err := adapter.Run(context.Background(), func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})

	if err == nil {
		t.Fatal("Run error=nil, want validation failure")
	}
}
