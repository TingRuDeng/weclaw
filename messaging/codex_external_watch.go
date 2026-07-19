package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexExternalWatchResult struct {
	Final             string
	Err               error
	Terminal          bool
	ConfirmedTerminal bool
	Failed            bool
	Source            string
}

type externalCodexWatchRequest struct {
	agent          agent.Agent
	routeUserID    string
	agentName      string
	conversationID string
	threadID       string
	turnID         string
	task           *activeAgentTask
	onProgress     func(agent.ProgressEvent)
}

// superviseExternalCodexWatch 把当前客户端断线切换为 rollout/reconnect 观察。
func (h *Handler) superviseExternalCodexWatch(runtime externalCodexTaskRuntime, onProgress func(agent.ProgressEvent)) codexExternalWatchResult {
	text, err := runtime.watch(runtime.ctx, onProgress)
	source := "runtime"
	if !runtime.state.Controllable {
		source = "rollout"
	}
	result := classifyCodexWatchResult(text, err, source)
	if result.Terminal {
		return result
	}
	runtime.task.markCodexDisconnected()
	return h.watchCodexAfterRuntimeDisconnect(runtime.ctx, externalCodexWatchRequest{
		agent: runtime.opts.agent, routeUserID: runtime.opts.routeUserID,
		agentName: runtime.opts.agentName, conversationID: runtime.opts.conversationID,
		threadID: runtime.opts.threadID, turnID: runtime.state.ActiveTurnID,
		task: runtime.task, onProgress: onProgress,
	})
}

// watchCodexAfterRuntimeDisconnect 从最新 rollout 尾部或重连共享 app-server 接续观察。
func (h *Handler) watchCodexAfterRuntimeDisconnect(ctx context.Context, req externalCodexWatchRequest) codexExternalWatchResult {
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	for {
		state, found, err := h.bootstrapCodexRolloutAfterDisconnect(req.threadID, req.turnID)
		if err != nil {
			return classifyCodexWatchResult("", err, "rollout")
		}
		if found && !state.Active {
			return terminalCodexRolloutState(state)
		}
		if found {
			if state.Progress != "" && req.onProgress != nil {
				req.onProgress(agent.TextProgressEvent(state.Progress))
			}
			result, done := h.watchRolloutOrReconnect(ctx, req, state)
			if done {
				return result
			}
		}
		if result, reconnected := h.watchReconnectedCodexRuntime(ctx, req); reconnected {
			if result.Terminal {
				return result
			}
			if req.task != nil {
				req.task.markCodexDisconnected()
			}
		}
		select {
		case <-ctx.Done():
			return codexExternalWatchResult{Err: ctx.Err(), Source: "supervisor"}
		case <-ticker.C:
		}
	}
}

// watchRolloutOrReconnect 竞争 rollout 终态与重新连上的共享 app-server 客户端。
func (h *Handler) watchRolloutOrReconnect(ctx context.Context, req externalCodexWatchRequest, state codexRolloutTaskState) (codexExternalWatchResult, bool) {
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	resultCh := make(chan codexExternalWatchResult, 1)
	go func() {
		text, err := watchCodexRolloutTask(watchCtx, state, textProgressCallback(req.onProgress))
		resultCh <- classifyCodexWatchResult(text, err, "rollout")
	}()
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			return result, result.Terminal
		case <-ticker.C:
			if _, reconnected, probeErr := h.currentCodexSharedHostBinding(ctx, req); reconnected || isCodexRuntimeConflict(probeErr) {
				cancel()
				rolloutResult := <-resultCh
				if rolloutResult.Terminal {
					return rolloutResult, true
				}
				if probeErr != nil {
					return failedCodexRuntimeWatch(probeErr), true
				}
				result, _ := h.watchReconnectedCodexRuntime(ctx, req)
				return result, result.Terminal
			}
		case <-ctx.Done():
			return codexExternalWatchResult{Err: ctx.Err(), Source: "supervisor"}, false
		}
	}
}

func (h *Handler) bootstrapCodexRolloutAfterDisconnect(threadID string, turnID string) (codexRolloutTaskState, bool, error) {
	state, found, err := h.readLocalCodexRolloutTaskState(threadID)
	if err != nil || !found {
		return state, found, err
	}
	targetTurnID := strings.TrimSpace(turnID)
	if state.TurnID == "" || (targetTurnID != "" && state.TurnID != targetTurnID) {
		return codexRolloutTaskState{}, false, fmt.Errorf("%w: %s", errCodexRolloutTurnChanged, state.TurnID)
	}
	return state, true, nil
}

func terminalCodexRolloutState(state codexRolloutTaskState) codexExternalWatchResult {
	if state.Aborted {
		err := errors.New(firstNonBlank(state.Reason, "Codex rollout 任务已中断"))
		return codexExternalWatchResult{Err: err, Terminal: true, ConfirmedTerminal: true, Failed: true, Source: "rollout"}
	}
	return codexExternalWatchResult{
		Final:    firstNonBlank(state.Final, "共享 Codex 任务已完成，但没有返回文本。"),
		Terminal: true, ConfirmedTerminal: true, Source: "rollout",
	}
}

// watchReconnectedCodexRuntime reconnects this frontend to the shared app-server.
func (h *Handler) watchReconnectedCodexRuntime(ctx context.Context, req externalCodexWatchRequest) (codexExternalWatchResult, bool) {
	binding, reconnected, probeErr := h.currentCodexSharedHostBinding(ctx, req)
	if isCodexRuntimeConflict(probeErr) {
		return failedCodexRuntimeWatch(probeErr), true
	}
	runtimeAgent, runtimeOK := req.agent.(agent.CodexThreadRuntimeAgent)
	if !reconnected || !runtimeOK {
		return codexExternalWatchResult{}, false
	}
	if req.task != nil {
		req.task.markCodexRunning(binding)
	}
	var text string
	var err error
	if structured, ok := req.agent.(agent.CodexStructuredThreadRuntimeAgent); ok {
		text, err = structured.WatchCodexThreadEvents(ctx, req.conversationID, req.threadID, req.onProgress)
	} else {
		text, err = runtimeAgent.WatchCodexThread(ctx, req.conversationID, req.threadID, textProgressCallback(req.onProgress))
	}
	return classifyCodexWatchResult(text, err, "runtime"), true
}

// currentCodexSharedHostBinding refreshes authoritative shared-host state instead
// of inferring availability from a stale frontend cache.
func (h *Handler) currentCodexSharedHostBinding(ctx context.Context, req externalCodexWatchRequest) (agent.CodexThreadBinding, bool, error) {
	liveAgent, ok := req.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return agent.CodexThreadBinding{}, false, nil
	}
	unlock := h.lockCodexThreadControl(req.threadID)
	defer unlock()
	route := codexConversationRoute{
		bindingKey:     codexBindingKey(req.routeUserID, req.agentName),
		conversationID: req.conversationID,
	}
	request := agent.CodexRuntimeRequest{
		Ref:    agent.CodexThreadRef{ConversationID: req.conversationID, ThreadID: req.threadID},
		Intent: codexSharedHostIntent(route),
	}
	binding, err := liveAgent.InspectCodexRuntime(ctx, request)
	return binding, err == nil && binding.Runtime == agent.CodexRuntimeWeClaw, err
}

// isCodexRuntimeConflict 只把显式双写冲突视为观察终态。
func isCodexRuntimeConflict(err error) bool {
	return errors.Is(err, agent.ErrCodexRuntimeConflict)
}

// failedCodexRuntimeWatch 把运行时冲突转换为可回推的失败终态。
func failedCodexRuntimeWatch(err error) codexExternalWatchResult {
	return codexExternalWatchResult{Err: err, Terminal: true, Failed: true, Source: "runtime"}
}

func classifyCodexWatchResult(text string, err error, source string) codexExternalWatchResult {
	if err == nil {
		return codexExternalWatchResult{Final: text, Terminal: true, ConfirmedTerminal: true, Source: source}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, agent.ErrCodexDesktopDisconnected) || errors.Is(err, agent.ErrCodexDesktopOwnershipUnknown) {
		return codexExternalWatchResult{Err: err, Source: source}
	}
	if errors.Is(err, errCodexRolloutAborted) || errors.Is(err, agent.ErrCodexTurnTerminal) {
		return codexExternalWatchResult{Err: err, Terminal: true, ConfirmedTerminal: true, Failed: true, Source: source}
	}
	if source == "rollout" {
		return codexExternalWatchResult{Err: err, Terminal: true, Failed: true, Source: source}
	}
	return codexExternalWatchResult{Err: err, Source: source}
}
