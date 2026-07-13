package messaging

import (
	"context"
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

func TestHandleClaudeCliOpensCurrentSession(t *testing.T) {
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
	if err := h.claudeSessions.commitSelection(claudeBindingKey("user-1", "claude"), workspace, "session-current"); err != nil {
		t.Fatal(err)
	}
	var opened []recordedClaudeCLIResume
	h.SetClaudeCLIResumeOpener(func(_ context.Context, request ClaudeCLIResumeRequest) error {
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
	if !containsText(calls.texts(), "已打开 Claude CLI") {
		t.Fatalf("reply should mention opened cli, messages=%#v", calls.texts())
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
	if err := h.claudeSessions.commitSelection(bindingKey, workspace, sessionID); err != nil {
		t.Fatal(err)
	}
	return h, claudeSessionRoute{
		Context: context.Background(), ActorUserID: "user-1", UserID: "user-1",
		AgentName: "claude", Agent: ag, WorkspaceRoot: workspace, BindingKey: bindingKey,
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

	if reply := h.handleClaudeCLI(route); !strings.Contains(reply, "任务正在运行") {
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
