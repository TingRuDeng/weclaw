package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
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

func (f *fakeAgent) SetCwd(_ string) {}

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

func TestSendTextReplyPreservesLineBreaks(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	reply := "🧩 步骤：查询当前工作目录\n🎯 目的：准确返回你当前会话路径\n▶️ 执行：运行 pwd 命令。\n/Volumes/Data/code/MyCode"

	if err := SendTextReply(context.Background(), client, "user-1", reply, "ctx-1", "client-1"); err != nil {
		t.Fatalf("SendTextReply error: %v", err)
	}

	texts := calls.texts()
	if len(texts) != 1 {
		t.Fatalf("sent texts=%#v, want one text", texts)
	}
	if texts[0] != reply {
		t.Fatalf("sent text=%q, want original reply with newlines", texts[0])
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
	if strings.Join(texts, "\n") != text {
		t.Fatalf("joined chunks=%q, want original text", strings.Join(texts, "\n"))
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
		"/codex workspace",
		"/sw reload",
		"/cx = /codex",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help text should mention %q", want)
		}
	}
	if strings.Contains(text, "Available commands") || strings.Contains(text, "Aliases:") {
		t.Error("help text should not use old English headings")
	}
	for _, want := range []string{
		"常用：\n\n/info",
		"/info 查看当前 Agent\n\n/new 开启新会话",
		"Codex：\n\n/codex where",
		"/codex where 查看当前 Codex workspace 和 thread\n\n/codex workspace",
		"Codex 账号：\n\n/sw ls",
		"常用别名：\n\n/cx = /codex",
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
	bindingKey := codexBindingKey("user-1", "codex")
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setPendingNew(bindingKey, workspaceB)

	where := h.renderCodexWhere(bindingKey, workspaceA)
	if !strings.Contains(where, "workspace: "+workspaceA+"\n\nthread: thread-a") {
		t.Fatalf("where reply should separate fields with blank lines, got %q", where)
	}

	list := h.renderCodexWorkspace(bindingKey)
	for _, want := range []string{
		"Codex workspaces:\n\n- ",
		workspaceA + "\n\n  thread: thread-a",
		workspaceB + "\n\n  thread: (new draft)",
	} {
		if !strings.Contains(list, want) {
			t.Fatalf("workspace reply missing %q, got %q", want, list)
		}
	}
	if strings.Contains(list, "Codex workspaces:\n- ") {
		t.Fatalf("workspace reply should not use single newline after title, got %q", list)
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

	if !strings.Contains(reply, "当前进度模式：summary") {
		t.Fatalf("reply=%q, want current summary mode", reply)
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
	if got := h.resolveProgressConfig("").Mode; got != progressModeSummary {
		t.Fatalf("progress mode=%q, want unchanged summary", got)
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
	if !containsText(calls.texts(), "已切换线程") {
		t.Fatalf("reply should mention switched thread, messages=%#v", calls.texts())
	}
}

func TestHandleCodexWhereAndWorkspaceCommands(t *testing.T) {
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
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-1")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	h.HandleMessage(context.Background(), client, newTextMessage(104, "/codex where"))
	h.HandleMessage(context.Background(), client, newTextMessage(105, "/codex workspace"))

	texts := calls.texts()
	if !containsText(texts, "workspace: "+workspace) {
		t.Fatalf("where should include workspace, messages=%#v", texts)
	}
	if !containsText(texts, "thread: thread-1") {
		t.Fatalf("where/workspace should include thread, messages=%#v", texts)
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
	if strings.Join(texts, "\n") != reply {
		t.Fatalf("joined chunks=%q, want original reply", strings.Join(texts, "\n"))
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
