package feishu

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
)

// TestFeishuDedupReservationRenewExtendsOwnership 验证续租会刷新原处理者的所有权时间。
func TestFeishuDedupReservationRenewExtendsOwnership(t *testing.T) {
	base := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	deduper := newFeishuEventDeduper(time.Minute)
	now := base
	deduper.now = func() time.Time { return now }
	event := newDMEvent(dedupTestEventOptions{MessageID: "om_lease", EventID: "evt_lease", Text: "附件"})
	scope := ExtractFeishuSessionScope(event)
	reservation, duplicate := deduper.reserve(event, scope)
	if duplicate {
		t.Fatal("首次预约不应判重")
	}
	now = base.Add(50 * time.Second)
	if !reservation.renew() {
		t.Fatal("当前所有者续租失败")
	}
	now = base.Add(100 * time.Second)
	if _, duplicate = deduper.reserve(event, scope); !duplicate {
		t.Fatal("续租后的处理权不应按原预约时间过期")
	}
}

// TestFeishuDedupReservationRenewRejectsStaleOwner 验证旧处理者不能刷新新 owner 的租约。
func TestFeishuDedupReservationRenewRejectsStaleOwner(t *testing.T) {
	base := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	deduper := newFeishuEventDeduper(time.Minute)
	now := base
	deduper.now = func() time.Time { return now }
	event := newDMEvent(dedupTestEventOptions{MessageID: "om_stale_lease", EventID: "evt_stale_lease", Text: "附件"})
	scope := ExtractFeishuSessionScope(event)
	first, _ := deduper.reserve(event, scope)
	now = base.Add(2 * time.Minute)
	second, duplicate := deduper.reserve(event, scope)
	if duplicate {
		t.Fatal("过期后新处理者应取得预约")
	}
	if first.renew() {
		t.Fatal("旧处理者不应刷新新 owner 的租约")
	}
	if !second.renew() {
		t.Fatal("当前所有者应能刷新租约")
	}
}

type blockingLeaseDownloader struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

// DownloadResource 阻塞资源下载，用于验证处理时间超过 TTL 时仍保持去重所有权。
func (d *blockingLeaseDownloader) DownloadResource(ctx context.Context, _ string, _ types.Resource) (platform.Attachment, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	select {
	case d.started <- struct{}{}:
	default:
	}
	select {
	case <-d.release:
		return platform.Attachment{Kind: platform.AttachmentFile, Path: "/tmp/lease-file"}, nil
	case <-ctx.Done():
		return platform.Attachment{}, ctx.Err()
	}
}

func (d *blockingLeaseDownloader) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// TestSlowAttachmentDownloadKeepsDedupOwnership 验证慢附件下载期间重投不会启动第二次下载。
func TestSlowAttachmentDownloadKeepsDedupOwnership(t *testing.T) {
	const ttl = 60 * time.Millisecond
	downloader := &blockingLeaseDownloader{
		started: make(chan struct{}, 2), release: make(chan struct{}),
	}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper = newFeishuEventDeduper(ttl)
	adapter.downloader = downloader
	event := newMessageEvent("p2p", "image", `{"image_key":"img_lease"}`)
	results := make(chan error, 2)
	dispatches := make(chan struct{}, 2)
	dispatch := func(context.Context, platform.IncomingMessage, platform.Replier) { dispatches <- struct{}{} }
	go func() { results <- adapter.handleMessageEvent(context.Background(), event, dispatch) }()
	select {
	case <-downloader.started:
	case <-time.After(time.Second):
		t.Fatal("首次附件下载未开始")
	}
	time.Sleep(2 * ttl)
	go func() { results <- adapter.handleMessageEvent(context.Background(), event, dispatch) }()
	time.Sleep(ttl)
	close(downloader.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("handle message error: %v", err)
		}
	}
	if downloader.callCount() != 1 {
		t.Fatalf("download calls=%d, want 1", downloader.callCount())
	}
	if len(dispatches) != 1 {
		t.Fatalf("dispatches=%d, want 1", len(dispatches))
	}
}

// TestAttachmentDownloadOwnershipLossStopsDispatch 验证租约换主后取消下载、阻止分发并反馈用户。
func TestAttachmentDownloadOwnershipLossStopsDispatch(t *testing.T) {
	downloader := &blockingLeaseDownloader{
		started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	sender := &fakeMessageSender{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper = newFeishuEventDeduper(60 * time.Millisecond)
	adapter.downloader = downloader
	adapter.sender = sender
	event := newMessageEvent("p2p", "image", `{"image_key":"img_lost"}`)
	result := make(chan error, 1)
	dispatches := 0
	go func() {
		result <- adapter.handleMessageEvent(context.Background(), event, func(context.Context, platform.IncomingMessage, platform.Replier) {
			dispatches++
		})
	}()
	select {
	case <-downloader.started:
	case <-time.After(time.Second):
		t.Fatal("附件下载未开始")
	}
	adapter.deduper.mu.Lock()
	for key, claim := range adapter.deduper.processing {
		claim.owner = &feishuDedupOwner{marker: 2}
		adapter.deduper.processing[key] = claim
	}
	adapter.deduper.mu.Unlock()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ownership loss notice error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("失去所有权后附件下载未停止")
	}
	close(downloader.release)
	if dispatches != 0 {
		t.Fatalf("dispatches=%d, want 0", dispatches)
	}
	if len(sender.replyTexts) != 1 || !strings.Contains(sender.replyTexts[0], "状态已失效") {
		t.Fatalf("replyTexts=%#v", sender.replyTexts)
	}
}
