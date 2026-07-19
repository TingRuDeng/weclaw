package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
)

func (h *Handler) codexAccountAgent(ctx context.Context) (agent.CodexAccountAgent, error) {
	name, runtimeAgent, err := h.getCodexSessionAgent(ctx)
	if err != nil {
		return nil, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "Codex Agent 不可用", err)
	}
	accountAgent, ok := runtimeAgent.(agent.CodexAccountAgent)
	if !ok {
		return nil, codexauth.NewError(codexauth.CodeRuntimeUnavailable, fmt.Sprintf("Agent %q 不支持 Codex 多账号", name), nil)
	}
	return accountAgent, nil
}

func (h *Handler) ListCodexAccounts(ctx context.Context) (agent.CodexAccountStatus, error) {
	accountAgent, err := h.codexAccountAgent(ctx)
	if err != nil {
		return agent.CodexAccountStatus{}, err
	}
	return accountAgent.ListCodexAccounts(ctx)
}

func (h *Handler) CurrentCodexAccount(ctx context.Context, withQuota bool) (agent.CodexAccountStatus, error) {
	accountAgent, err := h.codexAccountAgent(ctx)
	if err != nil {
		return agent.CodexAccountStatus{}, err
	}
	return accountAgent.CurrentCodexAccount(ctx, withQuota)
}

func (h *Handler) SaveCodexAccount(ctx context.Context, options agent.CodexAccountSaveOptions) (agent.CodexAccountProfile, error) {
	if count := h.activeCodexTaskCount(); count > 0 {
		return agent.CodexAccountProfile{}, codexauth.NewError(codexauth.CodeBusy, fmt.Sprintf("当前还有 %d 个 Codex 任务，不能保存账号", count), nil)
	}
	accountAgent, err := h.codexAccountAgent(ctx)
	if err != nil {
		return agent.CodexAccountProfile{}, err
	}
	return accountAgent.SaveCodexAccount(ctx, options)
}

func (h *Handler) UseCodexAccount(ctx context.Context, reference string, expectedRevision uint64) (agent.CodexAccountSwitchResult, error) {
	if count := h.activeCodexTaskCount(); count > 0 {
		return agent.CodexAccountSwitchResult{}, codexauth.NewError(codexauth.CodeBusy, fmt.Sprintf("当前还有 %d 个 Codex 任务，不能切换账号", count), nil)
	}
	accountAgent, err := h.codexAccountAgent(ctx)
	if err != nil {
		return agent.CodexAccountSwitchResult{}, err
	}
	return accountAgent.UseCodexAccount(ctx, strings.TrimSpace(reference), expectedRevision)
}

func (h *Handler) RemoveCodexAccount(ctx context.Context, reference string) error {
	accountAgent, err := h.codexAccountAgent(ctx)
	if err != nil {
		return err
	}
	return accountAgent.RemoveCodexAccount(ctx, strings.TrimSpace(reference))
}

func (h *Handler) DoctorCodexAccounts(ctx context.Context) codexauth.DoctorResult {
	accountAgent, err := h.codexAccountAgent(ctx)
	if err != nil {
		return codexauth.DoctorResult{Message: err.Error()}
	}
	return accountAgent.DoctorCodexAccounts(ctx)
}

// activeCodexTaskCount 覆盖 thread 尚未 materialize 的启动窗口，因此同时检查
// task 中的 codexThreadID 和已配置 Agent 身份。
func (h *Handler) activeCodexTaskCount() int {
	codexNames := make(map[string]struct{})
	h.mu.RLock()
	for name, runtimeAgent := range h.agents {
		if isCodexAgent(name, runtimeAgent.Info()) {
			codexNames[name] = struct{}{}
		}
	}
	for _, meta := range h.agentMetas {
		if isCodexAgent(meta.Name, agent.AgentInfo{Name: meta.Name, Type: meta.Type, Command: meta.Command}) {
			codexNames[meta.Name] = struct{}{}
		}
	}
	h.mu.RUnlock()

	count := 0
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	for _, task := range h.activeTasks {
		if task == nil {
			continue
		}
		task.mu.Lock()
		_, configuredCodex := codexNames[task.agentName]
		isCodexTask := strings.TrimSpace(task.codexThreadID) != "" || configuredCodex
		running := !task.detached && task.phase != codexTaskTerminal
		task.mu.Unlock()
		if isCodexTask && running {
			count++
		}
	}
	return count
}
