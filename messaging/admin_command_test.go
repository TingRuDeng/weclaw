package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func privateFeishuAdminMetadata(unionID string) map[string]string {
	return map[string]string{
		"feishu_union_id":  unionID,
		"feishu_chat_type": "p2p",
	}
}

func TestServiceAdminCommandRequiresWhitelistedUser(t *testing.T) {
	ag := &fakeAgent{reply: "agent reply", info: agent.AgentInfo{Name: "mock", Type: "test"}}
	calls := 0
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "mock" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("mock", ag)
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		calls++
		return "should not run", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "/update",
	}, reply)

	if ag.chatCallCount() != 0 {
		t.Fatalf("agent calls=%d, want 0 for denied admin command", ag.chatCallCount())
	}
	if calls != 0 {
		t.Fatalf("admin executor calls=%d, want 0 for denied admin command", calls)
	}
	texts := reply.waitTexts(t, 1)
	if len(texts) != 1 || !strings.Contains(texts[0], "未授权执行 WeClaw 管理命令") {
		t.Fatalf("reply texts=%#v, want unauthorized admin command notice", texts)
	}
}

func TestServiceAdminCommandRunsUpdateForWhitelistedUser(t *testing.T) {
	ag := &fakeAgent{reply: "agent reply", info: agent.AgentInfo{Name: "mock", Type: "test"}}
	var gotCommand string
	var gotArgs []string
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "mock" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("mock", ag)
	h.SetAdminUsers([]string{" ou_admin "})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		gotCommand = command
		gotArgs = append([]string(nil), args...)
		return "Already up to date", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "ou_admin",
		Text:     "/update",
	}, reply)

	if ag.chatCallCount() != 0 {
		t.Fatalf("agent calls=%d, want 0 for recognized admin command", ag.chatCallCount())
	}
	texts := reply.waitTexts(t, 2)
	if gotCommand != "update" || len(gotArgs) != 0 {
		t.Fatalf("executor command=%q args=%#v, want update with no args", gotCommand, gotArgs)
	}
	if !strings.Contains(texts[0], "管理命令已受理：/update") ||
		!strings.Contains(texts[0], "后台执行") ||
		!strings.Contains(texts[0], "另行通知") {
		t.Fatalf("reply texts=%#v, want asynchronous acceptance notice", texts)
	}
	if !strings.Contains(texts[1], "当前已是最新版本") {
		t.Fatalf("reply texts=%#v, want concise update result", texts)
	}
	if strings.Contains(texts[1], "Checking for updates") {
		t.Fatalf("reply texts=%#v, should not expose raw update output", texts)
	}
}

func TestServiceAdminCommandUpdatesStreamingCardInPlace(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(context.Context, string, []string) (string, error) {
		return "正在检查更新...\n已是最新版本 (v0.1.217)\n", nil
	})
	reply := newAdminStreamingCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/update",
		Metadata: privateFeishuAdminMetadata("on_admin"),
	}, reply)

	completed := reply.stream.waitCompleted(t)
	if reply.options.Title != "WeClaw · 更新" {
		t.Fatalf("stream title=%q, want update title", reply.options.Title)
	}
	if !strings.Contains(reply.options.InitialContent, "正在检查本地版本与最新版本") ||
		!strings.Contains(reply.options.InitialContent, "此卡片中更新") {
		t.Fatalf("stream initial content=%q, want in-place status explanation", reply.options.InitialContent)
	}
	if !strings.Contains(completed, "当前已是最新版本：v0.1.217") {
		t.Fatalf("stream completed=%q, want latest version result", completed)
	}
	if texts := reply.snapshotTexts(); len(texts) != 0 {
		t.Fatalf("reply texts=%#v, want no separate acceptance or completion message", texts)
	}
}

func TestServiceAdminCommandFailsStreamingCardInPlace(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(context.Context, string, []string) (string, error) {
		return "正在检查更新...", errors.New("release unavailable")
	})
	reply := newAdminStreamingCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/update",
		Metadata: privateFeishuAdminMetadata("on_admin"),
	}, reply)

	failed := reply.stream.waitFailed(t)
	if !strings.Contains(failed, "管理命令执行失败：/update") ||
		!strings.Contains(failed, "release unavailable") {
		t.Fatalf("stream failed=%q, want update failure", failed)
	}
	if texts := reply.snapshotTexts(); len(texts) != 0 {
		t.Fatalf("reply texts=%#v, want no separate failure message", texts)
	}
}

func TestServiceAdminCommandAllowsFeishuUnionID(t *testing.T) {
	var gotCommand string
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		gotCommand = command
		return "Already up to date", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin_for_this_bot",
		Text:     "/update",
		Metadata: privateFeishuAdminMetadata("on_admin"),
	}, reply)

	texts := reply.waitTexts(t, 2)
	if gotCommand != "update" {
		t.Fatalf("executor command=%q, want update", gotCommand)
	}
	if !strings.Contains(texts[0], "管理命令已受理：/update") || !strings.Contains(texts[0], "后台执行") {
		t.Fatalf("reply texts=%#v, want asynchronous acceptance notice", texts)
	}
}

func TestServiceAdminCommandRejectsFeishuGroupEvenForAdmin(t *testing.T) {
	calls := 0
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(context.Context, string, []string) (string, error) {
		calls++
		return "should not run", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/update",
		Route:    platform.SessionRoute{Key: "feishu:cli_a:tenant:group:oc_group"},
		Metadata: map[string]string{
			"feishu_union_id":  "on_admin",
			"feishu_chat_type": "group",
		},
	}, reply)

	texts := reply.waitTexts(t, 1)
	if calls != 0 {
		t.Fatalf("admin executor calls=%d, want 0 for group command", calls)
	}
	if !strings.Contains(texts[0], "请在机器人私聊窗口执行") {
		t.Fatalf("reply texts=%#v, want private-chat requirement", texts)
	}
}

func TestServiceAdminCommandRejectsFeishuGroupCardCallbackEvenForAdmin(t *testing.T) {
	calls := 0
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(context.Context, string, []string) (string, error) {
		calls++
		return "should not run", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Route:    platform.SessionRoute{Key: "feishu:cli_a:tenant:group:oc_group"},
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": "/update"},
		},
		Metadata: map[string]string{"feishu_union_id": "on_admin"},
	}, reply)

	texts := reply.waitTexts(t, 1)
	if calls != 0 {
		t.Fatalf("admin executor calls=%d, want 0 for group card callback", calls)
	}
	if !strings.Contains(texts[0], "请在机器人私聊窗口执行") {
		t.Fatalf("reply texts=%#v, want private-chat requirement", texts)
	}
}

func TestServiceAdminCommandReportsBackgroundUpdateFailure(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(context.Context, string, []string) (string, error) {
		return "正在检查更新...", errors.New("download failed")
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/update",
		Metadata: privateFeishuAdminMetadata("on_admin"),
	}, reply)

	texts := reply.waitTexts(t, 2)
	if !strings.Contains(texts[0], "管理命令已受理：/update") {
		t.Fatalf("reply texts=%#v, want acceptance notice", texts)
	}
	if !strings.Contains(texts[1], "管理命令执行失败：/update") ||
		!strings.Contains(texts[1], "download failed") {
		t.Fatalf("reply texts=%#v, want final update failure", texts)
	}
}

func TestServiceAdminCommandRejectsFeishuOpenIDAndUserID(t *testing.T) {
	calls := 0
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"ou_admin_for_this_bot", "user_admin"})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		calls++
		return "should not run", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:    platform.PlatformFeishu,
		UserID:      "ou_admin_for_this_bot",
		UserAliases: []string{"user_admin", "on_real_admin"},
		Text:        "/update",
		Metadata: map[string]string{
			"feishu_open_id":  "ou_admin_for_this_bot",
			"feishu_user_id":  "user_admin",
			"feishu_union_id": "on_real_admin",
		},
	}, reply)

	texts := reply.waitTexts(t, 1)
	if calls != 0 {
		t.Fatalf("admin executor calls=%d, want 0 when admin_users only has feishu open_id/user_id", calls)
	}
	if !strings.Contains(texts[0], "未授权执行 WeClaw 管理命令") {
		t.Fatalf("reply texts=%#v, want unauthorized notice", texts)
	}
}

func TestServiceAdminCommandAllowsRestartForceOnly(t *testing.T) {
	useAdminRestartNotificationPath(t)
	var gotCommand string
	var gotArgs []string
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"ou_admin"})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		gotCommand = command
		gotArgs = append([]string(nil), args...)
		return "restart scheduled", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "ou_admin",
		Text:     "/restart --force",
	}, reply)

	texts := reply.waitTexts(t, 2)
	if gotCommand != "restart" || !reflect.DeepEqual(gotArgs, []string{"--force"}) {
		t.Fatalf("executor command=%q args=%#v, want restart --force", gotCommand, gotArgs)
	}
	if !strings.Contains(texts[0], "管理命令已受理：/restart") || !strings.Contains(texts[0], "后台执行") {
		t.Fatalf("reply texts=%#v, want asynchronous acceptance notice", texts)
	}
	if !strings.Contains(texts[1], "restart scheduled") {
		t.Fatalf("reply texts=%#v, want restart output", texts)
	}
}

func TestServiceAdminRestartWithoutForceReportsActiveTasks(t *testing.T) {
	calls := 0
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		calls++
		return "should not run", nil
	})
	task, _, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{
		owner:     "ou_admin",
		agentName: "codex",
		message:   "运行中的任务",
	})
	if !started {
		t.Fatal("active task should start")
	}
	defer h.finishActiveTask("task-1", task)
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/restart",
		Metadata: privateFeishuAdminMetadata("on_admin"),
	}, reply)

	if calls != 0 {
		t.Fatalf("admin executor calls=%d, want 0 while active task blocks restart", calls)
	}
	texts := reply.waitTexts(t, 1)
	if len(texts) != 1 ||
		!strings.Contains(texts[0], "当前还有 1 个运行中的任务") ||
		!strings.Contains(texts[0], "/restart --force") {
		t.Fatalf("reply texts=%#v, want active task restart notice", texts)
	}
	if strings.Contains(texts[0], "开始执行管理命令") {
		t.Fatalf("reply texts=%#v, should not send start notice for blocked restart", texts)
	}
}

func TestRestartIgnoresDetachedTask(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{owner: "ou_admin"})
	if !started {
		t.Fatal("active task should start")
	}
	if cancelled, denied := h.cancelActiveTask("task-1", "ou_admin"); !cancelled || denied {
		t.Fatalf("cancelled=%v denied=%v, want true false", cancelled, denied)
	}
	if text, blocked := h.restartBlockedByActiveTasks("restart", nil); blocked {
		t.Fatalf("detached task should not block restart: %s", text)
	}
	h.finishActiveTask("task-1", task)
}

func TestServiceAdminCommandsRunSequentially(t *testing.T) {
	useAdminRestartNotificationPath(t)
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	updateStarted := make(chan struct{})
	allowUpdateDone := make(chan struct{})
	restartStarted := make(chan struct{})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		switch command {
		case "update":
			close(updateStarted)
			<-allowUpdateDone
			return "Updated to v0.1.113", nil
		case "restart":
			close(restartStarted)
			return "restart scheduled", nil
		default:
			t.Fatalf("unexpected admin command=%q", command)
			return "", nil
		}
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/update",
		Metadata: privateFeishuAdminMetadata("on_admin"),
	}, reply)
	waitForClosedChannel(t, updateStarted, "update start")
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/restart --force",
		Metadata: privateFeishuAdminMetadata("on_admin"),
	}, reply)
	assertChannelNotClosed(t, restartStarted, "restart should wait for update")

	close(allowUpdateDone)
	waitForClosedChannel(t, restartStarted, "restart start")
	texts := reply.waitTexts(t, 4)
	if !strings.Contains(texts[2], "已更新到：v0.1.113") {
		t.Fatalf("reply texts=%#v, want update completion before restart", texts)
	}
	if !strings.Contains(texts[3], "restart scheduled") {
		t.Fatalf("reply texts=%#v, want restart completion after update", texts)
	}
}

func TestServiceAdminCommandRejectsUnsupportedArgs(t *testing.T) {
	calls := 0
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		calls++
		return "should not run", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/update --restart",
		Metadata: privateFeishuAdminMetadata("on_admin"),
	}, reply)

	if calls != 0 {
		t.Fatalf("admin executor calls=%d, want 0 for unsupported args", calls)
	}
	texts := reply.waitTexts(t, 1)
	if len(texts) != 1 || !strings.Contains(texts[0], "不支持参数") {
		t.Fatalf("reply texts=%#v, want unsupported args notice", texts)
	}
}

func TestFormatServiceAdminCommandReplySummarizesUpdateOutput(t *testing.T) {
	output := "Checking for updates...\nAlready up to date (v0.1.97)\n"

	reply := formatServiceAdminCommandReply("update", output, nil)

	if !strings.Contains(reply, "当前已是最新版本：v0.1.97") {
		t.Fatalf("reply=%q, want concise latest version summary", reply)
	}
	if strings.Contains(reply, "Checking for updates") {
		t.Fatalf("reply=%q, should not include raw update progress", reply)
	}
}

func TestFormatServiceAdminCommandReplySummarizesUpdatedVersion(t *testing.T) {
	output := "Checking for updates...\nCurrent: v0.1.96 -> Latest: v0.1.97\nDownloading https://example.invalid/weclaw...\nUpdated to v0.1.97\nUpdate complete. Run 'weclaw restart' when you are ready.\n"

	reply := formatServiceAdminCommandReply("update", output, nil)

	if !strings.Contains(reply, "已更新到：v0.1.97") || !strings.Contains(reply, "请执行 /restart --force 生效") {
		t.Fatalf("reply=%q, want updated version summary with restart hint", reply)
	}
	if strings.Contains(reply, "Downloading") {
		t.Fatalf("reply=%q, should not include raw download progress", reply)
	}
}

func TestFormatServiceAdminCommandReplySummarizesChineseLatestVersion(t *testing.T) {
	output := "正在检查更新...\n已是最新版本 (v0.1.181)\n更新完成；准备就绪后运行 weclaw restart。\n"

	reply := formatServiceAdminCommandReply("update", output, nil)

	if !strings.Contains(reply, "当前已是最新版本：v0.1.181") {
		t.Fatalf("reply=%q, want Chinese latest version summary", reply)
	}
	if strings.Contains(reply, "准备就绪后") {
		t.Fatalf("reply=%q, should not replace latest status with final progress line", reply)
	}
}

func TestFormatServiceAdminCommandReplySummarizesChineseUpdatedVersion(t *testing.T) {
	output := "正在检查更新...\n当前版本: v0.1.180 -> 最新版本: v0.1.181\n正在下载 https://example.invalid/weclaw...\n已更新到 v0.1.181\n更新完成；准备就绪后运行 weclaw restart。\n"

	reply := formatServiceAdminCommandReply("update", output, nil)

	if !strings.Contains(reply, "已更新到：v0.1.181") || !strings.Contains(reply, "请执行 /restart --force 生效") {
		t.Fatalf("reply=%q, want Chinese updated version summary", reply)
	}
	if strings.Contains(reply, "正在下载") {
		t.Fatalf("reply=%q, should not include raw download progress", reply)
	}
}

func TestDefaultServiceAdminRestartReportsInvalidExecutable(t *testing.T) {
	oldExecutable := currentExecutablePathFunc
	currentExecutablePathFunc = func() (string, error) {
		return filepath.Join(t.TempDir(), "missing-weclaw"), nil
	}
	t.Cleanup(func() { currentExecutablePathFunc = oldExecutable })

	output, err := defaultServiceAdminCommandExecutor(context.Background(), "restart", nil)

	if err == nil {
		t.Fatal("defaultServiceAdminCommandExecutor restart error = nil, want executable validation error")
	}
	if strings.TrimSpace(output) != "" {
		t.Fatalf("output=%q, want empty output on validation failure", output)
	}
	if !strings.Contains(err.Error(), "restart executable") {
		t.Fatalf("error=%v, want restart executable hint", err)
	}
}

type adminCommandTestReplier struct {
	mu           sync.Mutex
	texts        []string
	capabilities platform.Capabilities
	options      platform.StreamOptions
	stream       *adminCommandTestStream
}

func newAdminCommandTestReplier() *adminCommandTestReplier {
	return &adminCommandTestReplier{}
}

func newAdminStreamingCommandTestReplier() *adminCommandTestReplier {
	return &adminCommandTestReplier{
		capabilities: platform.Capabilities{Text: true, Streaming: true},
		stream:       &adminCommandTestStream{},
	}
}

func (r *adminCommandTestReplier) Capabilities() platform.Capabilities {
	if r.capabilities == (platform.Capabilities{}) {
		return platform.Capabilities{Text: true}
	}
	return r.capabilities
}

func (r *adminCommandTestReplier) SendText(ctx context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return nil
}

func (r *adminCommandTestReplier) SendImage(ctx context.Context, localPath string) error {
	return nil
}

func (r *adminCommandTestReplier) SendFile(ctx context.Context, localPath string) error {
	return nil
}

func (r *adminCommandTestReplier) Typing(ctx context.Context, on bool) error {
	return nil
}

func (r *adminCommandTestReplier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.options = opts
	return r.stream, nil
}

func (r *adminCommandTestReplier) snapshotTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

type adminCommandTestStream struct {
	mu        sync.Mutex
	completed string
	failed    string
}

func (s *adminCommandTestStream) Update(context.Context, string) error { return nil }

func (s *adminCommandTestStream) Complete(_ context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = content
	return nil
}

func (s *adminCommandTestStream) Fail(_ context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = content
	return nil
}

func (s *adminCommandTestStream) waitCompleted(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		completed := s.completed
		s.mu.Unlock()
		if completed != "" {
			return completed
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for stream completion")
	return ""
}

func (s *adminCommandTestStream) waitFailed(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		failed := s.failed
		s.mu.Unlock()
		if failed != "" {
			return failed
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for stream failure")
	return ""
}

func (r *adminCommandTestReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	return nil
}

func (r *adminCommandTestReplier) waitTexts(t *testing.T, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.texts) >= want {
			texts := append([]string(nil), r.texts...)
			r.mu.Unlock()
			return texts
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("reply texts=%#v, want at least %d", r.texts, want)
	return nil
}

func waitForClosedChannel(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", name)
	}
}

func assertChannelNotClosed(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s", name)
	case <-time.After(100 * time.Millisecond):
	}
}
