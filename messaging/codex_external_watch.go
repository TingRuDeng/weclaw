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
			result, done := h.watchRolloutOrReconnect(ctx, req, state)
			if done {
				return result
			}
		}
		if result, reconnected := h.watchReconnectedCodexDesktop(ctx, req); reconnected {
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

// watchRolloutOrReconnect 竞争 rollout 终态与重新探测到的 Desktop 连接。
func (h *Handler) watchRolloutOrReconnect(ctx context.Context, req externalCodexWatchRequest, state codexRolloutTaskState) (codexExternalWatchResult, bool) {
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
			if _, reconnected, probeErr := h.currentCodexDesktopBinding(ctx, req); reconnected || isCodexRuntimeConflict(probeErr) {
				cancel()
				rolloutResult := <-resultCh
				if rolloutResult.Terminal {
					return rolloutResult, true
				}
				if probeErr != nil {
					return failedCodexRuntimeWatch(probeErr), true
				}
				result, _ := h.watchReconnectedCodexDesktop(ctx, req)
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
		Final:    firstNonBlank(state.Final, "Codex App 本地任务已完成，但没有返回文本。"),
		Terminal: true, ConfirmedTerminal: true, Source: "rollout",
	}
}

// watchReconnectedCodexDesktop 在重新探测确认后接续 Desktop 观察流。
func (h *Handler) watchReconnectedCodexDesktop(ctx context.Context, req externalCodexWatchRequest) (codexExternalWatchResult, bool) {
	binding, reconnected, probeErr := h.currentCodexDesktopBinding(ctx, req)
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
	text, err := runtimeAgent.WatchCodexThread(ctx, req.conversationID, req.threadID, req.onProgress)
	return classifyCodexWatchResult(text, err, "desktop"), true
}

// currentCodexDesktopBinding 每次重新探测 Desktop，避免把旧连接缓存当成重连事实。
func (h *Handler) currentCodexDesktopBinding(ctx context.Context, req externalCodexWatchRequest) (agent.CodexThreadBinding, bool, error) {
	liveAgent, ok := req.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return agent.CodexThreadBinding{}, false, nil
	}
	unlock := h.lockCodexThreadControl(req.threadID)
	defer unlock()
	intent := h.ensureCodexSessions().controlIntent(req.threadID)
	request := agent.CodexRuntimeRequest{
		Ref:    agent.CodexThreadRef{ConversationID: req.conversationID, ThreadID: req.threadID},
		Intent: agentControlIntent(intent),
	}
	binding, err := liveAgent.InspectCodexRuntime(ctx, request)
	return binding, err == nil && binding.Runtime == agent.CodexRuntimeDesktop, err
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
