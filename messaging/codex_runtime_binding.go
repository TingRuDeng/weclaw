package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// resolveBoundCodexRuntimeLocked 以持久化控制意图读取已建立的 runtime，不同步探测 Desktop。
func (h *Handler) resolveBoundCodexRuntimeLocked(opts codexRuntimeResolveOptions) (codexRuntimeResolution, error) {
	threadID := strings.TrimSpace(opts.threadID)
	if threadID == "" {
		return codexRuntimeResolution{}, nil
	}
	if err := h.guardCodexThreadSwitch(opts.route, threadID); err != nil {
		return codexRuntimeResolution{}, err
	}
	liveAgent, ok := opts.ag.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return codexRuntimeResolution{}, agent.ErrCodexRuntimeUnavailable
	}
	request, rollout, err := h.buildCodexRuntimeRequest(opts.route, threadID)
	if err != nil {
		return codexRuntimeResolution{}, err
	}
	binding, snapshotErr := liveAgent.CurrentCodexRuntime(request)
	return codexRuntimeResolution{
		Request: request, Binding: binding, Rollout: rollout, Live: true, ProbeErr: snapshotErr,
	}, snapshotErr
}

// buildCodexRuntimeRequest 组合 thread、控制 revision 与 rollout 检查点。
func (h *Handler) buildCodexRuntimeRequest(route codexConversationRoute, threadID string) (agent.CodexRuntimeRequest, codexRolloutTaskState, error) {
	intent := codexSharedHostIntent(route)
	rollout, _, err := h.readLocalCodexRolloutTaskState(threadID)
	if err != nil {
		return agent.CodexRuntimeRequest{}, codexRolloutTaskState{}, err
	}
	request := agent.CodexRuntimeRequest{
		Ref:        agent.CodexThreadRef{ConversationID: route.conversationID, ThreadID: threadID},
		Intent:     intent,
		Checkpoint: agentRolloutCheckpoint(rollout),
		PendingFirstTurn: h.ensureCodexSessions().isPendingFirstTurn(
			route.bindingKey, route.workspaceRoot, threadID,
		),
	}
	return request, rollout, nil
}

// buildCodexRuntimeRequestForTurn 让 rollout 读取失败降级为无 checkpoint 恢复，不能否决 remote owner 的写入。
func (h *Handler) buildCodexRuntimeRequestForTurn(route codexConversationRoute, threadID string) agent.CodexRuntimeRequest {
	request, _, err := h.buildCodexRuntimeRequest(route, threadID)
	if err == nil {
		return request
	}
	log.Printf("[codex-task] 首次写入忽略 rollout checkpoint 读取失败 thread=%q: %v", threadID, err)
	return agent.CodexRuntimeRequest{
		Ref:    route.ref(threadID),
		Intent: codexSharedHostIntent(route),
		PendingFirstTurn: h.ensureCodexSessions().isPendingFirstTurn(
			route.bindingKey, route.workspaceRoot, threadID,
		),
	}
}

func codexSharedHostIntent(route codexConversationRoute) agent.CodexControlIntent {
	return agent.CodexControlIntent{
		Owner: agent.CodexControlRemote, RouteKey: route.bindingKey,
		ConversationID: route.conversationID, Revision: 1,
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

// ensureCodexRuntimeReady 只核对单一 app-server 的真实可用性。消息 route
// 是 frontend binding，不再是互斥 writer owner。
func ensureCodexRuntimeReady(resolution codexRuntimeResolution, route codexConversationRoute) error {
	if !resolution.Live {
		return nil
	}
	_ = route
	switch resolution.Binding.Runtime {
	case agent.CodexRuntimeWeClaw:
		return nil
	case agent.CodexRuntimeConflict:
		return agent.ErrCodexRuntimeConflict
	default:
		return agent.ErrCodexRuntimeUnavailable
	}
}

func codexRuntimeReadyForRemoteTurn(runtime agent.CodexRuntimeHolder) bool {
	return runtime == agent.CodexRuntimeWeClaw
}

func codexResolutionModelStatus(resolution codexRuntimeResolution, fallback sessionModelStatus) sessionModelStatus {
	state := resolution.Binding.State
	if strings.TrimSpace(state.Model) == "" && strings.TrimSpace(state.Effort) == "" {
		return fallback
	}
	return sessionModelStatus{Model: state.Model, Effort: state.Effort}
}
