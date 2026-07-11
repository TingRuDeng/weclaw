package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexRuntimeResolution struct {
	Binding agent.CodexThreadBinding
	Rollout codexRolloutTaskState
}

type codexRuntimeResolveOptions struct {
	route    codexConversationRoute
	threadID string
	ag       agent.Agent
}

// resolveCodexRuntime 先绑定实时 owner，再决定是否允许 app-server 恢复。
func (h *Handler) resolveCodexRuntime(ctx context.Context, opts codexRuntimeResolveOptions) (codexRuntimeResolution, error) {
	threadID := strings.TrimSpace(opts.threadID)
	if threadID == "" {
		return codexRuntimeResolution{}, nil
	}
	if err := h.guardCodexThreadSwitch(opts.route, threadID); err != nil {
		return codexRuntimeResolution{}, err
	}
	liveAgent, ok := opts.ag.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return h.resolveLegacyCodexRuntime(ctx, opts)
	}
	ref := agent.CodexThreadRef{ConversationID: opts.route.conversationID, ThreadID: threadID}
	binding, bindErr := liveAgent.BindCodexThread(ctx, ref)
	if bindErr != nil && !isCodexOwnerStateError(bindErr) {
		return codexRuntimeResolution{}, bindErr
	}
	resolution := codexRuntimeResolution{Binding: binding}
	if binding.Owner == agent.CodexOwnerDesktopLive || binding.Owner == agent.CodexOwnerWeClawRuntime {
		return resolution, nil
	}
	rollout, _, err := h.readLocalCodexRolloutTaskState(threadID)
	if err != nil {
		return resolution, err
	}
	resolution.Rollout = rollout
	if binding.Owner != agent.CodexOwnerPersistedOnly || rollout.Active || h.hasExternalCodexTask(opts.route.conversationID) {
		return resolution, nil
	}
	if err := liveAgent.RecoverCodexThread(ctx, ref); err != nil {
		return resolution, err
	}
	resolution.Binding.Owner = agent.CodexOwnerWeClawRuntime
	resolution.Binding.Connected = true
	return resolution, nil
}

func (h *Handler) resolveLegacyCodexRuntime(ctx context.Context, opts codexRuntimeResolveOptions) (codexRuntimeResolution, error) {
	codexAgent, ok := opts.ag.(agent.CodexThreadAgent)
	if !ok {
		return codexRuntimeResolution{}, fmt.Errorf("当前 Codex Agent 不支持 thread 切换")
	}
	current, hasCurrent := codexAgent.CurrentCodexThread(opts.route.conversationID)
	if !hasCurrent || current != opts.threadID {
		if err := codexAgent.UseCodexThread(ctx, opts.route.conversationID, opts.threadID); err != nil {
			return codexRuntimeResolution{}, err
		}
	}
	return codexRuntimeResolution{Binding: agent.CodexThreadBinding{
		Ref:   agent.CodexThreadRef{ConversationID: opts.route.conversationID, ThreadID: opts.threadID},
		Owner: agent.CodexOwnerWeClawRuntime, Connected: true,
	}}, nil
}

func (h *Handler) guardCodexThreadSwitch(route codexConversationRoute, targetThreadID string) error {
	task, active := h.activeTask(route.conversationID)
	if !active || task == nil {
		return nil
	}
	currentThreadID, pending := h.ensureCodexSessions().getThread(route.bindingKey, route.workspaceRoot)
	if !pending && currentThreadID != "" && currentThreadID != targetThreadID {
		return fmt.Errorf("当前任务执行期间不能切换到其他 Codex 会话")
	}
	return nil
}

func (h *Handler) hasExternalCodexTask(conversationID string) bool {
	task, ok := h.activeTask(conversationID)
	if !ok || task == nil {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.externalCodex
}

func isCodexOwnerStateError(err error) bool {
	return errors.Is(err, agent.ErrCodexDesktopOwnershipUnknown) || errors.Is(err, agent.ErrCodexDesktopDisconnected)
}

func ensureCodexRuntimeReady(resolution codexRuntimeResolution) error {
	switch resolution.Binding.Owner {
	case agent.CodexOwnerDesktopLive, agent.CodexOwnerWeClawRuntime:
		return nil
	case agent.CodexOwnerDesktopDisconnected:
		return agent.ErrCodexDesktopDisconnected
	case agent.CodexOwnerUnknown:
		return agent.ErrCodexDesktopOwnershipUnknown
	case agent.CodexOwnerPersistedOnly:
		if resolution.Rollout.Active {
			return fmt.Errorf("Codex App 本地任务仍在执行，暂不能恢复")
		}
		return agent.ErrCodexDesktopDisconnected
	default:
		return agent.ErrCodexDesktopOwnershipUnknown
	}
}

func codexResolutionModelStatus(resolution codexRuntimeResolution, fallback sessionModelStatus) sessionModelStatus {
	state := resolution.Binding.State
	if strings.TrimSpace(state.Model) == "" && strings.TrimSpace(state.Effort) == "" {
		return fallback
	}
	return sessionModelStatus{Model: state.Model, Effort: state.Effort}
}

func renderCodexOwnerNotice(resolution codexRuntimeResolution) []string {
	switch resolution.Binding.Owner {
	case agent.CodexOwnerUnknown:
		return []string{"Codex Desktop thread 所有权未知，当前不会通过 app-server 恢复。"}
	case agent.CodexOwnerDesktopDisconnected:
		return []string{"Codex Desktop 当前已断开，等待重连后可继续远程接管。"}
	case agent.CodexOwnerPersistedOnly:
		return []string{"Codex 本地任务尚未确认可恢复，当前保持只读绑定。"}
	default:
		return nil
	}
}
