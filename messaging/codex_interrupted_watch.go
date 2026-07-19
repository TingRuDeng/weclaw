package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

// reconcileInterruptedCodexTurn 仅通过原 thread 和 turn 的 rollout 核对待确认中断。
func (h *Handler) reconcileInterruptedCodexTurn(ctx context.Context, interrupted *agent.CodexTurnInterruptedError, onProgress func(agent.ProgressEvent)) codexExternalWatchResult {
	if interrupted == nil || strings.TrimSpace(interrupted.ThreadID) == "" || strings.TrimSpace(interrupted.TurnID) == "" {
		err := errors.New("Codex 中断事件缺少 thread 或 turn，无法确认任务终态")
		return codexExternalWatchResult{Err: err, Terminal: true, Failed: true, Source: "app_server"}
	}
	if onProgress != nil {
		onProgress(agent.TextProgressEvent("Codex 连接发生切换，正在继续跟踪当前任务。"))
	}
	log.Printf("[codex-watch] reconciling interrupted turn (thread=%s, turn=%s, source=app_server)", interrupted.ThreadID, interrupted.TurnID)
	result := h.watchInterruptedCodexRollout(ctx, interrupted, onProgress)
	log.Printf("[codex-watch] interrupted turn resolved (thread=%s, turn=%s, source=%s, terminal=%t, failed=%t)", interrupted.ThreadID, interrupted.TurnID, result.Source, result.Terminal, result.Failed)
	return result
}

// watchInterruptedCodexRollout 等待目标 turn 出现后，从当前文件尾持续读取其终态。
func (h *Handler) watchInterruptedCodexRollout(ctx context.Context, interrupted *agent.CodexTurnInterruptedError, onProgress func(agent.ProgressEvent)) codexExternalWatchResult {
	state, err := h.awaitInterruptedCodexTurn(ctx, interrupted.ThreadID, interrupted.TurnID)
	if err != nil {
		return classifyCodexWatchResult("", err, "rollout")
	}
	if !state.Active {
		return terminalCodexRolloutState(state)
	}
	text, err := watchCodexRolloutTask(ctx, state, textProgressCallback(onProgress))
	return classifyCodexWatchResult(text, err, "rollout")
}

// awaitInterruptedCodexTurn 首次扫描一次历史文件，后续只增量等待目标 turn。
func (h *Handler) awaitInterruptedCodexTurn(ctx context.Context, threadID string, turnID string) (codexRolloutTaskState, error) {
	path, err := h.waitCodexRolloutPath(ctx, threadID)
	if err != nil {
		return codexRolloutTaskState{}, err
	}
	state, found, err := readCodexRolloutTaskStateForTurn(path, turnID)
	if err != nil || found {
		return state, err
	}
	return waitCodexRolloutTurnStart(ctx, state, turnID)
}

// waitCodexRolloutPath 等待共享 session 文件出现，由任务 context 控制最长时间。
func (h *Handler) waitCodexRolloutPath(ctx context.Context, threadID string) (string, error) {
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	for {
		h.mu.RLock()
		dir := h.codexLocalSessionDir
		h.mu.RUnlock()
		path, found, err := findLocalCodexRolloutPath(dir, threadID)
		if err != nil || found {
			return path, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// waitCodexRolloutTurnStart 从已知 EOF 增量等待目标任务，其他新 turn 视为替换。
func waitCodexRolloutTurnStart(ctx context.Context, state codexRolloutTaskState, turnID string) (codexRolloutTaskState, error) {
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	for {
		found := false
		next, err := readCodexRolloutEvents(state.Path, state.Offset, func(event codexRolloutEvent) error {
			if !found {
				if event.Kind != codexRolloutTaskStarted {
					return nil
				}
				if event.TurnID != turnID {
					return fmt.Errorf("%w: %s", errCodexRolloutTurnChanged, event.TurnID)
				}
				state = codexRolloutTaskState{Path: state.Path, TurnID: turnID, Active: true}
				found = true
			}
			if event.Kind == codexRolloutTaskStarted && event.TurnID != turnID && state.Active {
				return fmt.Errorf("%w: %s", errCodexRolloutTurnChanged, event.TurnID)
			}
			if event.Kind != codexRolloutTaskStarted || event.TurnID == turnID {
				applyCodexRolloutEvent(&state, event)
			}
			return nil
		})
		state.Offset = next
		if err != nil || found {
			return state, err
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-ticker.C:
		}
	}
}
