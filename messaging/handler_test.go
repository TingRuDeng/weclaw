package messaging

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/wechat"
)

const (
	taskQueueProbeDelay = 50 * time.Millisecond
	taskWaitTimeout     = 500 * time.Millisecond
	taskTimeoutWait     = 1500 * time.Millisecond
)

func TestWeChatReplierFormatsLineBreaksForDisplay(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	reply := "🧩 步骤：查询当前工作目录\n🎯 目的：准确返回你当前会话路径\n▶️ 执行：运行 pwd 命令。\n/Volumes/Data/code/MyCode"
	want := "🧩 步骤：查询当前工作目录\n\n🎯 目的：准确返回你当前会话路径\n\n▶️ 执行：运行 pwd 命令。\n\n/Volumes/Data/code/MyCode"

	if err := wechat.NewReplier(client, "user-1", "ctx-1", "client-1").SendText(context.Background(), reply); err != nil {
		t.Fatalf("SendText error: %v", err)
	}

	texts := calls.texts()
	if len(texts) != 1 {
		t.Fatalf("sent texts=%#v, want one text", texts)
	}
	if texts[0] != want {
		t.Fatalf("sent text=%q, want WeChat display line breaks %q", texts[0], want)
	}
}

func TestWeChatReplierSplitsLongTextAndKeepsOrder(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	text := strings.Join([]string{
		strings.Repeat("甲", 12),
		strings.Repeat("乙", 12),
		strings.Repeat("丙", 12),
	}, "\n")

	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	reply.ChunkRunes = 15
	if err := reply.SendText(context.Background(), text); err != nil {
		t.Fatalf("SendText error: %v", err)
	}

	texts := calls.texts()
	if len(texts) != 3 {
		t.Fatalf("sent texts=%#v, want three chunks", texts)
	}
	wantText := wechat.FormatTextForWeChatDisplay(text)
	if strings.Join(texts, "\n") != wantText {
		t.Fatalf("joined chunks=%q, want WeChat display text %q", strings.Join(texts, "\n"), wantText)
	}
	for _, chunk := range texts {
		if len([]rune(chunk)) > 15 {
			t.Fatalf("chunk is too long: %q", chunk)
		}
	}
}

func TestEnsureAgentStartedSerializesConcurrentStartup(t *testing.T) {
	start := make(chan struct{})
	release := make(chan struct{})
	var factoryCalls int
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		factoryCalls++
		close(start)
		<-release
		return &fakeAgent{reply: "ok", info: agent.AgentInfo{Name: name, Type: "test"}}
	}, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := h.EnsureAgentStarted(context.Background(), "codex")
		firstDone <- err
	}()
	<-start

	secondDone := make(chan error, 1)
	go func() {
		_, err := h.EnsureAgentStarted(context.Background(), "codex")
		secondDone <- err
	}()
	time.Sleep(taskQueueProbeDelay)
	close(release)

	if err := <-firstDone; err != nil {
		t.Fatalf("first EnsureAgentStarted error: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second EnsureAgentStarted error: %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("factoryCalls=%d, want one serialized startup", factoryCalls)
	}
}

func TestChatWithAgentWithProgress_UsesProgressInterface(t *testing.T) {
	h := newTestHandler()
	ag := &fakeProgressAgent{
		fakeAgent: fakeAgent{
			reply: "完成",
		},
		progressDeltas: []string{"第一段", "第二段"},
	}

	var got []string
	reply, err := h.chatWithAgentWithProgress(context.Background(), ag, "user-1", "hello", func(delta string) {
		got = append(got, delta)
	})
	if err != nil {
		t.Fatalf("chatWithAgentWithProgress error: %v", err)
	}
	if reply != "完成" {
		t.Fatalf("reply=%q, want=%q", reply, "完成")
	}
	if !ag.progressCalled {
		t.Fatal("expected ChatWithProgress to be called")
	}
	if ag.wasChatCalled() {
		t.Fatal("did not expect fallback Chat to be called")
	}
	if !reflect.DeepEqual(got, []string{"第一段", "第二段"}) {
		t.Fatalf("progress deltas=%v", got)
	}
}

func TestChatWithAgentWithProgress_FallbackToChat(t *testing.T) {
	h := newTestHandler()
	ag := &fakeAgent{reply: "ok"}
	reply, err := h.chatWithAgentWithProgress(context.Background(), ag, "user-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatWithAgentWithProgress error: %v", err)
	}
	if reply != "ok" {
		t.Fatalf("reply=%q, want=%q", reply, "ok")
	}
	if !ag.wasChatCalled() {
		t.Fatal("expected fallback Chat to be called")
	}
}

func TestTruncateTailRunes(t *testing.T) {
	tests := []struct {
		text  string
		limit int
		want  string
	}{
		{text: "abcdef", limit: 3, want: "def"},
		{text: "你好世界", limit: 2, want: "世界"},
		{text: "abc", limit: 10, want: "abc"},
		{text: "abc", limit: 0, want: ""},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("limit_%d_%s", tt.limit, tt.text), func(t *testing.T) {
			got := truncateTailRunes(tt.text, tt.limit)
			if got != tt.want {
				t.Fatalf("truncateTailRunes(%q,%d)=%q, want=%q", tt.text, tt.limit, got, tt.want)
			}
		})
	}
}
