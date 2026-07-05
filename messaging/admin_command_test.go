package messaging

import (
	"context"
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
		Platform: platform.PlatformFeishu,
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
	if !strings.Contains(texts[1], "Already up to date") {
		t.Fatalf("reply texts=%#v, want update command output", texts)
	}
}

func TestServiceAdminCommandAllowsRestartForceOnly(t *testing.T) {
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

func TestServiceAdminCommandRejectsUnsupportedArgs(t *testing.T) {
	calls := 0
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"ou_admin"})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		calls++
		return "should not run", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Text:     "/update --restart",
	}, reply)

	if calls != 0 {
		t.Fatalf("admin executor calls=%d, want 0 for unsupported args", calls)
	}
	texts := reply.waitTexts(t, 1)
	if len(texts) != 1 || !strings.Contains(texts[0], "不支持参数") {
		t.Fatalf("reply texts=%#v, want unsupported args notice", texts)
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
