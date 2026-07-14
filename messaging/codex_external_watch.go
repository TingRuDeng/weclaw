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
	Final    string
	Err      error
	Terminal bool
	Failed   bool
	Source   string
}

type externalCodexWatchRequest struct {
	agent          agent.Agent
	conversationID string
	threadID       string
	turnID         string
	task           *activeAgentTask
	onProgress     func(string)
}

// superviseExternalCodexWatch 把 Desktop 断线切换为 rollout/reconnect 观察。
func (h *Handler) superviseExternalCodexWatch(runtime externalCodexTaskRuntime, onProgress func(string)) codexExternalWatchResult {
	text, err := runtime.watch(runtime.ctx, onProgress)
	source := "desktop"
	if !runtime.state.Controllable {
		source = "rollout"
	}
	result := classifyCodexWatchResult(text, err, source)
	if result.Terminal {
		return result
	}
	runtime.task.markCodexDisconnected()
	return h.watchCodexAfterDesktopDisconnect(runtime.ctx, externalCodexWatchRequest{
		agent: runtime.opts.agent, conversationID: runtime.opts.conversationID,
		threadID: runtime.opts.threadID, turnID: runtime.state.ActiveTurnID,
		task: runtime.task, onProgress: onProgress,
	})
}

// watchCodexAfterDesktopDisconnect 从最新 rollout 尾部或重连 Desktop 接续观察。
func (h *Handler) watchCodexAfterDesktopDisconnect(ctx context.Context, req externalCodexWatchRequest) codexExternalWatchResult {
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
				req.onProgress(state.Progress)
			}
			result, done := watchRolloutOrReconnect(ctx, req, state)
			if done {
				return result
			}
		}
		if result, reconnected := watchReconnectedCodexDesktop(ctx, req); reconnected {
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

func watchRolloutOrReconnect(ctx context.Context, req externalCodexWatchRequest, state codexRolloutTaskState) (codexExternalWatchResult, bool) {
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	resultCh := make(chan codexExternalWatchResult, 1)
	go func() {
		text, err := watchCodexRolloutTask(watchCtx, state, req.onProgress)
		resultCh <- classifyCodexWatchResult(text, err, "rollout")
	}()
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			return result, result.Terminal
		case <-ticker.C:
			if _, reconnected := currentCodexDesktopBinding(req); reconnected {
				cancel()
				rolloutResult := <-resultCh
				if rolloutResult.Terminal {
					return rolloutResult, true
				}
				result, _ := watchReconnectedCodexDesktop(ctx, req)
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
		return codexExternalWatchResult{Err: err, Terminal: true, Failed: true, Source: "rollout"}
	}
	return codexExternalWatchResult{Final: firstNonBlank(state.Final, "Codex App 本地任务已完成，但没有返回文本。"), Terminal: true, Source: "rollout"}
}

func watchReconnectedCodexDesktop(ctx context.Context, req externalCodexWatchRequest) (codexExternalWatchResult, bool) {
	binding, reconnected := currentCodexDesktopBinding(req)
	runtimeAgent, runtimeOK := req.agent.(agent.CodexThreadRuntimeAgent)
	if !reconnected || !runtimeOK {
		return codexExternalWatchResult{}, false
	}
	if req.task != nil {
		req.task.markCodexRunning(binding)
	}
	text, err := runtimeAgent.WatchCodexThread(ctx, req.conversationID, req.threadID, req.onProgress)
	return classifyCodexWatchResult(text, err, "desktop"), true
}

func currentCodexDesktopBinding(req externalCodexWatchRequest) (agent.CodexThreadBinding, bool) {
	liveAgent, ok := req.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return agent.CodexThreadBinding{}, false
	}
	binding, found := liveAgent.CurrentCodexThreadBinding(req.conversationID)
	return binding, found && binding.Owner == agent.CodexOwnerDesktopLive
}

func classifyCodexWatchResult(text string, err error, source string) codexExternalWatchResult {
	if err == nil {
		return codexExternalWatchResult{Final: text, Terminal: true, Source: source}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, agent.ErrCodexDesktopDisconnected) || errors.Is(err, agent.ErrCodexDesktopOwnershipUnknown) {
		return codexExternalWatchResult{Err: err, Source: source}
	}
	if source == "rollout" {
		return codexExternalWatchResult{Err: err, Terminal: true, Failed: true, Source: source}
	}
	if errors.Is(err, errCodexRolloutAborted) || errors.Is(err, agent.ErrCodexTurnTerminal) {
		return codexExternalWatchResult{Err: err, Terminal: true, Failed: true, Source: source}
	}
	return codexExternalWatchResult{Err: err, Source: source}
}
