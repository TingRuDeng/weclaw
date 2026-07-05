package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/wechat"
)

type recordedILinkCalls struct {
	mu             sync.Mutex
	textMessages   []string
	typingStatuses []int
}

func newRecordingILinkClient(t *testing.T) (*ilink.Client, *recordedILinkCalls, func()) {
	t.Helper()

	calls := &recordedILinkCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getconfig":
			_ = json.NewEncoder(w).Encode(ilink.GetConfigResponse{Ret: 0, TypingTicket: "ticket-1"})
		case "/ilink/bot/sendtyping":
			var req ilink.SendTypingRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode sendtyping: %v", err)
			}
			calls.mu.Lock()
			calls.typingStatuses = append(calls.typingStatuses, req.Status)
			calls.mu.Unlock()
			_ = json.NewEncoder(w).Encode(ilink.SendTypingResponse{Ret: 0})
		case "/ilink/bot/sendmessage":
			var req ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode sendmessage: %v", err)
			}
			for _, item := range req.Msg.ItemList {
				if item.TextItem != nil {
					calls.mu.Lock()
					calls.textMessages = append(calls.textMessages, item.TextItem.Text)
					calls.mu.Unlock()
				}
			}
			_ = json.NewEncoder(w).Encode(ilink.SendMessageResponse{Ret: 0})
		default:
			http.NotFound(w, r)
		}
	}))

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    server.URL,
		BotToken:   "test-token",
		ILinkBotID: "bot-1",
	})
	return client, calls, server.Close
}

func (r *recordedILinkCalls) texts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.textMessages...)
}

func (r *recordedILinkCalls) typings() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.typingStatuses...)
}

func waitForText(t *testing.T, calls *recordedILinkCalls, contains string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, text := range calls.texts() {
			if strings.Contains(text, contains) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("未等到包含 %q 的消息，已发送: %#v", contains, calls.texts())
}

func waitForFakeAgentCalls(t *testing.T, ag *fakeAgent, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ag.chatCallCount() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("未等到 fake agent 调用次数=%d，实际=%d", want, ag.chatCallCount())
}

func newTextMessage(id int64, text string) ilink.WeixinMessage {
	return ilink.WeixinMessage{
		MessageID:    id,
		FromUserID:   "user-1",
		ToUserID:     "bot-1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx-1",
		ItemList: []ilink.MessageItem{{
			Type:     ilink.ItemTypeText,
			TextItem: &ilink.TextItem{Text: text},
		}},
	}
}

func handleTestWeChatMessage(h *Handler, ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	if msg.MessageType != ilink.MessageTypeUser || msg.MessageState != ilink.MessageStateFinish {
		return
	}
	reply := wechat.NewReplier(client, msg.FromUserID, msg.ContextToken, "")
	h.HandleMessage(ctx, wechat.IncomingFromWeixin(msg), reply)
}

func newFileMessage(id int64, fileName string) ilink.WeixinMessage {
	return ilink.WeixinMessage{
		MessageID:    id,
		FromUserID:   "user-1",
		ToUserID:     "bot-1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx-1",
		ItemList: []ilink.MessageItem{{
			Type: ilink.ItemTypeFile,
			FileItem: &ilink.FileItem{
				FileName: fileName,
				Media: &ilink.MediaInfo{
					EncryptQueryParam: "download-param",
					AESKey:            "aes-key",
				},
			},
		}},
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func containsText(texts []string, part string) bool {
	for _, text := range texts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

func waitForAgentEnter(t *testing.T, ag *blockingProgressAgent) {
	t.Helper()
	select {
	case <-ag.entered:
	case <-time.After(taskWaitTimeout):
		t.Fatal("未等到 Agent 开始处理")
	}
}

func waitForCodexThreadAgentEnter(t *testing.T, ag *blockingCodexThreadAgent) {
	t.Helper()
	select {
	case <-ag.entered:
	case <-time.After(taskWaitTimeout):
		t.Fatal("未等到 Codex Agent 开始处理")
	}
}

func waitDone(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(taskWaitTimeout):
		t.Fatalf("未等到%s结束", label)
	}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(taskWaitTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func hasPendingApprovalForTest(h *Handler, userID string) bool {
	h.pendingApprovalsMu.Lock()
	defer h.pendingApprovalsMu.Unlock()
	for _, pending := range h.pendingApprovals {
		if pending.userID == strings.TrimSpace(userID) {
			return true
		}
	}
	return false
}

func progressConfigWithTaskTimeout() config.ProgressConfig {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	cfg.TaskTimeoutSeconds = 1
	return cfg
}

func runWithExpectedTaskTimeout(t *testing.T, run func(context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		run(ctx)
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(taskTimeoutWait):
		cancel()
		<-done
		t.Fatalf("任务未在 %s 内按 task_timeout_seconds 自动结束", taskTimeoutWait)
	}
}

func textIndex(texts []string, part string) int {
	for i, text := range texts {
		if strings.Contains(text, part) {
			return i
		}
	}
	return -1
}
