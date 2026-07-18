package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCodexIndependentWriterCommandsAreDisabled(t *testing.T) {
	commands := []string{"/cx app", "/cx cli", "/cx attach", "/cx detach"}
	for index, command := range commands {
		t.Run(command, func(t *testing.T) {
			h := NewHandler(nil, nil)
			ag := &fakeVisibleCodexAgent{
				fakeCodexThreadAgent: fakeCodexThreadAgent{
					fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "companion", Command: "codex"}},
				},
				detachOK: true,
			}
			h.defaultName, h.agents["codex"] = "codex", ag
			client, calls, closeServer := newRecordingILinkClient(t)
			defer closeServer()

			handleTestWeChatMessage(h, context.Background(), client, newTextMessage(int64(300+index), command))

			text := strings.Join(calls.texts(), "\n")
			if !strings.Contains(text, "已停用") || !strings.Contains(text, "单一共享 app-server") ||
				!strings.Contains(text, "第二个 writer") {
				t.Fatalf("reply=%q", text)
			}
			if ag.openCalls != 0 || ag.detachCalls != 0 {
				t.Fatalf("disabled command invoked writer entry: attach=%d detach=%d", ag.openCalls, ag.detachCalls)
			}
		})
	}
}
