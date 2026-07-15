package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexRuntimeResolution struct {
	Request  agent.CodexRuntimeRequest
	Binding  agent.CodexThreadBinding
	Rollout  codexRolloutTaskState
	Live     bool
	ProbeErr error
}

type codexRuntimeResolveOptions struct {
	route    codexConversationRoute
	threadID string
	ag       agent.Agent
}

// resolveCodexRuntime 每次从持久化控制意图出发重新探测实际运行位置。
func (h *Handler) resolveCodexRuntime(ctx context.Context, opts codexRuntimeResolveOptions) (codexRuntimeResolution, error) {
	unlock := h.lockCodexThreadControl(strings.TrimSpace(opts.threadID))
	defer unlock()
	return h.resolveCodexRuntimeLocked(ctx, opts)
}

// resolveCodexRuntimeLocked 要求调用方已持有 thread 控制锁。
func (h *Handler) resolveCodexRuntimeLocked(ctx context.Context, opts codexRuntimeResolveOptions) (codexRuntimeResolution, error) {
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
	request, rollout, err := h.buildCodexRuntimeRequest(opts.route, threadID)
	if err != nil {
		return codexRuntimeResolution{}, err
	}
	binding, probeErr := liveAgent.InspectCodexRuntime(ctx, request)
	resolution := codexRuntimeResolution{
		Request: request, Binding: binding, Rollout: rollout, Live: true, ProbeErr: probeErr,
	}
	if probeErr != nil && !errors.Is(probeErr, agent.ErrCodexDesktopOwnershipUnknown) &&
		!errors.Is(probeErr, agent.ErrCodexRuntimeConflict) {
		return resolution, probeErr
	}
	if binding.Ref.ThreadID == "" {
		resolution.Binding = unknownCodexRuntimeBinding(request)
	}
	return resolution, nil
}

// buildCodexRuntimeRequest 组合 thread、控制 revision 与 rollout 检查点。
func (h *Handler) buildCodexRuntimeRequest(route codexConversationRoute, threadID string) (agent.CodexRuntimeRequest, codexRolloutTaskState, error) {
	intent := h.ensureCodexSessions().controlIntent(threadID)
	rollout, _, err := h.readLocalCodexRolloutTaskState(threadID)
	if err != nil {
		return agent.CodexRuntimeRequest{}, codexRolloutTaskState{}, err
	}
	request := agent.CodexRuntimeRequest{
		Ref:        agent.CodexThreadRef{ConversationID: route.conversationID, ThreadID: threadID},
		Intent:     agentControlIntent(intent),
		Checkpoint: agentRolloutCheckpoint(rollout),
	}
	return request, rollout, nil
}

func agentControlIntent(intent codexControlIntent) agent.CodexControlIntent {
	return agent.CodexControlIntent{
		Owner: agent.CodexControlOwner(intent.Owner), RouteKey: intent.RouteBindingKey,
		ConversationID: intent.ConversationID, Revision: intent.Revision,
	}
}

func agentRolloutCheckpoint(state codexRolloutTaskState) agent.CodexRolloutCheckpoint {
	return agent.CodexRolloutCheckpoint{
		Path: state.Path, TurnID: state.TurnID, Offset: state.Offset, Size: state.Size, Active: state.Active,
	}
}

func unknownCodexRuntimeBinding(request agent.CodexRuntimeRequest) agent.CodexThreadBinding {
	return agent.CodexThreadBinding{
		Ref: request.Ref, Control: request.Intent, Runtime: agent.CodexRuntimeUnknown,
		State: agent.CodexThreadState{ThreadID: request.Ref.ThreadID},
	}
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
		Ref: opts.route.ref(opts.threadID), Runtime: agent.CodexRuntimeWeClaw,
		State: agent.CodexThreadState{ThreadID: opts.threadID},
	}}, nil
}

func (route codexConversationRoute) ref(threadID string) agent.CodexThreadRef {
	return agent.CodexThreadRef{ConversationID: route.conversationID, ThreadID: threadID}
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

// ensureCodexRuntimeReady 同时核对持久化控制方、当前窗口和实际 writer。
func ensureCodexRuntimeReady(resolution codexRuntimeResolution, route codexConversationRoute) error {
	if !resolution.Live {
		return nil
	}
	intent := resolution.Request.Intent
	if err := ensureCodexRouteOwnsControl(intent, route); err != nil {
		return err
	}
	switch resolution.Binding.Runtime {
	case agent.CodexRuntimeDesktop, agent.CodexRuntimeWeClaw:
		return nil
	case agent.CodexRuntimeConflict:
		return agent.ErrCodexRuntimeConflict
	default:
		return agent.ErrCodexRuntimeUnavailable
	}
}

func ensureCodexRouteOwnsControl(intent agent.CodexControlIntent, route codexConversationRoute) error {
	switch intent.Owner {
	case agent.CodexControlUnclaimed:
		return fmt.Errorf("当前 Codex 会话未由本窗口控制；请重新选择会话或发送 /cx owner remote")
	case agent.CodexControlDesktop:
		return fmt.Errorf("当前 Codex 会话已归还 Codex Desktop；请重新选择会话或发送 /cx owner remote")
	case agent.CodexControlRemote:
		if intent.RouteKey != route.bindingKey || intent.ConversationID != route.conversationID {
			return fmt.Errorf("当前 Codex 会话由另一个消息窗口远程控制")
		}
		return nil
	default:
		return agent.ErrCodexControlRequired
	}
}

func codexResolutionModelStatus(resolution codexRuntimeResolution, fallback sessionModelStatus) sessionModelStatus {
	state := resolution.Binding.State
	if strings.TrimSpace(state.Model) == "" && strings.TrimSpace(state.Effort) == "" {
		return fallback
	}
	return sessionModelStatus{Model: state.Model, Effort: state.Effort}
}

func renderCodexControlOwner(owner agent.CodexControlOwner) string {
	switch owner {
	case agent.CodexControlDesktop:
		return "Codex Desktop"
	case agent.CodexControlRemote:
		return "远程窗口"
	default:
		return "未认领"
	}
}

func renderCodexRuntimeHolder(runtime agent.CodexRuntimeHolder) string {
	switch runtime {
	case agent.CodexRuntimeDesktop:
		return "Codex Desktop"
	case agent.CodexRuntimeWeClaw:
		return "WeClaw app-server"
	case agent.CodexRuntimeConflict:
		return "写入冲突"
	default:
		return "未确认"
	}
}
