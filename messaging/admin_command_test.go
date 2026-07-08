package messaging

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

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
	if !strings.Contains(texts[0], "开始执行管理命令：/update") {
		t.Fatalf("reply texts=%#v, want start notice", texts)
	}
	if !strings.Contains(texts[1], "当前已是最新版本") {
		t.Fatalf("reply texts=%#v, want concise update result", texts)
	}
	if strings.Contains(texts[1], "Checking for updates") {
		t.Fatalf("reply texts=%#v, should not expose raw update output", texts)
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
		Metadata: map[string]string{"feishu_union_id": "on_admin"},
	}, reply)

	texts := reply.waitTexts(t, 2)
	if gotCommand != "update" {
		t.Fatalf("executor command=%q, want update", gotCommand)
	}
	if !strings.Contains(texts[0], "开始执行管理命令：/update") {
		t.Fatalf("reply texts=%#v, want start notice", texts)
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
	if !strings.Contains(texts[0], "开始执行管理命令：/restart") {
		t.Fatalf("reply texts=%#v, want start notice", texts)
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
		Metadata: map[string]string{"feishu_union_id": "on_admin"},
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
		Metadata: map[string]string{"feishu_union_id": "on_admin"},
	}, reply)
	waitForClosedChannel(t, updateStarted, "update start")
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/restart --force",
		Metadata: map[string]string{"feishu_union_id": "on_admin"},
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
		Metadata: map[string]string{"feishu_union_id": "on_admin"},
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
	mu    sync.Mutex
	texts []string
}

func newAdminCommandTestReplier() *adminCommandTestReplier {
	return &adminCommandTestReplier{}
}

func (r *adminCommandTestReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
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
	return nil, nil
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
