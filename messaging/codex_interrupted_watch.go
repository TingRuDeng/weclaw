package messaging

import (
	"context"
	"errors"
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

// awaitInterruptedCodexTurn 扫描同一 root thread 的全部 lineage rollout，
// 直到精确目标 turn 出现或调用方取消。
func (h *Handler) awaitInterruptedCodexTurn(ctx context.Context, threadID string, turnID string) (codexRolloutTaskState, error) {
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	for {
		h.mu.RLock()
		dir := h.codexLocalSessionDir
		h.mu.RUnlock()
		paths, err := findLocalCodexRolloutPaths(dir, threadID)
		if err != nil {
			return codexRolloutTaskState{}, err
		}
		for _, path := range paths {
			state, found, readErr := readCodexRolloutTaskStateForTurn(path, turnID)
			if readErr != nil {
				return state, readErr
			}
			if found {
				return state, nil
			}
		}
		select {
		case <-ctx.Done():
			return codexRolloutTaskState{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
