package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

const claudeCLIConcurrencyProbeDelay = 50 * time.Millisecond

func TestHandleClaudeCLIOpensCurrentSession(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{
				Name: "claude", Type: "acp", Command: "claude-agent-acp", LocalCommand: "claude",
			},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	bindingKey := seedClaudeRemoteControl(t, h, "user-1", "claude", workspace, "session-current", 1)
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	if err := ag.UseClaudeSession(context.Background(), conversationID, "session-current"); err != nil {
		t.Fatal(err)
	}
	var opened []recordedClaudeCLIResume
	h.SetClaudeCLIResumeOpener(func(_ context.Context, request ClaudeCLIResumeRequest) error {
		if intent := h.ensureClaudeSessions().controlIntent("session-current"); intent.Owner != claudeOwnerLocal {
			t.Fatalf("opener intent=%+v, want local", intent)
		}
		if _, ok := ag.CurrentClaudeSession(conversationID); ok {
			t.Fatal("opener 调用前 ACP runtime 未清理")
		}
		opened = append(opened, recordedClaudeCLIResume{
			command: request.Command, workspace: request.WorkspaceRoot, sessionID: request.SessionID,
		})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(303, "/cc cli"))

	if len(opened) != 1 || opened[0].workspace != workspace || opened[0].sessionID != "session-current" {
		t.Fatalf("opened=%#v, want current session in workspace %s", opened, workspace)
	}
	if opened[0].command != "claude" {
		t.Fatalf("command=%q, want local_command claude", opened[0].command)
	}
	if !containsText(calls.texts(), "已释放远程控制并打开 Claude CLI") {
		t.Fatalf("reply should mention opened cli, messages=%#v", calls.texts())
	}
	if intent := h.ensureClaudeSessions().controlIntent("session-current"); intent.Owner != claudeOwnerLocal || intent.BindingKey != "" {
		t.Fatalf("intent=%+v, want local", intent)
	}
	if binding := h.ensureClaudeSessions().binding(bindingKey); binding.SessionID != "session-current" {
		t.Fatalf("binding=%+v, want retained selection", binding)
	}
	replier := platformtest.NewReplier(platform.Capabilities{Text: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.startAgentTask(agentTaskOptions{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "user-1", routeUserID: "user-1", reply: replier,
		agentName: "claude", message: "交接后的远程任务", agent: ag, progressCfg: cfg,
	})
	if h.ActiveTaskCount() != 0 || !containsText(replier.Texts, "/cc owner remote") {
		t.Fatalf("active=%d texts=%#v, want local owner rejection", h.ActiveTaskCount(), replier.Texts)
	}
}

// newClaudeCLIEntryRoute 创建可直接调用本地交接逻辑的 ACP 会话路由。
func newClaudeCLIEntryRoute(t *testing.T, workspace string, sessionID string) (*Handler, claudeSessionRoute) {
	t.Helper()
	h := NewHandler(nil, nil)
	ag := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "claude", Type: "acp", Command: "claude-agent-acp", LocalCommand: "claude",
	}}}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	bindingKey := claudeBindingKey("user-1", "claude")
	seedClaudeRemoteControl(t, h, "user-1", "claude", workspace, sessionID, 1)
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	if err := ag.UseClaudeSession(context.Background(), conversationID, sessionID); err != nil {
		t.Fatal(err)
	}
	ag.catalogSessions = []agent.ClaudeSession{{ID: sessionID, Cwd: workspace}}
	return h, claudeSessionRoute{
		Context: context.Background(), ActorUserID: "user-1", UserID: "user-1",
		AgentName: "claude", Agent: ag, WorkspaceRoot: workspace, BindingKey: bindingKey, Admin: true,
	}
}

func TestHandleClaudeCLIOpenerFailureRestoresRemoteOwner(t *testing.T) {
	workspace := t.TempDir()
	h, route := newClaudeCLIEntryRoute(t, workspace, "session-current")
	h.SetClaudeCLIResumeOpener(func(context.Context, ClaudeCLIResumeRequest) error {
		return errors.New("terminal unavailable")
	})

	reply := h.handleClaudeCLI(route)
	intent := h.ensureClaudeSessions().controlIntent("session-current")
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, workspace)
	current, bound := route.Agent.(*fakeClaudeSessionAgent).CurrentClaudeSession(conversationID)
	if !strings.Contains(reply, "打开 Claude CLI 失败") || !strings.Contains(reply, "已恢复远程控制") ||
		intent.Owner != claudeOwnerRemote || intent.BindingKey != route.BindingKey || intent.Revision <= 1 ||
		!bound || current != "session-current" {
		t.Fatalf("reply=%q intent=%+v runtime=%q/%v", reply, intent, current, bound)
	}
}

func TestHandleClaudeCLICompensationFailureStaysFailClosed(t *testing.T) {
	workspace := t.TempDir()
	h, route := newClaudeCLIEntryRoute(t, workspace, "session-current")
	route.Agent.(*fakeClaudeSessionAgent).useErr = errors.New("resume failed")
	h.SetClaudeCLIResumeOpener(func(context.Context, ClaudeCLIResumeRequest) error {
		return errors.New("terminal unavailable")
	})

	reply := h.handleClaudeCLI(route)
	intent := h.ensureClaudeSessions().controlIntent("session-current")
	if !strings.Contains(reply, "远程恢复未确认") || intent.Owner == claudeOwnerRemote ||
		strings.Contains(reply, "terminal unavailable") || strings.Contains(reply, "resume failed") {
		t.Fatalf("reply=%q intent=%+v", reply, intent)
	}
}

func TestHandleClaudeCLIRejectsNonOwnerWithoutOpening(t *testing.T) {
	for _, test := range []struct {
		name   string
		intent claudeControlIntent
		want   string
	}{
		{name: "local", intent: claudeControlIntent{Owner: claudeOwnerLocal, Revision: 2}, want: "/cc owner remote"},
		{name: "unclaimed", intent: claudeControlIntent{Owner: claudeOwnerUnclaimed, Revision: 2}, want: "/cc owner remote"},
		{name: "other route", intent: claudeControlIntent{
			Owner: claudeOwnerRemote, BindingKey: claudeBindingKey("other", "claude"),
			ConversationID: "other-conversation", Revision: 2,
		}, want: "其他远程窗口"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, route := newClaudeCLIEntryRoute(t, t.TempDir(), "session-current")
			h.ensureClaudeSessions().controls["session-current"] = test.intent
			opened := false
			h.SetClaudeCLIResumeOpener(func(context.Context, ClaudeCLIResumeRequest) error {
				opened = true
				return nil
			})
			reply := h.handleClaudeCLI(route)
			if opened || !strings.Contains(reply, test.want) {
				t.Fatalf("opened=%v reply=%q, want %q", opened, reply, test.want)
			}
		})
	}
}

func TestHandleClaudeCLIRejectsMissingSessionWithoutOpening(t *testing.T) {
	h, route := newClaudeCLIEntryRoute(t, t.TempDir(), "session-current")
	store := h.ensureClaudeSessions()
	store.bindings[route.BindingKey] = newClaudeBinding(route.WorkspaceRoot, "", claudeBindingUnbound)
	opened := false
	h.SetClaudeCLIResumeOpener(func(context.Context, ClaudeCLIResumeRequest) error {
		opened = true
		return nil
	})
	if reply := h.handleClaudeCLI(route); opened || !strings.Contains(reply, "没有可接手") {
		t.Fatalf("opened=%v reply=%q", opened, reply)
	}
}

func TestHandleClaudeCLICompensationDoesNotOverwriteConcurrentOwner(t *testing.T) {
	workspace := t.TempDir()
	h, route := newClaudeCLIEntryRoute(t, workspace, "session-current")
	other := route
	other.ActorUserID = "user-2"
	other.UserID = "user-2"
	other.BindingKey = claudeBindingKey("user-2", "claude")
	entered := make(chan struct{})
	release := make(chan struct{})
	h.SetClaudeCLIResumeOpener(func(context.Context, ClaudeCLIResumeRequest) error {
		close(entered)
		<-release
		return errors.New("terminal unavailable")
	})
	cliDone := make(chan string, 1)
	go func() { cliDone <- h.handleClaudeCLI(route) }()
	<-entered
	acquireDone := make(chan error, 1)
	go func() {
		_, err := h.acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest{
			Route:    other,
			Selected: agent.ClaudeSession{ID: "session-current", Cwd: workspace},
			Command:  "concurrent test",
		})
		acquireDone <- err
	}()
	select {
	case err := <-acquireDone:
		t.Fatalf("concurrent acquire bypassed opener critical section: %v", err)
	case <-time.After(claudeCLIConcurrencyProbeDelay):
	}
	close(release)
	if err := <-acquireDone; err != nil {
		t.Fatalf("concurrent acquire: %v", err)
	}
	reply := <-cliDone
	intent := h.ensureClaudeSessions().controlIntent("session-current")
	if !strings.Contains(reply, "远程恢复未确认") || intent.Owner != claudeOwnerRemote || intent.BindingKey != other.BindingKey {
		t.Fatalf("reply=%q intent=%+v, want concurrent owner preserved", reply, intent)
	}
}

func TestHandleClaudeCLIRejectsRunningSession(t *testing.T) {
	workspace := t.TempDir()
	h, route := newClaudeCLIEntryRoute(t, workspace, "session-current")
	key := buildClaudeConversationID(route.UserID, route.AgentName, workspace)
	task, _, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{owner: route.ActorUserID})
	if !started {
		t.Fatal("活动任务登记失败")
	}
	defer h.finishActiveTask(key, task)
	opened := false
	h.SetClaudeCLIResumeOpener(func(context.Context, ClaudeCLIResumeRequest) error {
		opened = true
		return nil
	})

	if reply := h.handleClaudeCLI(route); !strings.Contains(reply, "任务正在运行") || opened {
		t.Fatalf("reply=%q, want running rejection", reply)
	}
}

func TestHandleClaudeCLIRejectsInvalidSessionID(t *testing.T) {
	h, route := newClaudeCLIEntryRoute(t, t.TempDir(), "session;rm -rf")
	if reply := h.handleClaudeCLI(route); !strings.Contains(reply, "session ID 非法") {
		t.Fatalf("reply=%q, want invalid session rejection", reply)
	}
}

func TestHandleClaudeCLIRejectsUnauthorizedWorkspace(t *testing.T) {
	allowed := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	h, route := newClaudeCLIEntryRoute(t, outside, "session-current")
	route.Admin = false
	h.SetAllowedWorkspaceRoots([]string{allowed})
	h.SetAgentWorkDirs(map[string]string{"claude": allowed})

	if reply := h.handleClaudeCLI(route); !strings.Contains(reply, "工作空间不在允许范围") {
		t.Fatalf("reply=%q, want workspace rejection", reply)
	}
}

func TestClaudeCLIResumeCommandUsesNativeArguments(t *testing.T) {
	command := claudeCLIResumeCommand("/opt/Claude Code/claude", "/tmp/work space", "session-1")
	for _, want := range []string{"cd '/tmp/work space'", "'/opt/Claude Code/claude' --resume 'session-1'"} {
		if !strings.Contains(command, want) {
			t.Fatalf("command=%q, want %q", command, want)
		}
	}
}

// setBlockingClaudeCLIOpener 安装阻塞 opener，便于观测交接临界区。
func setBlockingClaudeCLIOpener(h *Handler) (<-chan ClaudeCLIResumeRequest, chan<- struct{}) {
	entered := make(chan ClaudeCLIResumeRequest, 1)
	release := make(chan struct{})
	h.SetClaudeCLIResumeOpener(func(_ context.Context, request ClaudeCLIResumeRequest) error {
		entered <- request
		<-release
		return nil
	})
	return entered, release
}

// assertClaudeCLIOperationBlocked 验证并发操作在交接临界区内尚未完成。
func assertClaudeCLIOperationBlocked(t *testing.T, done <-chan string) {
	t.Helper()
	select {
	case result := <-done:
		t.Fatalf("并发操作未等待本地交接，result=%q", result)
	case <-time.After(claudeCLIConcurrencyProbeDelay):
	}
}

func TestHandleClaudeCLISerializesConcurrentNewSession(t *testing.T) {
	workspace := t.TempDir()
	h, route := newClaudeCLIEntryRoute(t, workspace, "session-current")
	route.Agent.(*fakeClaudeSessionAgent).fakeAgent.resetSessionID = "session-new"
	entered, release := setBlockingClaudeCLIOpener(h)
	cliDone := make(chan string, 1)
	go func() { cliDone <- h.handleClaudeCLI(route) }()
	request := <-entered
	newDone := make(chan string, 1)
	go func() { newDone <- h.handleClaudeNew(route) }()
	assertClaudeCLIOperationBlocked(t, newDone)
	close(release)
	<-cliDone
	if result := <-newDone; !strings.Contains(result, "已创建") {
		t.Fatalf("result=%q, want new session after handoff", result)
	}
	if request.WorkspaceRoot != workspace || request.SessionID != "session-current" {
		t.Fatalf("request=%#v, want atomic old binding snapshot", request)
	}
}

func TestHandleClaudeCLISerializesConcurrentAgentTask(t *testing.T) {
	workspace := t.TempDir()
	h, route := newClaudeCLIEntryRoute(t, workspace, "session-current")
	entered, release := setBlockingClaudeCLIOpener(h)
	cliDone := make(chan string, 1)
	go func() { cliDone <- h.handleClaudeCLI(route) }()
	<-entered
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	taskDone := make(chan string, 1)
	go func() {
		h.startAgentTask(agentTaskOptions{
			ctx: context.Background(), platformName: platform.PlatformFeishu,
			userID: route.ActorUserID, routeUserID: route.UserID,
			reply:     platformtest.NewReplier(platform.Capabilities{Text: true}),
			agentName: route.AgentName, message: "远程任务", agent: route.Agent, progressCfg: cfg,
		})
		taskDone <- "已登记"
	}()
	assertClaudeCLIOperationBlocked(t, taskDone)
	close(release)
	<-cliDone
	<-taskDone
}
