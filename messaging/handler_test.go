package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
	"github.com/fastclaw-ai/weclaw/wechat"
)

const (
	taskQueueProbeDelay = 50 * time.Millisecond
	taskWaitTimeout     = 500 * time.Millisecond
	taskTimeoutWait     = 1500 * time.Millisecond
)

func newTestHandler() *Handler {
	return &Handler{agents: make(map[string]agent.Agent)}
}

type fakeAgent struct {
	mu                 sync.Mutex
	reply              string
	err                error
	chatCalled         bool
	chatCalls          int
	lastConversationID string
	lastMessage        string
	lastCwd            string
	resetConversation  string
	resetSessionID     string
	info               agent.AgentInfo
}

func (f *fakeAgent) Chat(_ context.Context, conversationID string, message string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.chatCalled = true
	f.chatCalls++
	f.lastConversationID = conversationID
	f.lastMessage = message
	return f.reply, f.err
}

func (f *fakeAgent) ResetSession(_ context.Context, conversationID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.resetConversation = conversationID
	return f.resetSessionID, nil
}

func (f *fakeAgent) Info() agent.AgentInfo {
	if f.info.Name != "" {
		return f.info
	}
	return agent.AgentInfo{Name: "fake", Type: "test", Model: "mock", Command: "fake"}
}

func (f *fakeAgent) SetCwd(cwd string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastCwd = cwd
}

func (f *fakeAgent) wasChatCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chatCalled
}

func (f *fakeAgent) chatCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chatCalls
}

func (f *fakeAgent) lastChatConversationID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastConversationID
}

func (f *fakeAgent) lastChatMessage() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastMessage
}

func (f *fakeAgent) lastWorkingDir() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCwd
}

func (f *fakeAgent) resetConversationID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetConversation
}

type fakeCodexThreadAgent struct {
	fakeAgent
	threadID        string
	useConversation string
	useThreadID     string
	clearCalledWith string
	useErr          error
	modelStatus     agent.CodexModelStatus
	models          []agent.CodexModel
	modelErr        error
	quota           agent.CodexQuota
	quotaErr        error
}

type fakeVisibleCodexAgent struct {
	fakeCodexThreadAgent
	openCalls   int
	detachCalls int
	detachOK    bool
	openErr     error
}

type fakeClaudeSessionAgent struct {
	fakeAgent
	sessionID       string
	useConversation string
	useSessionID    string
	clearCalledWith string
	useErr          error
}

func (f *fakeClaudeSessionAgent) CurrentClaudeSession(conversationID string) (string, bool) {
	if f.sessionID == "" {
		return "", false
	}
	return f.sessionID, true
}

func (f *fakeClaudeSessionAgent) UseClaudeSession(_ context.Context, conversationID string, sessionID string) error {
	f.useConversation = conversationID
	f.useSessionID = sessionID
	if f.useErr != nil {
		return f.useErr
	}
	f.sessionID = sessionID
	return nil
}

func (f *fakeClaudeSessionAgent) ClearClaudeSession(conversationID string) {
	f.clearCalledWith = conversationID
	f.sessionID = ""
}

func (f *fakeVisibleCodexAgent) OpenVisibleCompanion(_ context.Context) error {
	f.openCalls++
	return f.openErr
}

func (f *fakeVisibleCodexAgent) DetachVisibleCompanion() bool {
	f.detachCalls++
	return f.detachOK
}

type recordedCodexAppOpen struct {
	command   string
	workspace string
}

type recordedCodexCLIResume struct {
	command   string
	workspace string
	threadID  string
}

type recordedClaudeCLIResume struct {
	command   string
	workspace string
	sessionID string
}

func (f *fakeCodexThreadAgent) CurrentCodexThread(conversationID string) (string, bool) {
	if f.threadID == "" {
		return "", false
	}
	return f.threadID, true
}

func (f *fakeCodexThreadAgent) UseCodexThread(_ context.Context, conversationID string, threadID string) error {
	f.useConversation = conversationID
	f.useThreadID = threadID
	if f.useErr != nil {
		return f.useErr
	}
	f.threadID = threadID
	return nil
}

func (f *fakeCodexThreadAgent) ClearCodexThread(conversationID string) {
	f.clearCalledWith = conversationID
	f.threadID = ""
}

func (f *fakeCodexThreadAgent) CodexModelStatus() agent.CodexModelStatus {
	return f.modelStatus
}

func (f *fakeCodexThreadAgent) ListCodexModels(_ context.Context) ([]agent.CodexModel, error) {
	if f.modelErr != nil {
		return nil, f.modelErr
	}
	return f.models, nil
}

func (f *fakeCodexThreadAgent) ReadCodexQuota(_ context.Context) (agent.CodexQuota, error) {
	if f.quotaErr != nil {
		return agent.CodexQuota{}, f.quotaErr
	}
	return f.quota, nil
}

type fakeProgressAgent struct {
	fakeAgent
	progressCalled bool
	progressDeltas []string
	delay          time.Duration
}

func (f *fakeProgressAgent) ChatWithProgress(_ context.Context, _ string, _ string, onProgress func(delta string)) (string, error) {
	f.progressCalled = true
	for _, delta := range f.progressDeltas {
		if onProgress != nil {
			onProgress(delta)
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.reply, f.err
}

type blockingProgressAgent struct {
	fakeAgent
	mu        sync.Mutex
	started   int
	active    int
	maxActive int
	entered   chan struct{}
	release   chan struct{}
}

func newBlockingProgressAgent() *blockingProgressAgent {
	return &blockingProgressAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
}

func (f *blockingProgressAgent) ChatWithProgress(ctx context.Context, _ string, _ string, _ func(delta string)) (string, error) {
	callIndex := f.markStarted()
	f.entered <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
		f.markFinished()
		return "", ctx.Err()
	}
	f.markFinished()
	return fmt.Sprintf("第%d条结果", callIndex), nil
}

func (f *blockingProgressAgent) markStarted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	return f.started
}

func (f *blockingProgressAgent) markFinished() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
}

func (f *blockingProgressAgent) stats() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.maxActive
}

type blockingCodexThreadAgent struct {
	fakeCodexThreadAgent
	mu               sync.Mutex
	started          int
	active           int
	maxActive        int
	entered          chan struct{}
	release          chan struct{}
	threads          map[string]string
	conversationCwds map[string]string
}

func newBlockingCodexThreadAgent() *blockingCodexThreadAgent {
	return &blockingCodexThreadAgent{
		fakeCodexThreadAgent: fakeCodexThreadAgent{
			fakeAgent: fakeAgent{
				info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			},
		},
		entered:          make(chan struct{}, 4),
		release:          make(chan struct{}),
		threads:          make(map[string]string),
		conversationCwds: make(map[string]string),
	}
}

func (f *blockingCodexThreadAgent) ChatWithProgress(ctx context.Context, conversationID string, _ string, _ func(delta string)) (string, error) {
	callIndex := f.markStarted(conversationID)
	f.entered <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
		f.markFinished()
		return "", ctx.Err()
	}
	f.markFinished()
	return fmt.Sprintf("第%d条结果", callIndex), nil
}

func (f *blockingCodexThreadAgent) CurrentCodexThread(conversationID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	threadID := f.threads[conversationID]
	return threadID, threadID != ""
}

func (f *blockingCodexThreadAgent) UseCodexThread(_ context.Context, conversationID string, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.useConversation = conversationID
	f.useThreadID = threadID
	if f.useErr != nil {
		return f.useErr
	}
	f.threads[conversationID] = threadID
	return nil
}

func (f *blockingCodexThreadAgent) ClearCodexThread(conversationID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearCalledWith = conversationID
	delete(f.threads, conversationID)
}

func (f *blockingCodexThreadAgent) SetConversationCwd(conversationID string, cwd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversationCwds[conversationID] = cwd
}

func (f *blockingCodexThreadAgent) markStarted(conversationID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.threads[conversationID] = fmt.Sprintf("thread-generated-%d", f.started)
	return f.started
}

func (f *blockingCodexThreadAgent) markFinished() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
}

func (f *blockingCodexThreadAgent) conversationCwd(conversationID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conversationCwds[conversationID]
}

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

func TestHandlePlatformMessageUsesPlatformReplier(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformWeChat,
		AccountID: "bot-1",
		UserID:    "user-1",
		ChatID:    "user-1",
		MessageID: "9001",
		Text:      "/status",
	}, reply)

	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "agent:") {
		t.Fatalf("platform reply texts=%#v, want status reply", reply.Texts)
	}
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

func TestParseCommand_NoPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("hello world")
	if len(names) != 0 {
		t.Errorf("expected nil names, got %v", names)
	}
	if msg != "hello world" {
		t.Errorf("expected full text, got %q", msg)
	}
}

func TestParseCommand_SlashWithAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_AtPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_MultiAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cx hello")
	if len(names) != 2 || names[0] != "claude" || names[1] != "codex" {
		t.Errorf("expected [claude codex], got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_MultiAgentDedup(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cc hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] (deduped), got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_SwitchOnly(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestParseCommand_Alias(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/cc write a function")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from /cc alias, got %v", names)
	}
	if msg != "write a function" {
		t.Errorf("expected 'write a function', got %q", msg)
	}
}

func TestParseCommand_CustomAlias(t *testing.T) {
	h := newTestHandler()
	h.customAliases = map[string]string{"ai": "claude", "c": "claude"}
	names, msg := h.parseCommand("/ai hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from custom alias, got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_AbsolutePathIsPlainText(t *testing.T) {
	h := newTestHandler()
	text := "/Volumes/Data/code/MyCode/cc-switch/codex-switch.sh看下具体实现"

	names, msg := h.parseCommand(text)

	if len(names) != 0 {
		t.Fatalf("absolute path should not parse as agent command, names=%v", names)
	}
	if msg != text {
		t.Fatalf("message=%q, want original text", msg)
	}
}

func TestResolveAlias(t *testing.T) {
	h := newTestHandler()
	tests := map[string]string{
		"cc":  "claude",
		"cx":  "codex",
		"oc":  "openclaw",
		"cs":  "cursor",
		"km":  "kimi",
		"gm":  "gemini",
		"ocd": "opencode",
	}
	for alias, want := range tests {
		got := h.resolveAlias(alias)
		if got != want {
			t.Errorf("resolveAlias(%q) = %q, want %q", alias, got, want)
		}
	}
	if got := h.resolveAlias("unknown"); got != "unknown" {
		t.Errorf("resolveAlias(unknown) = %q, want %q", got, "unknown")
	}
	h.customAliases = map[string]string{"cc": "custom-claude"}
	if got := h.resolveAlias("cc"); got != "custom-claude" {
		t.Errorf("resolveAlias(cc) with custom = %q, want custom-claude", got)
	}
}

func TestBuildHelpText(t *testing.T) {
	text := buildHelpText()
	if text == "" {
		t.Error("help text is empty")
	}
	for _, want := range []string{
		"WeClaw 帮助",
		"常用：",
		"Codex：",
		"发送消息：",
		"更多：",
		"/status 查看 WeClaw 运行态",
		"/new 新建会话",
		"/cwd <路径> 切换工作目录",
		"/cx status 查看 Codex 会话状态",
		"/cx quota 查看 Codex 账号额度",
		"/cx ls",
		"/cx <编号|..> 选择或返回",
		"/cx cli 打开本地 CLI",
		"/cx app 打开 Codex App",
		"/codex <内容> 发给 Codex",
		"/cc <内容> 发给 Claude",
		"@cc @cx <内容> 同时发送",
		"/cx help Codex 高级命令",
		"/progress 查看进度模式",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help text should mention %q", want)
		}
	}
	if strings.Contains(text, "Available commands") || strings.Contains(text, "Aliases:") {
		t.Error("help text should not use old English headings")
	}
	if strings.Contains(text, "/codex where") || strings.Contains(text, "/codex workspace") {
		t.Error("help text should not mention old Codex session commands")
	}
	for _, hidden := range []string{
		"Codex 主路径：",
		"指定 Agent：",
		"常用别名：",
		"高级能力：",
		"Codex 账号：",
		"/info",
		"/cx usage",
		"/guide",
		"/run",
		"/cancel",
		"/claude 任务",
		"/cs = /cursor",
		"/km = /kimi",
		"/gm = /gemini",
		"/progress、/sw",
		"/sw reload",
		"/cx attach app",
		"/cx detach",
		"/progress 查看或切换进度模式",
	} {
		if strings.Contains(text, hidden) {
			t.Errorf("main help should hide advanced command %q", hidden)
		}
	}
	for _, want := range []string{
		"常用：\n\n/status 查看 WeClaw 运行态",
		"/status 查看 WeClaw 运行态\n\n/new 新建会话",
		"Codex：\n\n/cx status 查看 Codex 会话状态",
		"/cx ls 查看列表\n\n/cx <编号|..> 选择或返回",
		"发送消息：\n\n/codex <内容> 发给 Codex",
		"更多：\n\n/cx help Codex 高级命令",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help text should use blank lines for WeChat rendering, missing %q", want)
		}
	}
}

func TestBuildCodexSessionHelpTextIncludesDescriptions(t *testing.T) {
	text := buildCodexSessionHelpText()
	for _, want := range []string{
		"/cx ls 查看工作空间或当前工作空间会话",
		"/cx <编号|..> 选择当前列表项或返回上一级",
		"/cx cd <编号|工作空间名|..> 进入工作空间或返回工作空间列表",
		"/cx switch <编号> 切换当前工作空间会话",
		"/cx new 新建当前工作空间会话",
		"/cx pwd 查看当前工作空间",
		"/cx cli 打开本地 CLI 接手当前 thread",
		"/cx app 打开 Codex App 到当前工作空间",
		"/cx status 查看 remote、thread 和本地入口状态",
		"/cx quota 查看 Codex 账号额度",
		"/cx clean 清理已不存在的 WeClaw 工作空间记录",
		"/cx model status 查看 Codex 模型状态",
		"/cx model ls 查看可用 Codex 模型",
		"/codex 可作为 /cx 的兼容写法",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Codex help should describe %q, got %q", want, text)
		}
	}
}

func TestCodexCleanRemovesMissingStoredWorkspaces(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	missingWorkspace := filepath.Join(t.TempDir(), "missing")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-current",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetCodexLocalSessionDir(t.TempDir())
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, missingWorkspace, "thread-missing")
	h.codexSessions.setActiveWorkspace(bindingKey, missingWorkspace)
	h.setCodexBrowseWorkspace(bindingKey, missingWorkspace)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(109, "/cx clean"))

	texts := calls.texts()
	if !containsText(texts, "已清理 Codex 工作空间：1 个") {
		t.Fatalf("clean reply should include removed count, messages=%#v", texts)
	}
	if !containsText(texts, filepath.Base(missingWorkspace)) {
		t.Fatalf("clean reply should include removed workspace name, messages=%#v", texts)
	}
	if thread, _ := h.codexSessions.getThread(bindingKey, missingWorkspace); thread != "" {
		t.Fatalf("missing workspace thread=%q, want empty after clean", thread)
	}
	if browse, ok := h.codexBrowseWorkspace(bindingKey); ok || browse != "" {
		t.Fatalf("browse workspace=(%q,%v), want cleared", browse, ok)
	}
}

func TestStatusCommandUsesGlobalStatusAndInfoDoesNotCallAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{
		reply: "默认回复",
		info:  agent.AgentInfo{Name: "codex", Type: "acp", Model: "gpt-test", Command: "codex"},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(130, "/status"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(131, "/info"))

	texts := calls.texts()
	if !containsText(texts, "agent: codex") || !containsText(texts, "type: acp") {
		t.Fatalf("status reply mismatch, messages=%#v", texts)
	}
	if !containsText(texts, "请使用 /status") {
		t.Fatalf("info migration reply mismatch, messages=%#v", texts)
	}
	if ag.chatCallCount() != 0 {
		t.Fatalf("/info should not call default agent, calls=%d", ag.chatCallCount())
	}
}

func TestStatusCommandShowsDefaultModelWhenModelEmpty(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}

	text := h.buildStatus()

	if !strings.Contains(text, "model: (Agent 默认)") {
		t.Fatalf("status should show default model, got %q", text)
	}
}

func TestCommandRepliesUseBlankLinesForWeChat(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Model: "gpt-test", Command: "codex"},
	}

	tests := map[string]string{
		"status":      h.buildStatus(),
		"cwd":         h.handleCwd("/cwd"),
		"progress":    h.handleProgressCommand("/progress"),
		"progressErr": h.handleProgressCommand("/progress unknown"),
		"codexHelp":   buildCodexSessionHelpText(),
	}

	for name, text := range tests {
		if strings.Contains(text, "\n") && !strings.Contains(text, "\n\n") {
			t.Fatalf("%s reply should use blank lines for WeChat rendering, got %q", name, text)
		}
	}
}

func TestCodexWorkspaceRepliesUseBlankLinesForWeChat(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(t.TempDir())
	bindingKey := codexBindingKey("user-1", "codex")
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setPendingNew(bindingKey, workspaceB)

	where := h.renderCodexWhoami(bindingKey, workspaceA)
	if !strings.Contains(where, "workspace: "+workspaceA+"\n\nthread: thread-a") {
		t.Fatalf("where reply should separate fields with blank lines, got %q", where)
	}

	list := h.renderCodexList(bindingKey)
	for _, want := range []string{
		"Codex 工作空间:\n\n0. ",
		filepath.Base(workspaceA),
		filepath.Base(workspaceB),
	} {
		if !strings.Contains(list, want) {
			t.Fatalf("workspace reply missing %q, got %q", want, list)
		}
	}
	if strings.Contains(list, "thread-a") || strings.Contains(list, workspaceA) {
		t.Fatalf("workspace reply should hide thread ids and full paths, got %q", list)
	}
}

func TestHandleProgressCommandShowsCurrentMode(t *testing.T) {
	h := NewHandler(nil, nil)

	reply := h.handleProgressCommand("/progress")

	if !strings.Contains(reply, "当前进度模式：typing") {
		t.Fatalf("reply=%q, want current typing mode", reply)
	}
}

func TestHandleProgressCommandChangesMode(t *testing.T) {
	h := NewHandler(nil, nil)

	reply := h.handleProgressCommand("/progress stream")

	if !strings.Contains(reply, "已切换进度模式：stream") {
		t.Fatalf("reply=%q, want switched stream mode", reply)
	}
	if got := h.resolveProgressConfig("").Mode; got != progressModeStream {
		t.Fatalf("progress mode=%q, want stream", got)
	}
}

func TestHandleProgressCommandRejectsUnknownMode(t *testing.T) {
	h := NewHandler(nil, nil)

	reply := h.handleProgressCommand("/progress noisy")

	if !strings.Contains(reply, "不支持的进度模式") {
		t.Fatalf("reply=%q, want unsupported mode message", reply)
	}
	if got := h.resolveProgressConfig("").Mode; got != progressModeTyping {
		t.Fatalf("progress mode=%q, want unchanged typing", got)
	}
}

func TestResolveProgressConfigForPlatformUsesPlatformOverride(t *testing.T) {
	h := NewHandler(nil, nil)
	globalCfg := config.DefaultProgressConfig()
	globalCfg.Mode = progressModeTyping
	agentCfg := config.ProgressConfig{Mode: progressModeStream}
	platformCfg := config.ProgressConfig{Mode: progressModeSummary}

	h.SetProgressConfig(globalCfg)
	h.SetAgentProgressConfigs(map[string]config.ProgressConfig{"codex": agentCfg})
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		string(platform.PlatformFeishu): platformCfg,
	})

	got := h.resolveProgressConfigForPlatform(platform.PlatformFeishu, "codex")
	if got.Mode != progressModeSummary {
		t.Fatalf("progress mode=%q, want platform override %q", got.Mode, progressModeSummary)
	}
}

func TestHandleMessageUsesPlatformDefaultAgent(t *testing.T) {
	codex := &fakeAgent{reply: "codex reply", info: agent.AgentInfo{Name: "codex", Type: "test"}}
	claude := &fakeAgent{reply: "claude reply", info: agent.AgentInfo{Name: "claude", Type: "test"}}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		switch name {
		case "claude":
			return claude
		case "codex":
			return codex
		default:
			return nil
		}
	}, nil)
	h.SetDefaultAgent("codex", codex)
	h.SetPlatformDefaultAgents(map[string]string{
		string(platform.PlatformFeishu): "claude",
	})

	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "user-1",
		Text:     "hello",
	}, reply)

	if !claude.wasChatCalled() {
		t.Fatal("claude was not called for feishu platform default agent")
	}
	if codex.chatCallCount() != 0 {
		t.Fatalf("codex calls=%d, want 0", codex.chatCallCount())
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

func TestStartProgressSessionSummaryModeDoesNotSendRealtimeSnippet(t *testing.T) {
	h := NewHandler(nil, nil)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeSummary
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	reply := wechat.NewReplier(client, "user-1", "ctx-1", "")
	onProgress, stop := h.startProgressSession(context.Background(), reply, "", "修复实时回复碎片化", cfg)

	onProgress("这里是一段 Codex 正文 delta")
	waitForText(t, calls, "处理中，请耐心等待")
	stop()

	for _, text := range calls.texts() {
		if strings.Contains(text, "这里是一段 Codex 正文 delta") {
			t.Fatalf("summary mode should not send raw delta, got messages %#v", calls.texts())
		}
		if strings.Contains(text, "实时片段") {
			t.Fatalf("summary mode should not send realtime snippet, got messages %#v", calls.texts())
		}
	}
}

func TestStartProgressSessionDefaultTypingModeDoesNotSendTextFeedback(t *testing.T) {
	h := NewHandler(nil, nil)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	cfg := config.DefaultProgressConfig()
	reply := wechat.NewReplier(client, "user-1", "ctx-1", "")
	onProgress, stop := h.startProgressSession(context.Background(), reply, "", "查询当前工作目录", cfg)

	onProgress("正在生成结果")
	time.Sleep(taskQueueProbeDelay)
	stop()

	if texts := calls.texts(); len(texts) != 0 {
		t.Fatalf("default typing mode should not send progress text, got %#v", texts)
	}
	if typings := calls.typings(); len(typings) == 0 {
		t.Fatal("default typing mode should still send typing status")
	}
}

func TestStartProgressSessionStreamModeKeepsLegacySnippet(t *testing.T) {
	h := NewHandler(nil, nil)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	reply := wechat.NewReplier(client, "user-1", "ctx-1", "")
	onProgress, stop := h.startProgressSession(context.Background(), reply, "", "修复实时回复碎片化", cfg)

	onProgress("第一段第二段第三段")
	waitForText(t, calls, "实时片段，仅供预览")
	stop()
}

func TestSendToNamedAgentUsesAgentProgressOverride(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["codex"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "最终结果"},
		progressDeltas: []string{"第一段第二段第三段"},
		delay:          50 * time.Millisecond,
	}
	globalCfg := config.DefaultProgressConfig()
	globalCfg.EnableTyping = boolPtr(false)
	globalCfg.InitialDelaySeconds = 0
	globalCfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(globalCfg)
	streamCfg := config.ProgressConfig{Mode: progressModeStream}
	h.SetAgentProgressConfigs(map[string]config.ProgressConfig{"codex": streamCfg})

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.sendToNamedAgent(context.Background(), platform.PlatformWeChat, "user-1", reply, "codex", "hello", "client-1")

	waitForText(t, calls, "实时片段，仅供预览")
}

func TestSendToNamedAgentSerializesSameExecutionKey(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}
	h.agents["claude"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", reply, "claude", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	secondDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-2")
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", reply, "claude", "第二条", "client-2")
		close(secondDone)
	}()
	time.Sleep(50 * time.Millisecond)
	started, maxActive := ag.stats()
	if started != 1 || maxActive != 1 {
		t.Fatalf("并发进入 Codex: started=%d maxActive=%d", started, maxActive)
	}

	ag.release <- struct{}{}
	waitDone(t, firstDone, "第一条任务")
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitDone(t, secondDone, "第二条任务")

	texts := calls.texts()
	firstIndex := textIndex(texts, "第1条结果")
	secondIndex := textIndex(texts, "第2条结果")
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("回复顺序错误，messages=%#v", texts)
	}
}

func TestSendToNamedAgentUsesTaskTimeout(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "slow", Type: "cli", Command: "slow"}
	h.agents["slow"] = ag
	h.SetProgressConfig(progressConfigWithTaskTimeout())

	runWithExpectedTaskTimeout(t, func(ctx context.Context) {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", reply, "slow", "hello", "client-1")
	})
	waitForText(t, calls, "context deadline exceeded")
}

func TestSendToDefaultAgentUsesTaskTimeout(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "slow", Type: "cli", Command: "slow"}
	h.SetDefaultAgent("slow", ag)
	h.SetProgressConfig(progressConfigWithTaskTimeout())

	runWithExpectedTaskTimeout(t, func(ctx context.Context) {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToDefaultAgent(ctx, platform.PlatformWeChat, "user-1", reply, "hello", "client-1")
	})
	waitForText(t, calls, "context deadline exceeded")
}

func TestBroadcastToAgentsUsesTaskTimeout(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "slow", Type: "cli", Command: "slow"}
	h.agents["slow"] = ag
	h.SetProgressConfig(progressConfigWithTaskTimeout())

	runWithExpectedTaskTimeout(t, func(ctx context.Context) {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.broadcastToAgents(ctx, platform.PlatformWeChat, "user-1", reply, []string{"slow"}, "hello")
	})
	waitForText(t, calls, "context deadline exceeded")
}

func TestBroadcastToRunningCodexReturnsGuideWithoutBlockingOtherAgents(t *testing.T) {
	h := NewHandler(nil, nil)
	codex := newBlockingProgressAgent()
	h.agents["codex"] = codex
	h.agents["claude"] = &fakeAgent{
		reply: "claude ok",
		info:  agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-1"), "codex", "第一条", "client-1")
	waitForAgentEnter(t, codex)

	done := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-2")
		h.broadcastToAgents(ctx, platform.PlatformWeChat, "user-1", reply, []string{"codex", "claude"}, "第二条")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("broadcast should not block behind running Codex task")
	}
	waitForText(t, calls, "Codex 正在处理上一条任务")
	waitForText(t, calls, "[claude] claude ok")

	codex.release <- struct{}{}
}

func TestRunningCodexStoresSecondMessageAsPendingGuide(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", reply, "codex", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), "codex", "第二条", "client-2")
	started, _ := ag.stats()
	if started != 1 {
		t.Fatalf("第二条消息不应立即进入 Codex，started=%d", started)
	}
	if !containsText(calls.texts(), "回复 /guide 将此消息作为引导对话发送给 Codex") {
		t.Fatalf("未发送引导确认提示，messages=%#v", calls.texts())
	}

	ag.release <- struct{}{}
	waitDone(t, firstDone, "第一条任务")
}

func TestCodexBackgroundTaskRecordsFrozenWorkspaceAfterSwitch(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingCodexThreadAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspace A: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspace B: %v", err)
	}
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setPendingNew(bindingKey, workspaceA)
	h.codexSessions.setActiveWorkspace(bindingKey, workspaceA)
	h.codexSessions.setThread(bindingKey, workspaceB, "thread-b")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	handleTestWeChatMessage(h, ctx, client, newTextMessage(10, "/codex A 任务"))
	waitForCodexThreadAgentEnter(t, ag)

	conversationA := buildCodexConversationID("user-1", "codex", workspaceA)
	if got := ag.conversationCwd(conversationA); got != normalizeCodexWorkspaceRoot(workspaceA) {
		t.Fatalf("conversation cwd=%q, want %q", got, normalizeCodexWorkspaceRoot(workspaceA))
	}

	handleTestWeChatMessage(h, ctx, client, newTextMessage(11, "/cx switch thread-b"))
	handleTestWeChatMessage(h, ctx, client, newTextMessage(12, "/guide"))

	ag.release <- struct{}{}
	waitForText(t, calls, "第1条结果")

	active, ok := h.codexSessions.getActiveWorkspace(bindingKey)
	if !ok || active != normalizeCodexWorkspaceRoot(workspaceB) {
		t.Fatalf("active workspace=(%q,%v), want %q true", active, ok, normalizeCodexWorkspaceRoot(workspaceB))
	}
	threadA, pendingA := h.codexSessions.getThread(bindingKey, workspaceA)
	if threadA != "thread-generated-1" || pendingA {
		t.Fatalf("workspace A thread=%q pending=%v, want thread-generated-1 false", threadA, pendingA)
	}
	threadB, pendingB := h.codexSessions.getThread(bindingKey, workspaceB)
	if threadB != "thread-b" || pendingB {
		t.Fatalf("workspace B thread=%q pending=%v, want thread-b false", threadB, pendingB)
	}
	if !containsText(calls.texts(), "当前没有可发送的引导对话") {
		t.Fatalf("/guide should target current B session, messages=%#v", calls.texts())
	}
}

func TestCodexHandlerReturnsWhileTaskRunsSoGuideCanBeStored(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		handleTestWeChatMessage(h, ctx, client, newTextMessage(1, "/codex 第一条"))
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	select {
	case <-firstDone:
	case <-time.After(50 * time.Millisecond):
		ag.release <- struct{}{}
		waitDone(t, firstDone, "第一条任务")
		t.Fatal("Codex Handler 应在任务后台运行后返回，避免串行消息入口阻塞 /guide")
	}

	handleTestWeChatMessage(h, ctx, client, newTextMessage(2, "/codex 第二条"))
	started, _ := ag.stats()
	if started != 1 {
		t.Fatalf("第二条消息不应立即进入 Codex，started=%d", started)
	}
	if !containsText(calls.texts(), "回复 /guide 将此消息作为引导对话发送给 Codex") {
		t.Fatalf("未发送引导确认提示，messages=%#v", calls.texts())
	}

	ag.release <- struct{}{}
	waitForText(t, calls, "第1条结果")
}

func TestGuideSendsPendingMessageAndSuppressesFirstReply(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", reply, "codex", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), "codex", "第二条", "client-2")

	guideDone := make(chan struct{})
	go func() {
		handleTestWeChatMessage(h, ctx, client, newTextMessage(3, "/guide"))
		close(guideDone)
	}()
	waitDone(t, firstDone, "第一条监听")
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitDone(t, guideDone, "引导命令")
	waitForText(t, calls, "第2条结果")

	texts := calls.texts()
	if containsText(texts, "第1条结果") {
		t.Fatalf("第一条任务被引导接管后不应发送最终结果，messages=%#v", texts)
	}
	if !containsText(texts, "第2条结果") {
		t.Fatalf("未发送引导后的最终结果，messages=%#v", texts)
	}
}

func TestCancelWithdrawsPendingGuideAndKeepsRunningTask(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", reply, "codex", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), "codex", "第二条", "client-2")

	handleTestWeChatMessage(h, ctx, client, newTextMessage(3, "/cancel"))
	ag.release <- struct{}{}
	waitDone(t, firstDone, "第一条任务")
	waitForText(t, calls, "第1条结果")

	started, _ := ag.stats()
	if started != 1 {
		t.Fatalf("/cancel 只应撤回暂存消息，不应启动第二条，started=%d", started)
	}
	texts := calls.texts()
	if !containsText(texts, "已撤回该消息。") {
		t.Fatalf("未发送撤回提示，messages=%#v", texts)
	}
	if !containsText(texts, "第1条结果") {
		t.Fatalf("撤回暂存消息后应继续返回第一条结果，messages=%#v", texts)
	}
}

func TestPendingGuideBecomesRunnableAfterTaskFinishes(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-1"), "codex", "第一条", "client-1")
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), "codex", "第二条", "client-2")

	ag.release <- struct{}{}
	waitForText(t, calls, "第1条结果")
	waitForText(t, calls, "回复 /run 执行该消息")

	handleTestWeChatMessage(h, ctx, client, newTextMessage(3, "/run"))
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitForText(t, calls, "第2条结果")

	started, _ := ag.stats()
	if started != 2 {
		t.Fatalf("/run 应执行暂存消息，started=%d", started)
	}
}

func TestCancelWithdrawsRunnablePendingGuide(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-1"), "codex", "第一条", "client-1")
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), "codex", "第二条", "client-2")

	ag.release <- struct{}{}
	waitForText(t, calls, "回复 /run 执行该消息")
	handleTestWeChatMessage(h, ctx, client, newTextMessage(3, "/cancel"))
	waitForText(t, calls, "已撤回该消息。")

	handleTestWeChatMessage(h, ctx, client, newTextMessage(4, "/run"))
	waitForText(t, calls, "当前没有待执行的暂存消息。")

	started, _ := ag.stats()
	if started != 1 {
		t.Fatalf("撤回后不应执行暂存消息，started=%d", started)
	}
}

func TestBroadcastProgressUsesAgentPrefix(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["codex"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "codex ok"},
		progressDeltas: []string{"codex delta"},
		delay:          50 * time.Millisecond,
	}
	h.agents["claude"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "claude ok"},
		progressDeltas: []string{"claude delta"},
		delay:          50 * time.Millisecond,
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.broadcastToAgents(context.Background(), platform.PlatformWeChat, "user-1", reply, []string{"codex", "claude"}, "hello")

	if !containsText(calls.texts(), "[codex] 实时片段，仅供预览") {
		t.Fatalf("expected codex progress prefix, messages=%#v", calls.texts())
	}
	if !containsText(calls.texts(), "[claude] 实时片段，仅供预览") {
		t.Fatalf("expected claude progress prefix, messages=%#v", calls.texts())
	}
}

func TestDuplicateTextFallbackWhenMessageIDZero(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "ok"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	msg := newTextMessage(0, "同一个任务")

	handleTestWeChatMessage(h, context.Background(), client, msg)
	handleTestWeChatMessage(h, context.Background(), client, msg)

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("MessageID=0 duplicate text should only start agent once, chatCalls=%d", ag.chatCallCount())
	}
}

func TestDuplicateMessageIDStillDeduped(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "ok"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	msg := newTextMessage(99, "同一个任务")

	handleTestWeChatMessage(h, context.Background(), client, msg)
	handleTestWeChatMessage(h, context.Background(), client, msg)

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("same MessageID should only start agent once, chatCalls=%d", ag.chatCallCount())
	}
}

func TestHandleMessage_AbsolutePathTextGoesToDefaultAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "ok"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	text := "/Volumes/Data/code/MyCode/cc-switch/codex-switch.sh看下具体实现"

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(100, text))

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("absolute path text should call default agent once, chatCalls=%d", ag.chatCallCount())
	}
	if ag.lastChatMessage() != text {
		t.Fatalf("agent message=%q, want original text", ag.lastChatMessage())
	}
	if containsText(calls.texts(), "Usage: specify one agent") {
		t.Fatalf("absolute path text should not reply usage, messages=%#v", calls.texts())
	}
}

func TestHandleMessageRemovedSwitchCommandDoesNotCallAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "ok"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(101, "/sw reload"))

	if ag.chatCallCount() != 0 {
		t.Fatalf("removed /sw command should not call default agent, chatCalls=%d", ag.chatCallCount())
	}
	if !containsText(calls.texts(), "不再支持从微信端切换 Codex 账号") {
		t.Fatalf("removed /sw command should explain removal, messages=%#v", calls.texts())
	}
}

func TestSendToNamedCodexUsesWorkspaceConversationAndRecordsThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			reply: "ok",
			info:  agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-1",
	}
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.sendToNamedAgent(context.Background(), platform.PlatformWeChat, "user-1", reply, "codex", "hello", "client-1")

	waitForFakeAgentCalls(t, &ag.fakeAgent, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("codex chat calls=%d, want 1", ag.chatCallCount())
	}
	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.lastChatConversationID() != wantConversationID {
		t.Fatalf("conversationID=%q, want %q", ag.lastChatConversationID(), wantConversationID)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-1" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-1 false", thread, pending)
	}
}

func TestHandleCodexNewCommandClearsWorkspaceThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-old",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-old")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(102, "/codex new"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.clearCalledWith != wantConversationID {
		t.Fatalf("clear conversationID=%q, want %q", ag.clearCalledWith, wantConversationID)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "" || !pending {
		t.Fatalf("stored thread=%q pending=%v, want empty true", thread, pending)
	}
	if !containsText(calls.texts(), "已切换到新会话") {
		t.Fatalf("reply should mention new session, messages=%#v", calls.texts())
	}
}

func TestHandleGlobalNewResetsActiveCodexWorkspaceThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info:           agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			resetSessionID: "thread-new",
		},
		threadID: "thread-old",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setActiveWorkspace(bindingKey, workspace)
	h.codexSessions.setThread(bindingKey, workspace, "thread-old")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(123, "/new"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.resetConversationID() != wantConversationID {
		t.Fatalf("reset conversation=%q, want %q", ag.resetConversationID(), wantConversationID)
	}
	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "thread-new" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-new false", thread, pending)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已创建新的codex会话") || strings.Contains(text, "/Users/") {
		t.Fatalf("reply should use default agent name, messages=%#v", calls.texts())
	}
}

func TestHandleGlobalNewResetsActiveClaudeWorkspaceSession(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info:           agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
			resetSessionID: "session-new",
		},
		sessionID: "session-old",
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	bindingKey := claudeBindingKey("user-1", "claude")
	h.claudeSessions.setActiveWorkspace(bindingKey, workspace)
	h.claudeSessions.setSession(bindingKey, workspace, "session-old")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(304, "/new"))

	wantConversationID := buildClaudeConversationID("user-1", "claude", workspace)
	if ag.resetConversationID() != wantConversationID {
		t.Fatalf("reset conversation=%q, want %q", ag.resetConversationID(), wantConversationID)
	}
	sessionID, pending := h.claudeSessions.getSession(bindingKey, workspace)
	if sessionID != "session-new" || pending {
		t.Fatalf("stored session=%q pending=%v, want session-new false", sessionID, pending)
	}
	if !containsText(calls.texts(), "已创建新的claude会话") {
		t.Fatalf("reply should mention new claude session, messages=%#v", calls.texts())
	}
}

func TestHandleCwdRecordsActiveClaudeWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	reply := h.handleCwd("/cwd "+workspace, "user-1")

	active, ok := h.claudeSessions.getActiveWorkspace(claudeBindingKey("user-1", "claude"))
	if !ok || active != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("active workspace=(%q,%v), want %q true; reply=%q", active, ok, normalizeClaudeWorkspaceRoot(workspace), reply)
	}
}

func TestHandleCodexSwitchCommandSetsWorkspaceThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(103, "/codex switch thread-2"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-2" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-2)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-2" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-2 false", thread, pending)
	}
	if !containsText(calls.texts(), "已切换会话") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchCommandSwitchesWorkspaceForKnownThread(t *testing.T) {
	h := NewHandler(nil, nil)
	currentWorkspace := t.TempDir()
	targetWorkspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), targetWorkspace, "thread-target")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(106, "/codex switch thread-target"))

	wantConversationID := buildCodexConversationID("user-1", "codex", targetWorkspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-target" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-target)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastWorkingDir() != targetWorkspace {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), targetWorkspace)
	}
	if got := h.codexWorkspaceRoot("codex"); got != targetWorkspace {
		t.Fatalf("handler workspace=%q, want %q", got, targetWorkspace)
	}
	if !containsText(calls.texts(), "工作空间: "+filepath.Base(targetWorkspace)) {
		t.Fatalf("reply should mention switched workspace, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchCommandAcceptsListIndex(t *testing.T) {
	h := NewHandler(nil, nil)
	root := t.TempDir()
	currentWorkspace := filepath.Join(root, "a")
	targetWorkspace := filepath.Join(root, "b")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, currentWorkspace, "thread-a")
	h.codexSessions.setThread(bindingKey, targetWorkspace, "thread-b")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(108, "/codex switch 1"))

	wantConversationID := buildCodexConversationID("user-1", "codex", targetWorkspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-b" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-b)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(targetWorkspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(targetWorkspace))
	}
	if !containsText(calls.texts(), "工作空间: "+filepath.Base(targetWorkspace)) {
		t.Fatalf("reply should mention switched workspace, messages=%#v", calls.texts())
	}
}

func TestDiscoverLocalCodexSessionsReadsIndexAndSessionMeta(t *testing.T) {
	codexDir := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "桌面会话 A", "2026-04-28T08:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "桌面会话 B", "2026-04-29T08:00:00Z")

	sessions := discoverLocalCodexSessions(codexDir)

	if len(sessions) != 2 {
		t.Fatalf("sessions len=%d, want 2: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "thread-b" || sessions[0].WorkspaceRoot != normalizeCodexWorkspaceRoot(workspaceB) {
		t.Fatalf("first session=%#v, want newest thread-b workspace-b", sessions[0])
	}
	if sessions[1].ThreadName != "桌面会话 A" {
		t.Fatalf("second thread name=%q, want 桌面会话 A", sessions[1].ThreadName)
	}
}

func TestDiscoverLocalCodexSessionsSkipsArchivedSessions(t *testing.T) {
	codexDir := t.TempDir()
	activeWorkspace := filepath.Join(t.TempDir(), "active")
	archivedWorkspace := filepath.Join(t.TempDir(), "archived")
	writeLocalCodexSession(t, codexDir, "thread-active", activeWorkspace, "活跃会话", "2026-04-29T09:00:00Z")
	writeArchivedLocalCodexSession(t, codexDir, "thread-archived", archivedWorkspace, "归档会话", "2026-04-29T08:00:00Z")

	sessions := discoverLocalCodexSessions(codexDir)

	if len(sessions) != 1 {
		t.Fatalf("sessions len=%d, want 1: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "thread-active" {
		t.Fatalf("session thread=%q, want thread-active", sessions[0].ThreadID)
	}
}

func TestDiscoverLocalCodexSessionsSkipsHiddenDesktopSessions(t *testing.T) {
	codexDir := t.TempDir()
	visibleWorkspace := filepath.Join(t.TempDir(), "visible")
	writeLocalCodexSession(t, codexDir, "thread-visible", visibleWorkspace, "桌面主会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSessionMeta(t, codexDir, "thread-subagent", filepath.Join(t.TempDir(), "subagent"), "2026-04-29T08:00:00Z", `"Codex Desktop"`, `"subagent"`, `{"subagent":{"thread_spawn":{"parent_thread_id":"thread-visible"}}}`)
	writeLocalCodexSessionMeta(t, codexDir, "thread-cli", filepath.Join(t.TempDir(), "cli"), "2026-04-29T07:00:00Z", `"codex-tui"`, `"user"`, `"vscode"`)
	writeLocalCodexIndex(t, codexDir, "thread-subagent", "子代理会话", "2026-04-29T08:00:00Z")
	writeLocalCodexIndex(t, codexDir, "thread-cli", "CLI 会话", "2026-04-29T07:00:00Z")

	sessions := discoverLocalCodexSessions(codexDir)

	if len(sessions) != 1 {
		t.Fatalf("sessions len=%d, want 1: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "thread-visible" {
		t.Fatalf("session thread=%q, want thread-visible", sessions[0].ThreadID)
	}
}

func TestDiscoverLocalCodexSessionsSkipsMissingWorkspace(t *testing.T) {
	codexDir := t.TempDir()
	existingWorkspace := filepath.Join(t.TempDir(), "existing")
	missingWorkspace := filepath.Join(t.TempDir(), "missing")
	if err := os.MkdirAll(existingWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir existing workspace: %v", err)
	}
	writeLocalCodexSession(t, codexDir, "thread-existing", existingWorkspace, "现存工作空间", "2026-04-29T09:00:00Z")
	writeLocalCodexIndex(t, codexDir, "thread-missing", "已删除工作空间", "2026-04-29T10:00:00Z")
	writeLocalCodexSessionMeta(t, codexDir, "thread-missing", missingWorkspace, "2026-04-29T10:00:00Z", `"Codex Desktop"`, `""`, `"vscode"`)

	sessions := discoverLocalCodexSessions(codexDir)

	if len(sessions) != 1 {
		t.Fatalf("sessions len=%d, want 1: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "thread-existing" {
		t.Fatalf("session thread=%q, want thread-existing", sessions[0].ThreadID)
	}
}

func TestDiscoverLocalClaudeSessionsReadsProjectTranscripts(t *testing.T) {
	claudeDir := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	writeLocalClaudeSession(t, claudeDir, "session-a", workspaceA, "功能 A", "2026-04-28T08:00:00Z")
	writeLocalClaudeSession(t, claudeDir, "session-b", workspaceB, "功能 B", "2026-04-29T08:00:00Z")

	sessions := discoverLocalClaudeSessions(claudeDir)

	if len(sessions) != 2 {
		t.Fatalf("sessions len=%d, want 2: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "session-b" || sessions[0].WorkspaceRoot != normalizeCodexWorkspaceRoot(workspaceB) {
		t.Fatalf("first session=%#v, want newest session-b workspace-b", sessions[0])
	}
	if sessions[1].ThreadName != "功能 A" {
		t.Fatalf("second session name=%q, want 功能 A", sessions[1].ThreadName)
	}
}

func TestDiscoverLocalClaudeSessionsSkipsMissingWorkspace(t *testing.T) {
	claudeDir := t.TempDir()
	existingWorkspace := filepath.Join(t.TempDir(), "existing")
	missingWorkspace := filepath.Join(t.TempDir(), "missing")
	writeLocalClaudeSession(t, claudeDir, "session-existing", existingWorkspace, "现存会话", "2026-04-29T09:00:00Z")
	writeLocalClaudeProjectConfig(t, claudeDir, missingWorkspace)
	writeLocalClaudeTranscript(t, claudeDir, missingWorkspace, "session-missing", "已删除会话", "2026-04-29T10:00:00Z")

	sessions := discoverLocalClaudeSessions(claudeDir)

	if len(sessions) != 1 {
		t.Fatalf("sessions len=%d, want 1: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "session-existing" {
		t.Fatalf("session id=%q, want session-existing", sessions[0].ThreadID)
	}
}

func TestSetClaudeSessionFileRestoresWorkspaceSession(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "claude-sessions.json")
	workspace := t.TempDir()
	bindingKey := claudeBindingKey("user-1", "claude")
	first := NewHandler(nil, nil)
	first.SetClaudeSessionFile(stateFile)
	first.ensureClaudeSessions().setActiveWorkspace(bindingKey, workspace)
	first.ensureClaudeSessions().setSession(bindingKey, workspace, "session-restored")

	second := NewHandler(nil, nil)
	second.SetClaudeSessionFile(stateFile)

	sessionID, pending := second.ensureClaudeSessions().getSession(bindingKey, workspace)
	if sessionID != "session-restored" || pending {
		t.Fatalf("restored session=(%q,%v), want session-restored false", sessionID, pending)
	}
	active, ok := second.ensureClaudeSessions().getActiveWorkspace(bindingKey)
	if !ok || active != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("restored active workspace=(%q,%v), want %q true", active, ok, normalizeClaudeWorkspaceRoot(workspace))
	}
}

func TestClaudeCcLsIncludesLocalSessionsAndHidesSessionIDs(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "local")
	writeLocalClaudeSession(t, claudeDir, "session-local", workspace, "本机会话", "2026-04-29T09:00:00Z")
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(301, "/cc ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "Claude 会话") || !strings.Contains(text, "0. local / 本机会话") {
		t.Fatalf("ls should show local session, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "session-local") {
		t.Fatalf("session ls should hide session id, messages=%#v", calls.texts())
	}
}

func TestClaudeCcLsNumberMatchesSwitchIndexAcrossSortedWorkspaces(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "aaa")
	workspaceZ := filepath.Join(t.TempDir(), "zzz")
	writeLocalClaudeSession(t, claudeDir, "session-old", workspaceA, "较早会话", "2026-04-28T09:00:00Z")
	writeLocalClaudeSession(t, claudeDir, "session-new", workspaceZ, "较新会话", "2026-04-29T09:00:00Z")
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(303, "/cc ls"))
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. zzz / 较新会话") {
		t.Fatalf("ls index 0 should show newest switch target, messages=%#v", calls.texts())
	}

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(304, "/cc switch 0"))

	wantConversationID := buildClaudeConversationID("user-1", "claude", workspaceZ)
	if ag.useConversation != wantConversationID || ag.useSessionID != "session-new" {
		t.Fatalf("use conversation/session=(%q,%q), want (%q,session-new)", ag.useConversation, ag.useSessionID, wantConversationID)
	}
}

func TestHandleClaudeSwitchCommandBindsLocalSessionIndex(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "desktop")
	writeLocalClaudeSession(t, claudeDir, "session-desktop", workspace, "桌面会话", "2026-04-29T09:00:00Z")
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(302, "/cc switch 0"))

	wantConversationID := buildClaudeConversationID("user-1", "claude", workspace)
	if ag.useConversation != wantConversationID || ag.useSessionID != "session-desktop" {
		t.Fatalf("use conversation/session=(%q,%q), want (%q,session-desktop)", ag.useConversation, ag.useSessionID, wantConversationID)
	}
	if ag.lastWorkingDir() != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("claude cwd=%q, want %q", ag.lastWorkingDir(), normalizeClaudeWorkspaceRoot(workspace))
	}
	if !containsText(calls.texts(), "已切换 Claude 会话") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
}

func TestHandleClaudeCliOpensCurrentSession(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	h.claudeSessions.setSession(claudeBindingKey("user-1", "claude"), workspace, "session-current")
	var opened []recordedClaudeCLIResume
	h.SetClaudeCLIResumeOpener(func(_ context.Context, command string, workspaceRoot string, sessionID string) error {
		opened = append(opened, recordedClaudeCLIResume{command: command, workspace: workspaceRoot, sessionID: sessionID})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(303, "/cc cli"))

	if len(opened) != 1 || opened[0].workspace != workspace || opened[0].sessionID != "session-current" {
		t.Fatalf("opened=%#v, want current session in workspace %s", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Claude CLI") {
		t.Fatalf("reply should mention opened cli, messages=%#v", calls.texts())
	}
}

func TestCodexLsIncludesLocalCodexSessionsAndDeduplicatesRecordedThread(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	recordedWorkspace := filepath.Join(t.TempDir(), "recorded")
	localWorkspace := filepath.Join(t.TempDir(), "local")
	writeLocalCodexSession(t, codexDir, "thread-recorded", recordedWorkspace, "重复会话", "2026-04-29T08:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-local", localWorkspace, "桌面本机会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": recordedWorkspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), recordedWorkspace, "thread-recorded")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(109, "/codex ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. local") || !strings.Contains(text, "1. recorded") {
		t.Fatalf("ls should include local and recorded workspace names, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-recorded") || strings.Contains(text, "来源:") {
		t.Fatalf("workspace ls should hide thread ids and source labels, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchCommandBindsLocalCodexSessionIndex(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "desktop")
	writeLocalCodexSession(t, codexDir, "thread-desktop", workspace, "桌面会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(110, "/codex switch 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-desktop" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-desktop)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-desktop" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-desktop false", thread, pending)
	}
	if !containsText(calls.texts(), "已切换会话") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchFailureDoesNotLeakThreadStoreErrorOrSwitchWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	currentWorkspace := filepath.Join(t.TempDir(), "current")
	localWorkspace := filepath.Join(t.TempDir(), "desktop")
	writeLocalCodexSession(t, codexDir, "thread-bad", localWorkspace, "桌面会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info:    agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			lastCwd: currentWorkspace,
		},
		useErr: fmt.Errorf("resume thread thread-bad: agent error: failed to read thread: thread-store internal error: failed to read thread /tmp/rollout.jsonl: rollout does not start with session metadata"),
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(116, "/codex switch 0"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "该 Codex 会话当前无法被微信接手") || !strings.Contains(text, "/cx app") {
		t.Fatalf("reply should explain local thread resume failure, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-store internal error") || strings.Contains(text, "session metadata") {
		t.Fatalf("reply should hide internal thread-store details, messages=%#v", calls.texts())
	}
	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(currentWorkspace) {
		t.Fatalf("codex cwd=%q, want unchanged %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(currentWorkspace))
	}
}

func TestCodexCxLsListsWorkspacesWithoutThreads(t *testing.T) {
	h := NewHandler(nil, nil)
	root := t.TempDir()
	workspaceA := filepath.Join(root, "weclaw")
	workspaceB := filepath.Join(root, "card-manager-android")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetCodexLocalSessionDir(t.TempDir())
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setThread(bindingKey, workspaceB, "thread-b")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(111, "/cx ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "Codex 工作空间") {
		t.Fatalf("ls should show workspace list, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "0. card-manager-android") || !strings.Contains(text, "1. weclaw") {
		t.Fatalf("ls should show workspace short names, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-a") || strings.Contains(text, workspaceA) {
		t.Fatalf("workspace ls should hide thread ids and full paths, messages=%#v", calls.texts())
	}
}

func TestCodexCxLsUsesCodexAppWorkspaceOrder(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	weclawWorkspace := filepath.Join(root, "weclaw")
	safariWorkspace := filepath.Join(root, "SafariCollection")
	tmpWorkspace := filepath.Join(root, "tmp")
	writeLocalCodexSession(t, codexDir, "thread-weclaw", weclawWorkspace, "WeClaw 会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-safari", safariWorkspace, "Safari 会话", "2026-04-29T08:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-tmp", tmpWorkspace, "历史临时会话", "2026-04-29T10:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{weclawWorkspace, safariWorkspace}, []string{weclawWorkspace, safariWorkspace})
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(150, "/cx ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. weclaw") || !strings.Contains(text, "1. SafariCollection") {
		t.Fatalf("ls should follow Codex App project order, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "tmp") {
		t.Fatalf("ls should hide workspaces not in Codex App project order, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceThenLsListsSessionsWithoutThreadIDs(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspace := filepath.Join(root, "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-local-a", workspace, "实现两级会话浏览", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-local-b", workspace, "修复安全问题", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(112, "/cx cd 0"))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	text := strings.Join(calls.texts(), "\n")
	if strings.Contains(text, "已进入工作空间") {
		t.Fatalf("cd reply should not include redundant title, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "工作空间: weclaw") || !strings.Contains(text, "weclaw 会话") {
		t.Fatalf("cd reply should enter workspace and show sessions, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "0. 实现两级会话浏览") || !strings.Contains(text, "1. 修复安全问题") {
		t.Fatalf("session ls should show numbered session names, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-local-a") || strings.Contains(text, "来源:") {
		t.Fatalf("session ls should hide thread ids and source labels, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceUsesCodexAppThreadList(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-jsonl-a", workspace, "JSONL 旧会话 A", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-jsonl-b", workspace, "JSONL 旧会话 B", "2026-04-29T08:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{workspace}, []string{workspace})
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake sqlite db: %v", err)
	}
	writeFakeSQLite3(t, `[{"id":"thread-app-new","title":"App 新会话\n第二行不展示","recency_at_ms":2000},{"id":"thread-app-old","title":"App 旧会话","recency_at_ms":1000}]`)
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(151, "/cx cd 0"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. App 新会话") || !strings.Contains(text, "1. App 旧会话") {
		t.Fatalf("session ls should use Codex App thread order, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "JSONL 旧会话") || strings.Contains(text, "第二行不展示") {
		t.Fatalf("session ls should hide JSONL fallback and multiline title tail, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceSkipsStoredArchivedThread(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-archived", workspace, "已归档旧缓存", "2026-04-29T09:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{workspace}, []string{workspace})
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake sqlite db: %v", err)
	}
	writeFakeSQLite3(t, `[{"id":"thread-visible","title":"App 可见会话","recency_at_ms":2000}]`)
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-archived")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(152, "/cx cd 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-visible" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-visible)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if containsText(calls.texts(), "thread-archived") || containsText(calls.texts(), "已归档旧缓存") {
		t.Fatalf("cd should ignore stored archived thread, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceClearsStaleStoredThread(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-archived", workspace, "已归档旧缓存", "2026-04-29T09:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{workspace}, []string{workspace})
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake sqlite db: %v", err)
	}
	writeFakeSQLite3(t, `[{"id":"thread-visible-a","title":"App 可见会话 A","recency_at_ms":3000},{"id":"thread-visible-b","title":"App 可见会话 B","recency_at_ms":2000}]`)
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-archived")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(153, "/cx cd 0"))

	threadID, pending := h.codexSessions.getThread(bindingKey, workspace)
	if threadID != "" || pending {
		t.Fatalf("stale stored thread=%q pending=%v, want empty false", threadID, pending)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "App 可见会话 A") || !strings.Contains(text, "App 可见会话 B") {
		t.Fatalf("cd should still show app visible sessions, messages=%#v", calls.texts())
	}
}

func TestCodexCxSwitchUsesCurrentWorkspaceSessionIndex(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "alpha")
	workspaceB := filepath.Join(root, "beta")
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "Beta 会话", "2026-04-29T10:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(114, "/cx cd alpha"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(115, "/cx switch 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspaceA)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-a" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-a)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if !containsText(calls.texts(), "已切换会话") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
	if containsText(calls.texts(), "thread-a") {
		t.Fatalf("switch reply should hide thread id, messages=%#v", calls.texts())
	}
}

func TestCodexShortIndexEntersWorkspaceFromWorkspaceList(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(140, "/cx 0"))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("/cx 0 should enter workspace, got cwd=%q want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-a" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-a)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已进入工作空间并切换会话") || strings.Contains(text, "0. 会话 A") {
		t.Fatalf("/cx 0 should auto switch single session, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceWithNoSessionsCreatesDraft(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(t.TempDir())
	workspace := filepath.Join(t.TempDir(), "empty")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setPendingNew(bindingKey, workspace)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(145, "/cx cd 0"))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "" || !pending {
		t.Fatalf("thread=%q pending=%v, want pending new draft", thread, pending)
	}
	if !containsText(calls.texts(), "已进入工作空间并创建新会话草稿") {
		t.Fatalf("cd should create draft for empty workspace, messages=%#v", calls.texts())
	}
}

func TestCodexShortIndexSwitchesSessionInsideWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(141, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(142, "/cx 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-a" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-a)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if !containsText(calls.texts(), "已切换会话") {
		t.Fatalf("/cx 0 should switch current workspace session, messages=%#v", calls.texts())
	}
}

func TestCodexShortDotDotReturnsToWorkspaceList(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(143, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(144, "/cx .."))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已返回工作空间列表") || !strings.Contains(text, "0. weclaw") {
		t.Fatalf("/cx .. should return to workspace list, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdDotDotReturnsToWorkspaceListWithoutChangingCwd(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(116, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(117, "/cx cd .."))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("cd .. should not change codex cwd, got %q want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已返回工作空间列表") ||
		!strings.Contains(text, "Codex 工作空间") ||
		!strings.Contains(text, "0. weclaw") {
		t.Fatalf("cd .. reply should include workspace list, messages=%#v", calls.texts())
	}
}

func TestCodexCxPwdShowsBrowseWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(119, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(120, "/cx pwd"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "浏览层级: 会话") || !strings.Contains(text, "工作空间: weclaw") {
		t.Fatalf("pwd should show current browse workspace, messages=%#v", calls.texts())
	}
}

func TestCodexAttachOpensVisibleCompanion(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeVisibleCodexAgent{
		fakeCodexThreadAgent: fakeCodexThreadAgent{
			fakeAgent: fakeAgent{
				info: agent.AgentInfo{Name: "codex", Type: "companion", Command: "codex"},
			},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(123, "/cx attach"))

	if ag.openCalls != 1 {
		t.Fatalf("OpenVisibleCompanion calls=%d, want 1", ag.openCalls)
	}
	if !containsText(calls.texts(), "已打开 Codex 本地可见端") {
		t.Fatalf("attach reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexDetachClosesVisibleCompanionOnly(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeVisibleCodexAgent{
		fakeCodexThreadAgent: fakeCodexThreadAgent{
			fakeAgent: fakeAgent{
				info: agent.AgentInfo{Name: "codex", Type: "companion", Command: "codex"},
			},
		},
		detachOK: true,
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(124, "/cx detach"))

	if ag.detachCalls != 1 {
		t.Fatalf("DetachVisibleCompanion calls=%d, want 1", ag.detachCalls)
	}
	if !containsText(calls.texts(), "已断开 Codex 本地可见端") {
		t.Fatalf("detach reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAttachRequiresVisibleCompanion(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "cli", Command: "codex"},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(125, "/cx attach"))

	if !containsText(calls.texts(), "当前 Codex Agent 不支持 attach") {
		t.Fatalf("attach unsupported reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAttachResumesRemoteFirstThreadInTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.agentWorkDirs["codex"] = workspace
	var opened []recordedCodexCLIResume
	h.SetCodexCLIResumeOpener(func(_ context.Context, command string, workspace string, threadID string) error {
		opened = append(opened, recordedCodexCLIResume{command: command, workspace: workspace, threadID: threadID})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(125, "/cx attach"))

	if len(opened) != 1 || opened[0].command != "codex-bin" || opened[0].workspace != workspace || opened[0].threadID != "thread-1" {
		t.Fatalf("opened=%#v, want codex-bin/%s/thread-1", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Codex 本地可见端") || !containsText(calls.texts(), "thread-1") {
		t.Fatalf("attach reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexCliCommandResumesRemoteFirstThreadInTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.agentWorkDirs["codex"] = workspace
	var opened []recordedCodexCLIResume
	h.SetCodexCLIResumeOpener(func(_ context.Context, command string, workspace string, threadID string) error {
		opened = append(opened, recordedCodexCLIResume{command: command, workspace: workspace, threadID: threadID})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(129, "/cx cli"))

	if len(opened) != 1 || opened[0].command != "codex-bin" || opened[0].workspace != workspace || opened[0].threadID != "thread-1" {
		t.Fatalf("opened=%#v, want codex-bin/%s/thread-1", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Codex CLI") || !containsText(calls.texts(), "thread-1") {
		t.Fatalf("cli reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAttachRequiresRecordedThreadForRemoteFirstAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(128, "/cx attach"))

	if !containsText(calls.texts(), "当前还没有可接手的 Codex thread") {
		t.Fatalf("attach without thread reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAppCommandOpensCurrentWorkspaceWithThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.agentWorkDirs["codex"] = workspace
	var opened []recordedCodexAppOpen
	h.SetCodexAppOpener(func(_ context.Context, command string, workspace string) error {
		opened = append(opened, recordedCodexAppOpen{command: command, workspace: workspace})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(126, "/cx app"))

	if len(opened) != 1 || opened[0].command != "codex-bin" || opened[0].workspace != workspace {
		t.Fatalf("opened=%#v, want codex-bin/%s", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Codex App") || !containsText(calls.texts(), "thread-1") {
		t.Fatalf("app reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAppCommandKeepsLsOnOpenedWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	staleWorkspace := t.TempDir()
	bindingKey := codexBindingKey("user-1", "codex")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.agentWorkDirs["codex"] = workspace
	h.codexSessions.setActiveWorkspace(bindingKey, workspace)
	h.codexSessions.setThread(bindingKey, workspace, "thread-1")
	h.setCodexBrowseWorkspace(bindingKey, staleWorkspace)
	h.SetCodexAppOpener(func(_ context.Context, _ string, _ string) error {
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(127, "/cx app"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(128, "/cx ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, filepath.Base(workspace)+" 会话") || !strings.Contains(text, "0. 未命名会话") {
		t.Fatalf("ls should show opened workspace session, messages=%#v", calls.texts())
	}
	if strings.Contains(text, filepath.Base(staleWorkspace)+" 会话") {
		t.Fatalf("ls should not stay on stale browse workspace, messages=%#v", calls.texts())
	}
}

func TestCodexStatusShowsWorkspaceThreadAndLocalEntryState(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.agentWorkDirs["codex"] = workspace
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(131, "/cx status"))

	text := strings.Join(calls.texts(), "\n")
	for _, want := range []string{
		"Codex 状态",
		"工作空间: " + workspace,
		"thread: thread-1",
		"remote: 已配置",
		"CLI: 未打开过",
		"App: 未打开过",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status should contain %q, messages=%#v", want, calls.texts())
		}
	}
}

func TestCodexStatusRecordsSuccessfulLocalEntries(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.agentWorkDirs["codex"] = workspace
	h.SetCodexCLIResumeOpener(func(_ context.Context, _ string, _ string, _ string) error {
		return nil
	})
	h.SetCodexAppOpener(func(_ context.Context, _ string, _ string) error {
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(132, "/cx cli"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(133, "/cx app"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(134, "/cx status"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "CLI: 已打开过") || !strings.Contains(text, "App: 已打开过") {
		t.Fatalf("status should record successful local entries, messages=%#v", calls.texts())
	}
}

func TestCodexAppFailureSuggestsCli(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.agentWorkDirs["codex"] = workspace
	h.SetCodexAppOpener(func(_ context.Context, _ string, _ string) error {
		return errors.New("app unavailable")
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(130, "/cx app"))

	if !containsText(calls.texts(), "打开 Codex App 失败") || !containsText(calls.texts(), "/cx cli") {
		t.Fatalf("app failure reply should suggest /cx cli, messages=%#v", calls.texts())
	}
}

func TestCodexAttachAppAliasOpensCodexApp(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.agentWorkDirs["codex"] = workspace
	var opened []recordedCodexAppOpen
	h.SetCodexAppOpener(func(_ context.Context, command string, workspace string) error {
		opened = append(opened, recordedCodexAppOpen{command: command, workspace: workspace})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(127, "/cx attach app"))

	if len(opened) != 1 || opened[0].workspace != workspace {
		t.Fatalf("opened=%#v, want workspace %s", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Codex App") {
		t.Fatalf("attach app reply mismatch, messages=%#v", calls.texts())
	}
}

func TestResolveAgentConversationIDRestoresActiveWorkspaceAfterRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	bindingKey := codexBindingKey("user-1", "codex")
	defaultWorkspace := t.TempDir()
	activeWorkspace := t.TempDir()

	first := NewHandler(nil, nil)
	first.SetCodexSessionFile(stateFile)
	first.codexSessions.setThread(bindingKey, activeWorkspace, "thread-active")
	first.codexSessions.setActiveWorkspace(bindingKey, activeWorkspace)

	second := NewHandler(nil, nil)
	second.SetCodexSessionFile(stateFile)
	second.SetAgentWorkDirs(map[string]string{"codex": defaultWorkspace})
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}

	conversationID, err := second.resolveAgentConversationID(context.Background(), "user-1", "codex", ag)
	if err != nil {
		t.Fatalf("resolveAgentConversationID error: %v", err)
	}

	wantConversationID := buildCodexConversationID("user-1", "codex", activeWorkspace)
	if conversationID != wantConversationID {
		t.Fatalf("conversationID=%q, want %q", conversationID, wantConversationID)
	}
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-active" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-active)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastWorkingDir() != activeWorkspace {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), activeWorkspace)
	}
}

func TestSendToNamedCodexDoesNotCreateNewThreadWhenResumeFails(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			reply: "不应调用",
			info:  agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		useErr: errors.New("resume failed"),
	}
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-old")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.sendToNamedAgent(context.Background(), platform.PlatformWeChat, "user-1", reply, "codex", "继续", "client-1")

	waitForText(t, calls, "恢复 Codex 会话失败")
	if ag.chatCallCount() != 0 {
		t.Fatalf("恢复旧 thread 失败后不应继续新建会话聊天，chatCalls=%d", ag.chatCallCount())
	}
	if ag.useThreadID != "thread-old" {
		t.Fatalf("恢复 thread=%q，want thread-old", ag.useThreadID)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-old" || pending {
		t.Fatalf("不应覆盖旧 thread，thread=%q pending=%v", thread, pending)
	}
}

func TestRecordCodexThreadKeepsExistingThreadWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	currentWorkspace := t.TempDir()
	ownerWorkspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-owner",
	}
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, ownerWorkspace, "thread-owner")

	h.recordCodexThread("user-1", "codex", ag, buildCodexConversationID("user-1", "codex", currentWorkspace))

	currentThread, currentPending := h.codexSessions.getThread(bindingKey, currentWorkspace)
	if currentThread != "" || currentPending {
		t.Fatalf("不应把已有 thread 移动到当前 workspace，thread=%q pending=%v", currentThread, currentPending)
	}
	ownerThread, ownerPending := h.codexSessions.getThread(bindingKey, ownerWorkspace)
	if ownerThread != "thread-owner" || ownerPending {
		t.Fatalf("原 workspace thread=%q pending=%v，want thread-owner false", ownerThread, ownerPending)
	}
	active, ok := h.codexSessions.getActiveWorkspace(bindingKey)
	if !ok || active != normalizeCodexWorkspaceRoot(ownerWorkspace) {
		t.Fatalf("active workspace=(%q,%v)，want %q true", active, ok, normalizeCodexWorkspaceRoot(ownerWorkspace))
	}
}

func TestHandleCodexWhoamiAndLsCommands(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetCodexLocalSessionDir(t.TempDir())
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-1")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(104, "/codex whoami"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(105, "/codex ls"))

	texts := calls.texts()
	if !containsText(texts, "workspace: "+workspace) {
		t.Fatalf("whoami should include workspace, messages=%#v", texts)
	}
	if !containsText(texts, "thread: thread-1") {
		t.Fatalf("whoami/ls should include thread, messages=%#v", texts)
	}
	if !containsText(texts, "0. "+filepath.Base(workspace)) {
		t.Fatalf("ls should include numbered workspace, messages=%#v", texts)
	}
}

func TestHandleCodexModelStatusCommandShowsCurrentConfig(t *testing.T) {
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		t.Fatalf("model status should not start agent %q", name)
		return nil
	}, nil)
	h.SetAgentMetas([]AgentMeta{{
		Name:    "codex",
		Type:    "acp",
		Command: "codex",
		Model:   "gpt-5.4",
		Effort:  "high",
	}})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(121, "/cx model status"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "Codex 模型配置") ||
		!strings.Contains(text, "model: gpt-5.4") ||
		!strings.Contains(text, "effort: high") {
		t.Fatalf("model status reply mismatch, messages=%#v", calls.texts())
	}
}

func TestHandleCodexModelLsCommandListsModelsAndEfforts(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		models: []agent.CodexModel{
			{ID: "gpt-5.4", Name: "GPT-5.4", EffortOptions: []string{"medium", "high"}},
			{ID: "gpt-5.3-codex", EffortOptions: []string{"low", "medium"}},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": t.TempDir()})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(122, "/cx model ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "Codex 可用模型") ||
		!strings.Contains(text, "0. gpt-5.4 (GPT-5.4)") ||
		!strings.Contains(text, "effort: medium, high") ||
		!strings.Contains(text, "1. gpt-5.3-codex") {
		t.Fatalf("model ls reply mismatch, messages=%#v", calls.texts())
	}
}

func TestHandleCodexQuotaCommandShowsRateLimits(t *testing.T) {
	reset := int64(1710003600)
	h := NewHandler(nil, nil)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		quota: agent.CodexQuota{Limits: []agent.CodexRateLimit{
			{
				ID:       "codex",
				Name:     "Codex",
				PlanType: "pro",
				Primary: &agent.CodexRateLimitWindow{
					UsedPercent:        80,
					ResetsAt:           &reset,
					WindowDurationMins: int64Ptr(300),
				},
				Secondary: &agent.CodexRateLimitWindow{UsedPercent: 20},
				Credits:   &agent.CodexCredits{Balance: "10", HasCredits: true},
			},
			{
				ID:          "research",
				ReachedType: "rate_limit_reached",
				Primary:     &agent.CodexRateLimitWindow{UsedPercent: 100},
			},
		}},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": t.TempDir()})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(132, "/cx quota"))

	text := strings.Join(calls.texts(), "\n")
	for _, want := range []string{
		"Codex 账号额度",
		"codex (Codex)",
		"plan: pro",
		"primary: 已用 80%",
		"secondary: 已用 20%",
		"credits: 有额度，余额 10",
		"research",
		"已达到限制: rate_limit_reached",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("quota reply missing %q, messages=%#v", want, calls.texts())
		}
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}

func TestHandleMessage_FileMessageSavesFileAndSendsPathToAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	saveDir := t.TempDir()
	h.SetSaveDir(saveDir)
	ag := &fakeAgent{reply: "已分析"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.cdnDownloader = func(_ context.Context, queryParam string, aesKey string) ([]byte, error) {
		if queryParam != "download-param" || aesKey != "aes-key" {
			t.Fatalf("download args=(%q,%q), want download-param/aes-key", queryParam, aesKey)
		}
		return []byte("文件内容"), nil
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)
	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newFileMessage(10, "方案.txt"))

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("file message should start agent once, chatCalls=%d", ag.chatCallCount())
	}
	if !strings.Contains(ag.lastChatMessage(), "用户发送了一个文件") {
		t.Fatalf("agent message should describe incoming file, got %q", ag.lastChatMessage())
	}
	if !strings.Contains(ag.lastChatMessage(), "方案.txt") {
		t.Fatalf("agent message should include file name, got %q", ag.lastChatMessage())
	}
	if !strings.Contains(ag.lastChatMessage(), saveDir) {
		t.Fatalf("agent message should include saved local path, got %q", ag.lastChatMessage())
	}
	if _, err := os.Stat(extractSavedPathFromAgentMessage(ag.lastChatMessage())); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
}

func TestHandleMessage_FileMessageWithoutMediaDoesNotCallAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "不应调用"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	msg := newFileMessage(11, "broken.txt")
	msg.ItemList[0].FileItem.Media = nil

	handleTestWeChatMessage(h, context.Background(), client, msg)

	if ag.chatCallCount() != 0 {
		t.Fatalf("file without media should not call agent, chatCalls=%d", ag.chatCallCount())
	}
	if !containsText(calls.texts(), "文件保存失败") {
		t.Fatalf("expected file failure reply, got %#v", calls.texts())
	}
}

func extractSavedPathFromAgentMessage(message string) string {
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, "本地路径：") {
			return strings.TrimPrefix(line, "本地路径：")
		}
	}
	return ""
}

func TestSendReplyWithMediaUsesChunksForLongFinalText(t *testing.T) {
	h := NewHandler(nil, nil)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	reply := strings.Join([]string{
		strings.Repeat("甲", 12),
		strings.Repeat("乙", 12),
		strings.Repeat("丙", 12),
	}, "\n")

	replyWriter := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.sendReplyWithMedia(withTextReplyChunkLimit(context.Background(), 15), replyWriter, "user-1", "codex", reply)

	texts := calls.texts()
	if len(texts) != 3 {
		t.Fatalf("sent texts=%#v, want three chunks", texts)
	}
	wantReply := wechat.FormatTextForWeChatDisplay(reply)
	if strings.Join(texts, "\n") != wantReply {
		t.Fatalf("joined chunks=%q, want WeChat display reply %q", strings.Join(texts, "\n"), wantReply)
	}
}

func TestSendReplyWithMediaAsksChoices(t *testing.T) {
	h := NewHandler(nil, nil)
	replyWriter := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.sendReplyWithMedia(context.Background(), replyWriter, "user-1", "codex", "请选择一个方案：\n1. 继续\n2. 暂停")

	if len(replyWriter.Texts) != 1 || replyWriter.Texts[0] != "请选择一个方案：" {
		t.Fatalf("texts=%#v, want cleaned prompt", replyWriter.Texts)
	}
	if len(replyWriter.Choices) != 1 || len(replyWriter.Choices[0].Choices) != 2 {
		t.Fatalf("choices=%#v, want two choices", replyWriter.Choices)
	}
}

func TestRawCommandStopCancelsActiveCodexTask(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "cli", Command: "codex"},
		},
	}
	h.agents["codex"] = ag
	key := h.agentExecutionKey("feishu:ou_user", "codex", ag)
	task, taskCtx, started := h.beginActiveTask(context.Background(), key)
	if !started {
		t.Fatal("beginActiveTask started=false, want true")
	}
	replyWriter := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "feishu:ou_user",
		MessageID: "card-stop-1",
		RawCommand: &platform.CardAction{
			Action: "stop",
		},
	}, replyWriter)

	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("task context was not canceled")
	}
	if task.shouldSendFinal() {
		t.Fatal("task should not send final after stop")
	}
	h.finishActiveTask(key, task)
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

func progressConfigWithTaskTimeout() config.ProgressConfig {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	cfg.TaskTimeoutSeconds = 1
	return cfg
}

func writeLocalCodexSession(t *testing.T, codexDir string, threadID string, workspace string, threadName string, updatedAt string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create local codex workspace: %v", err)
	}
	writeLocalCodexIndex(t, codexDir, threadID, threadName, updatedAt)
	writeLocalCodexSessionMeta(t, codexDir, threadID, workspace, updatedAt, `"Codex Desktop"`, `""`, `"vscode"`)
}

func writeCodexAppWorkspaceState(t *testing.T, codexDir string, projectOrder []string, savedRoots []string) {
	t.Helper()
	state := map[string][]string{
		"project-order":                  projectOrder,
		"electron-saved-workspace-roots": savedRoots,
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal codex app workspace state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, ".codex-global-state.json"), data, 0o600); err != nil {
		t.Fatalf("write codex app workspace state: %v", err)
	}
}

func writeFakeSQLite3(t *testing.T, output string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + output + "\nEOF\n"
	path := filepath.Join(binDir, "sqlite3")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sqlite3: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeLocalCodexIndex(t *testing.T, codexDir string, threadID string, threadName string, updatedAt string) {
	t.Helper()
	indexLine := fmt.Sprintf(`{"id":%q,"thread_name":%q,"updated_at":%q}`+"\n", threadID, threadName, updatedAt)
	indexPath := filepath.Join(codexDir, "session_index.jsonl")
	file, err := os.OpenFile(indexPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open session index: %v", err)
	}
	if _, err := file.WriteString(indexLine); err != nil {
		t.Fatalf("write session index: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close session index: %v", err)
	}
}

func writeLocalCodexSessionMeta(t *testing.T, codexDir string, threadID string, workspace string, updatedAt string, originatorJSON string, threadSourceJSON string, sourceJSON string) {
	t.Helper()
	sessionDir := filepath.Join(codexDir, "sessions", "2026", "04", "29")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "rollout-"+threadID+".jsonl")
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"originator":%s,"thread_source":%s,"source":%s}}`+"\n", updatedAt, threadID, updatedAt, workspace, originatorJSON, threadSourceJSON, sourceJSON)
	if err := os.WriteFile(sessionPath, []byte(meta), 0o600); err != nil {
		t.Fatalf("write session meta: %v", err)
	}
}

func writeArchivedLocalCodexSession(t *testing.T, codexDir string, threadID string, workspace string, threadName string, updatedAt string) {
	t.Helper()
	writeLocalCodexSession(t, codexDir, threadID, workspace, threadName, updatedAt)

	archivedDir := filepath.Join(codexDir, "archived_sessions")
	if err := os.MkdirAll(archivedDir, 0o700); err != nil {
		t.Fatalf("create archived session dir: %v", err)
	}
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"originator":"Codex Desktop"}}`+"\n", updatedAt, threadID, updatedAt, workspace)
	sessionPath := filepath.Join(archivedDir, "rollout-"+threadID+".jsonl")
	if err := os.WriteFile(sessionPath, []byte(meta), 0o600); err != nil {
		t.Fatalf("write archived session meta: %v", err)
	}
}

func writeLocalClaudeSession(t *testing.T, claudeDir string, sessionID string, workspace string, title string, updatedAt string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create local claude workspace: %v", err)
	}
	writeLocalClaudeProjectConfig(t, claudeDir, workspace)
	writeLocalClaudeTranscript(t, claudeDir, workspace, sessionID, title, updatedAt)
}

func writeLocalClaudeProjectConfig(t *testing.T, claudeDir string, workspace string) {
	t.Helper()
	configPath := filepath.Join(claudeDir, "claude.json")
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read claude config: %v", err)
	}
	var cfg struct {
		Projects map[string]map[string]interface{} `json:"projects"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse claude config: %v", err)
		}
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]map[string]interface{}{}
	}
	cfg.Projects[workspace] = map[string]interface{}{"hasTrustDialogAccepted": true}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal claude config: %v", err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatalf("write claude config: %v", err)
	}
}

func writeLocalClaudeTranscript(t *testing.T, claudeDir string, workspace string, sessionID string, title string, updatedAt string) {
	t.Helper()
	projectDir := filepath.Join(claudeDir, "projects", encodeClaudeProjectPath(workspace))
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("create claude project dir: %v", err)
	}
	sessionPath := filepath.Join(projectDir, sessionID+".jsonl")
	line := fmt.Sprintf(`{"type":"summary","summary":%q,"timestamp":%q}`+"\n", title, updatedAt)
	if err := os.WriteFile(sessionPath, []byte(line), 0o600); err != nil {
		t.Fatalf("write claude transcript: %v", err)
	}
	when, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		t.Fatalf("parse updatedAt: %v", err)
	}
	if err := os.Chtimes(sessionPath, when, when); err != nil {
		t.Fatalf("chtime claude transcript: %v", err)
	}
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
