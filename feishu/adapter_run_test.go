package feishu

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
)

type fakeWSRunner struct {
	started chan struct{}
	closed  chan struct{}
}

type stubbornWSRunner struct {
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
}

type failingWSRunner struct {
	err     error
	started chan struct{}
}

func (f *failingWSRunner) Start(context.Context) error {
	if f.started != nil {
		close(f.started)
	}
	return f.err
}
func (f *failingWSRunner) Close() {}

type blockingWSRunner struct {
	started chan struct{}
	closed  chan struct{}
}

func (f *blockingWSRunner) Start(context.Context) error {
	close(f.started)
	<-f.closed
	return nil
}

func (f *blockingWSRunner) Close() {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
}

func (f *stubbornWSRunner) Start(context.Context) error {
	close(f.started)
	<-f.release
	return nil
}

func (f *stubbornWSRunner) Close() { close(f.closed) }

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
		!strings.Contains(output, "im:message:readonly") ||
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

func TestAdapterRunRetriesWithoutReturningSDKControlledErrorDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(context.Context, Credentials) error { return nil }
	adapter.wsFactory = func(*dispatcher.EventDispatcher) wsRunner {
		return &failingWSRunner{err: errors.New("connect failed: access_key=secret&ticket=secret"), started: started}
	}

	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(context.Context, platform.IncomingMessage, platform.Replier) {})
	}()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run error=%v, want nil after cancellation", err)
	}
}

func TestAdapterRunReturnsWhenWSStartIgnoresClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ws := &stubbornWSRunner{
		started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}),
	}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(context.Context, Credentials) error { return nil }
	adapter.wsFactory = func(*dispatcher.EventDispatcher) wsRunner { return ws }
	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(context.Context, platform.IncomingMessage, platform.Replier) {})
	}()
	<-ws.started
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run error=%v, want nil after cancellation", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run should not wait forever for ws Start")
	}
	select {
	case <-ws.closed:
	default:
		t.Fatal("Run should close ws client on cancellation")
	}
	close(ws.release)
}

func TestAdapterRunReconnectsAfterWebSocketStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &failingWSRunner{err: errors.New("temporary connection failure")}
	second := &blockingWSRunner{started: make(chan struct{}), closed: make(chan struct{})}
	var attempts int
	var validations int
	var dispatchers []*dispatcher.EventDispatcher
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(context.Context, Credentials) error {
		validations++
		return nil
	}
	adapter.reconnectWait = func(context.Context, time.Duration) error { return nil }
	adapter.wsFactory = func(eventDispatcher *dispatcher.EventDispatcher) wsRunner {
		dispatchers = append(dispatchers, eventDispatcher)
		attempts++
		if attempts == 1 {
			return first
		}
		return second
	}

	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(context.Context, platform.IncomingMessage, platform.Replier) {})
	}()
	select {
	case <-second.started:
	case <-time.After(time.Second):
		t.Fatal("Run did not reconnect after the first websocket stopped")
	}
	if attempts != 2 || validations != 2 || len(dispatchers) != 2 || dispatchers[0] == dispatchers[1] {
		t.Fatalf("attempts=%d validations=%d dispatchers=%d, want two distinct websocket attempts with revalidation", attempts, validations, len(dispatchers))
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run error=%v, want nil after cancellation", err)
	}
}

func TestAdapterRunRevalidatesCredentialsBeforeReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &failingWSRunner{err: errors.New("temporary connection failure")}
	second := &blockingWSRunner{started: make(chan struct{}), closed: make(chan struct{})}
	validations := 0
	attempts := 0
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.reconnectWait = func(context.Context, time.Duration) error { return nil }
	adapter.validate = func(context.Context, Credentials) error {
		validations++
		if validations == 2 {
			return errors.New("temporary credential validation failure")
		}
		return nil
	}
	adapter.wsFactory = func(*dispatcher.EventDispatcher) wsRunner {
		attempts++
		if attempts == 1 {
			return first
		}
		return second
	}

	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(context.Context, platform.IncomingMessage, platform.Replier) {})
	}()
	select {
	case <-second.started:
	case <-time.After(time.Second):
		t.Fatal("Run did not reconnect after credential validation recovered")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run error=%v, want nil after cancellation", err)
	}
	if validations != 3 || attempts != 2 {
		t.Fatalf("validations=%d attempts=%d, want 3 validations and 2 websocket attempts", validations, attempts)
	}
}
