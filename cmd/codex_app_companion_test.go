package cmd

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCreateCompanionRuntimeSupportsCodexApp(t *testing.T) {
	runtime, err := createCompanionRuntime(agent.CompanionEndpoint{
		Agent:   "codex",
		Command: "codex",
		Cwd:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("createCompanionRuntime() error = %v, want nil", err)
	}
	if runtime == nil {
		t.Fatal("createCompanionRuntime() = nil, want codex runtime")
	}
}

func TestHandleCodexAppMessageFiltersThreadAndBuildsReply(t *testing.T) {
	resultCh := make(chan codexAppTurnResult, 1)
	state := &codexAppTurnState{}
	var progress []string

	done := handleCodexAppMessage([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"other","turnId":"turn-1","delta":"bad"}}`), "thread-1", "turn-1", state, func(text string) {
		progress = append(progress, text)
	}, resultCh)
	if done {
		t.Fatal("其他 thread 的事件不应结束 turn")
	}

	done = handleCodexAppMessage([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","delta":"O"}}`), "thread-1", "turn-1", state, func(text string) {
		progress = append(progress, text)
	}, resultCh)
	if done {
		t.Fatal("delta 事件不应结束 turn")
	}
	done = handleCodexAppMessage([]byte(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","text":"OK"}}}`), "thread-1", "turn-1", state, nil, resultCh)
	if done {
		t.Fatal("agentMessage completed 事件不应结束 turn")
	}
	done = handleCodexAppMessage([]byte(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`), "thread-1", "turn-1", state, nil, resultCh)
	if !done {
		t.Fatal("turn/completed 应结束 turn")
	}

	result := <-resultCh
	if result.err != nil || result.text != "OK" {
		t.Fatalf("result = %#v, want OK", result)
	}
	if strings.Join(progress, "") != "O" {
		t.Fatalf("progress = %#v, want O", progress)
	}
}

func TestHandleCodexAppMessageReturnsTargetError(t *testing.T) {
	resultCh := make(chan codexAppTurnResult, 1)
	state := &codexAppTurnState{}

	done := handleCodexAppMessage([]byte(`{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","error":{"message":"额度不足"}}}`), "thread-1", "turn-1", state, nil, resultCh)
	if !done {
		t.Fatal("target error 应结束 turn")
	}
	result := <-resultCh
	if result.err == nil || !strings.Contains(result.err.Error(), "额度不足") {
		t.Fatalf("result = %#v, want target error", result)
	}
}
