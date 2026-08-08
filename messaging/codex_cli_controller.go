package messaging

import (
	"context"
	"fmt"

	"github.com/fastclaw-ai/weclaw/agent"
)

// PrepareCodexCLIHost delegates Host preparation to the same Agent instance
// used by mobile frontends, so auto/daemon/managed resolution cannot drift.
func (h *Handler) PrepareCodexCLIHost(ctx context.Context) (agent.CodexCLIHost, error) {
	name, runtimeAgent, err := h.getCodexSessionAgent(ctx)
	if err != nil {
		return agent.CodexCLIHost{}, err
	}
	controller, ok := runtimeAgent.(agent.CodexCLIHostController)
	if !ok {
		return agent.CodexCLIHost{}, fmt.Errorf("Agent %q 不支持受控 Codex CLI", name)
	}
	return controller.PrepareCodexCLIHost(ctx)
}
