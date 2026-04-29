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
	reply              string
	err                error
	chatCalled         bool
	chatCalls          int
	lastConversationID string
	lastMessage        string
	lastCwd            string
	info               agent.AgentInfo
}

func (f *fakeAgent) Chat(_ context.Context, conversationID string, message string) (string, error) {
	f.chatCalled = true
	f.chatCalls++
	f.lastConversationID = conversationID
	f.lastMessage = message
	return f.reply, f.err
}

func (f *fakeAgent) ResetSession(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeAgent) Info() agent.AgentInfo {
	if f.info.Name != "" {
		return f.info
	}
	return agent.AgentInfo{Name: "fake", Type: "test", Model: "mock", Command: "fake"}
}

func (f *fakeAgent) SetCwd(cwd string) {
	f.lastCwd = cwd
}

type fakeStoppableAgent struct {
	fakeAgent
	stopped bool
}

func (f *fakeStoppableAgent) Stop() {
	f.stopped = true
}

type fakeCodexThreadAgent struct {
	fakeAgent
	threadID        string
	useConversation string
	useThreadID     string
	clearCalledWith string
	useErr          error
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

func TestSendTextReplyFormatsLineBreaksForWeChatDisplay(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	reply := "🧩 步骤：查询当前工作目录\n🎯 目的：准确返回你当前会话路径\n▶️ 执行：运行 pwd 命令。\n/Volumes/Data/code/MyCode"
	want := "🧩 步骤：查询当前工作目录\n\n🎯 目的：准确返回你当前会话路径\n\n▶️ 执行：运行 pwd 命令。\n\n/Volumes/Data/code/MyCode"

	if err := SendTextReply(context.Background(), client, "user-1", reply, "ctx-1", "client-1"); err != nil {
		t.Fatalf("SendTextReply error: %v", err)
	}

	texts := calls.texts()
	if len(texts) != 1 {
		t.Fatalf("sent texts=%#v, want one text", texts)
	}
	if texts[0] != want {
		t.Fatalf("sent text=%q, want WeChat display line breaks %q", texts[0], want)
	}
}

func TestSendTextReplyChunksSplitsLongTextAndKeepsOrder(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	text := strings.Join([]string{
		strings.Repeat("甲", 12),
		strings.Repeat("乙", 12),
		strings.Repeat("丙", 12),
	}, "\n")

	if err := SendTextReplyChunks(context.Background(), client, "user-1", text, "ctx-1", "client-1", 15); err != nil {
		t.Fatalf("SendTextReplyChunks error: %v", err)
	}

	texts := calls.texts()
	if len(texts) != 3 {
		t.Fatalf("sent texts=%#v, want three chunks", texts)
	}
	wantText := FormatTextForWeChatDisplay(text)
	if strings.Join(texts, "\n") != wantText {
		t.Fatalf("joined chunks=%q, want WeChat display text %q", strings.Join(texts, "\n"), wantText)
	}
	for _, chunk := range texts {
		if len([]rune(chunk)) > 15 {
			t.Fatalf("chunk is too long: %q", chunk)
		}
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
		"Codex 账号：",
		"指定 Agent：",
		"常用别名：",
		"/cx ls",
		"/cx cd <编号|名称|..>",
		"/cx switch <编号>",
		"/sw reload",
		"/codex = /cx",
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
	for _, want := range []string{
		"常用：\n\n/info",
		"/info 查看当前 Agent\n\n/new 开启新会话",
		"Codex：\n\n/cx ls",
		"/cx ls 查看工作空间或当前工作空间会话\n\n/cx cd",
		"Codex 账号：\n\n/sw ls",
		"常用别名：\n\n/codex = /cx",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help text should use blank lines for WeChat rendering, missing %q", want)
		}
	}
}

func TestCommandRepliesUseBlankLinesForWeChat(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Model: "gpt-test", Command: "codex"},
	}

	tests := map[string]string{
		"info":        h.buildStatus(),
		"cwd":         h.handleCwd("/cwd"),
		"progress":    h.handleProgressCommand("/progress"),
		"progressErr": h.handleProgressCommand("/progress unknown"),
		"codexHelp":   buildCodexSessionHelpText(),
		"switchHelp":  buildSwitchHelpText(),
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

func TestParseSwitchCommand_ListAlias(t *testing.T) {
	args, usage := parseSwitchCommand("/sw ls")
	if usage != "" {
		t.Fatalf("unexpected usage: %s", usage)
	}
	want := []string{"list"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("parseSwitchCommand args=%v, want=%v", args, want)
	}
}

func TestParseSwitchCommand_SwitchShortcut(t *testing.T) {
	args, usage := parseSwitchCommand("/sw 0")
	if usage != "" {
		t.Fatalf("unexpected usage: %s", usage)
	}
	want := []string{"switch", "0"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("parseSwitchCommand args=%v, want=%v", args, want)
	}
}

func TestParseSwitchCommand_Usage(t *testing.T) {
	_, usage := parseSwitchCommand("/sw")
	if usage == "" {
		t.Fatal("expected usage message")
	}
}

func TestParseSwitchCommand_OldPrefixRejected(t *testing.T) {
	_, usage := parseSwitchCommand("/switch ls")
	if usage == "" {
		t.Fatal("expected usage for old /switch prefix")
	}
}

func TestHandleSwitchCommand_StripsANSI(t *testing.T) {
	h := newTestHandler()
	h.switchScript = "/tmp/codex-switch.sh"
	var gotScript string
	var gotArgs []string
	h.switchRunner = func(ctx context.Context, scriptPath string, args ...string) (string, error) {
		gotScript = scriptPath
		gotArgs = append([]string(nil), args...)
		return "\x1b[0;32m[OK]\x1b[0m 已切换\n", nil
	}

	reply := h.handleSwitchCommand(context.Background(), "/sw 1")
	if gotScript != "/tmp/codex-switch.sh" {
		t.Fatalf("scriptPath=%q, want %q", gotScript, "/tmp/codex-switch.sh")
	}
	wantArgs := []string{"switch", "1"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args=%v, want=%v", gotArgs, wantArgs)
	}
	if strings.Contains(reply, "\x1b[") {
		t.Fatalf("reply still contains ANSI code: %q", reply)
	}
	if !strings.Contains(reply, "[OK] 已切换") {
		t.Fatalf("unexpected reply: %q", reply)
	}
}

func TestHandleSwitchCommandFormatsScriptOutputForWeChat(t *testing.T) {
	h := newTestHandler()
	h.switchRunner = func(ctx context.Context, scriptPath string, args ...string) (string, error) {
		return "当前账号: plus\n可切换账号: 2\n", nil
	}

	reply := h.handleSwitchCommand(context.Background(), "/sw current")
	want := "当前账号: plus\n\n可切换账号: 2"
	if reply != want {
		t.Fatalf("reply=%q, want %q", reply, want)
	}
}

func TestHandleSwitchCommand_ReturnsErrorOutput(t *testing.T) {
	h := newTestHandler()
	h.switchRunner = func(ctx context.Context, scriptPath string, args ...string) (string, error) {
		return "\x1b[0;31m[ERROR]\x1b[0m 切换失败", errors.New("exit status 1")
	}

	reply := h.handleSwitchCommand(context.Background(), "/sw bad-id")
	if !strings.Contains(reply, "切换失败") {
		t.Fatalf("unexpected reply: %q", reply)
	}
	if strings.Contains(reply, "\x1b[") {
		t.Fatalf("reply still contains ANSI code: %q", reply)
	}
}

func TestHandleSwitchCommand_LocalHelpDoesNotRunScript(t *testing.T) {
	h := newTestHandler()
	h.switchRunner = func(ctx context.Context, scriptPath string, args ...string) (string, error) {
		t.Fatalf("switch help should not execute script, got script=%s args=%v", scriptPath, args)
		return "", nil
	}

	reply := h.handleSwitchCommand(context.Background(), "/sw help")
	if !strings.Contains(reply, "/sw ls") {
		t.Fatalf("help reply should mention /sw ls, got %q", reply)
	}
	if !strings.Contains(reply, "/sw <编号|ID>") {
		t.Fatalf("help reply should mention switch shortcut, got %q", reply)
	}
}

func TestHandleSwitchCommand_RestartsRunningCodexAgentAfterSwitch(t *testing.T) {
	oldCodex := &fakeStoppableAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	newCodex := &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}
	h := newTestHandler()
	h.defaultName = "codex"
	h.agents["codex"] = oldCodex
	h.factory = func(_ context.Context, name string) agent.Agent {
		if name != "codex" {
			t.Fatalf("unexpected factory name: %s", name)
		}
		return newCodex
	}
	h.switchRunner = func(_ context.Context, _ string, args ...string) (string, error) {
		if !reflect.DeepEqual(args, []string{"switch", "1"}) {
			t.Fatalf("switch args=%v, want [switch 1]", args)
		}
		return "已切换", nil
	}

	reply := h.handleSwitchCommand(context.Background(), "/sw 1")

	if !oldCodex.stopped {
		t.Fatal("切换账号后应该停止旧 Codex Agent 进程")
	}
	if h.agents["codex"] != newCodex {
		t.Fatalf("切换账号后默认 Codex Agent 应该重建，got %#v", h.agents["codex"])
	}
	if !strings.Contains(reply, "已刷新 WeClaw 中的 Codex Agent") {
		t.Fatalf("reply should mention codex refresh, got %q", reply)
	}
}

func TestHandleSwitchCommand_ReloadRefreshesCodexAgentWithoutRunningScript(t *testing.T) {
	oldCodex := &fakeStoppableAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	newCodex := &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}
	h := newTestHandler()
	h.defaultName = "codex"
	h.agents["codex"] = oldCodex
	h.factory = func(_ context.Context, name string) agent.Agent {
		if name != "codex" {
			t.Fatalf("unexpected factory name: %s", name)
		}
		return newCodex
	}
	h.switchRunner = func(_ context.Context, _ string, args ...string) (string, error) {
		t.Fatalf("/sw reload 不应该执行外部切换脚本，got args=%v", args)
		return "", nil
	}

	reply := h.handleSwitchCommand(context.Background(), "/sw reload")

	if !oldCodex.stopped {
		t.Fatal("/sw reload 应该停止旧 Codex Agent 进程")
	}
	if h.agents["codex"] != newCodex {
		t.Fatalf("/sw reload 后默认 Codex Agent 应该重建，got %#v", h.agents["codex"])
	}
	if !strings.Contains(reply, "当前本机登录状态") {
		t.Fatalf("reply should mention local login state, got %q", reply)
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
	if ag.chatCalled {
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
	if !ag.chatCalled {
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
	onProgress, stop := h.startProgressSession(context.Background(), client, "user-1", "ctx-1", "", "修复实时回复碎片化", cfg)

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
	onProgress, stop := h.startProgressSession(context.Background(), client, "user-1", "ctx-1", "", "查询当前工作目录", cfg)

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
	onProgress, stop := h.startProgressSession(context.Background(), client, "user-1", "ctx-1", "", "修复实时回复碎片化", cfg)

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
	h.sendToNamedAgent(context.Background(), client, newTextMessage(1, "/codex hello"), "codex", "hello", "client-1")

	if !containsText(calls.texts(), "实时片段，仅供预览") {
		t.Fatalf("expected named agent to use stream override, messages=%#v", calls.texts())
	}
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
		h.sendToNamedAgent(ctx, client, newTextMessage(1, "/claude 第一条"), "claude", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	secondDone := make(chan struct{})
	go func() {
		h.sendToNamedAgent(ctx, client, newTextMessage(2, "/claude 第二条"), "claude", "第二条", "client-2")
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
		h.sendToNamedAgent(ctx, client, newTextMessage(3001, "/slow hello"), "slow", "hello", "client-1")
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
		h.sendToDefaultAgent(ctx, client, newTextMessage(3002, "hello"), "hello", "client-1")
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
		h.broadcastToAgents(ctx, client, newTextMessage(3003, "@slow hello"), []string{"slow"}, "hello")
	})
	waitForText(t, calls, "context deadline exceeded")
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
		h.sendToNamedAgent(ctx, client, newTextMessage(1, "/codex 第一条"), "codex", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	h.sendToNamedAgent(ctx, client, newTextMessage(2, "/codex 第二条"), "codex", "第二条", "client-2")
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
		h.sendToNamedAgent(ctx, client, newTextMessage(1, "/codex 第一条"), "codex", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(ctx, client, newTextMessage(2, "/codex 第二条"), "codex", "第二条", "client-2")

	guideDone := make(chan struct{})
	go func() {
		h.HandleMessage(ctx, client, newTextMessage(3, "/guide"))
		close(guideDone)
	}()
	waitDone(t, firstDone, "第一条监听")
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitDone(t, guideDone, "引导任务")

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
		h.sendToNamedAgent(ctx, client, newTextMessage(1, "/codex 第一条"), "codex", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(ctx, client, newTextMessage(2, "/codex 第二条"), "codex", "第二条", "client-2")

	h.HandleMessage(ctx, client, newTextMessage(3, "/cancel"))
	ag.release <- struct{}{}
	waitDone(t, firstDone, "第一条任务")

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

	h.broadcastToAgents(context.Background(), client, newTextMessage(1, "@codex @claude hello"), []string{"codex", "claude"}, "hello")

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

	h.HandleMessage(context.Background(), client, msg)
	h.HandleMessage(context.Background(), client, msg)

	if ag.chatCalls != 1 {
		t.Fatalf("MessageID=0 duplicate text should only start agent once, chatCalls=%d", ag.chatCalls)
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

	h.HandleMessage(context.Background(), client, msg)
	h.HandleMessage(context.Background(), client, msg)

	if ag.chatCalls != 1 {
		t.Fatalf("same MessageID should only start agent once, chatCalls=%d", ag.chatCalls)
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

	h.HandleMessage(context.Background(), client, newTextMessage(100, text))

	if ag.chatCalls != 1 {
		t.Fatalf("absolute path text should call default agent once, chatCalls=%d", ag.chatCalls)
	}
	if ag.lastMessage != text {
		t.Fatalf("agent message=%q, want original text", ag.lastMessage)
	}
	if containsText(calls.texts(), "Usage: specify one agent") {
		t.Fatalf("absolute path text should not reply usage, messages=%#v", calls.texts())
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

	h.sendToNamedAgent(context.Background(), client, newTextMessage(101, "/codex hello"), "codex", "hello", "client-1")

	if ag.chatCalls != 1 {
		t.Fatalf("codex chat calls=%d, want 1", ag.chatCalls)
	}
	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.lastConversationID != wantConversationID {
		t.Fatalf("conversationID=%q, want %q", ag.lastConversationID, wantConversationID)
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

	h.HandleMessage(context.Background(), client, newTextMessage(102, "/codex new"))

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

	h.HandleMessage(context.Background(), client, newTextMessage(103, "/codex switch thread-2"))

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

	h.HandleMessage(context.Background(), client, newTextMessage(106, "/codex switch thread-target"))

	wantConversationID := buildCodexConversationID("user-1", "codex", targetWorkspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-target" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-target)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastCwd != targetWorkspace {
		t.Fatalf("codex cwd=%q, want %q", ag.lastCwd, targetWorkspace)
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

	h.HandleMessage(context.Background(), client, newTextMessage(108, "/codex switch 1"))

	wantConversationID := buildCodexConversationID("user-1", "codex", targetWorkspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-b" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-b)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastCwd != normalizeCodexWorkspaceRoot(targetWorkspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastCwd, normalizeCodexWorkspaceRoot(targetWorkspace))
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

	h.HandleMessage(context.Background(), client, newTextMessage(109, "/codex ls"))

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

	h.HandleMessage(context.Background(), client, newTextMessage(110, "/codex switch 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-desktop" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-desktop)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastCwd != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastCwd, normalizeCodexWorkspaceRoot(workspace))
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-desktop" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-desktop false", thread, pending)
	}
	if !containsText(calls.texts(), "已切换会话") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
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

	h.HandleMessage(context.Background(), client, newTextMessage(111, "/cx ls"))

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

	h.HandleMessage(context.Background(), client, newTextMessage(112, "/cx cd 0"))
	h.HandleMessage(context.Background(), client, newTextMessage(113, "/cx ls"))

	if ag.lastCwd != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastCwd, normalizeCodexWorkspaceRoot(workspace))
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已进入工作空间") || !strings.Contains(text, "weclaw 会话") {
		t.Fatalf("cd then ls should enter workspace and show sessions, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "0. 实现两级会话浏览") || !strings.Contains(text, "1. 修复安全问题") {
		t.Fatalf("session ls should show numbered session names, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-local-a") || strings.Contains(text, "来源:") {
		t.Fatalf("session ls should hide thread ids and source labels, messages=%#v", calls.texts())
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

	h.HandleMessage(context.Background(), client, newTextMessage(114, "/cx cd alpha"))
	h.HandleMessage(context.Background(), client, newTextMessage(115, "/cx switch 0"))

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

	h.HandleMessage(context.Background(), client, newTextMessage(116, "/cx cd weclaw"))
	h.HandleMessage(context.Background(), client, newTextMessage(117, "/cx cd .."))
	h.HandleMessage(context.Background(), client, newTextMessage(118, "/cx ls"))

	if ag.lastCwd != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("cd .. should not change codex cwd, got %q want %q", ag.lastCwd, normalizeCodexWorkspaceRoot(workspace))
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已返回工作空间列表") || !strings.Contains(text, "Codex 工作空间") {
		t.Fatalf("cd .. should return to workspace list, messages=%#v", calls.texts())
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

	h.HandleMessage(context.Background(), client, newTextMessage(119, "/cx cd weclaw"))
	h.HandleMessage(context.Background(), client, newTextMessage(120, "/cx pwd"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "浏览层级: 会话") || !strings.Contains(text, "工作空间: weclaw") {
		t.Fatalf("pwd should show current browse workspace, messages=%#v", calls.texts())
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
	if ag.lastCwd != activeWorkspace {
		t.Fatalf("codex cwd=%q, want %q", ag.lastCwd, activeWorkspace)
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

	h.sendToNamedAgent(context.Background(), client, newTextMessage(107, "/codex 继续"), "codex", "继续", "client-1")

	if ag.chatCalls != 0 {
		t.Fatalf("恢复旧 thread 失败后不应继续新建会话聊天，chatCalls=%d", ag.chatCalls)
	}
	if ag.useThreadID != "thread-old" {
		t.Fatalf("恢复 thread=%q，want thread-old", ag.useThreadID)
	}
	if !containsText(calls.texts(), "恢复 Codex 会话失败") {
		t.Fatalf("未提示恢复失败，messages=%#v", calls.texts())
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

	h.HandleMessage(context.Background(), client, newTextMessage(104, "/codex whoami"))
	h.HandleMessage(context.Background(), client, newTextMessage(105, "/codex ls"))

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

	h.HandleMessage(context.Background(), client, newFileMessage(10, "方案.txt"))

	if ag.chatCalls != 1 {
		t.Fatalf("file message should start agent once, chatCalls=%d", ag.chatCalls)
	}
	if !strings.Contains(ag.lastMessage, "用户发送了一个文件") {
		t.Fatalf("agent message should describe incoming file, got %q", ag.lastMessage)
	}
	if !strings.Contains(ag.lastMessage, "方案.txt") {
		t.Fatalf("agent message should include file name, got %q", ag.lastMessage)
	}
	if !strings.Contains(ag.lastMessage, saveDir) {
		t.Fatalf("agent message should include saved local path, got %q", ag.lastMessage)
	}
	if _, err := os.Stat(extractSavedPathFromAgentMessage(ag.lastMessage)); err != nil {
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

	h.HandleMessage(context.Background(), client, msg)

	if ag.chatCalls != 0 {
		t.Fatalf("file without media should not call agent, chatCalls=%d", ag.chatCalls)
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
	msg := newTextMessage(1, "hello")
	reply := strings.Join([]string{
		strings.Repeat("甲", 12),
		strings.Repeat("乙", 12),
		strings.Repeat("丙", 12),
	}, "\n")

	h.sendReplyWithMedia(ctxWithChunkLimit(context.Background(), 15), client, msg, "codex", reply, "client-1")

	texts := calls.texts()
	if len(texts) != 3 {
		t.Fatalf("sent texts=%#v, want three chunks", texts)
	}
	wantReply := FormatTextForWeChatDisplay(reply)
	if strings.Join(texts, "\n") != wantReply {
		t.Fatalf("joined chunks=%q, want WeChat display reply %q", strings.Join(texts, "\n"), wantReply)
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

	sessionDir := filepath.Join(codexDir, "sessions", "2026", "04", "29")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "rollout-"+threadID+".jsonl")
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"originator":"Codex Desktop"}}`+"\n", updatedAt, threadID, updatedAt, workspace)
	if err := os.WriteFile(sessionPath, []byte(meta), 0o600); err != nil {
		t.Fatalf("write session meta: %v", err)
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
