package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const codexRolloutPollInterval = 200 * time.Millisecond

// watchCodexRolloutTask 从初始 EOF 偏移增量跟踪指定 turn，避免重复读取历史记录。
func watchCodexRolloutTask(ctx context.Context, state codexRolloutTaskState, onProgress func(string)) (string, error) {
	if !state.Active || strings.TrimSpace(state.TurnID) == "" {
		return "", fmt.Errorf("Codex rollout 当前没有运行中的任务")
	}
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	offset := state.Offset
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			result, next, err := readCodexRolloutTaskDelta(state, offset, onProgress)
			if err != nil {
				return "", err
			}
			offset = next
			if result.done {
				return result.finalText()
			}
		}
	}
}

type codexRolloutWatchResult struct {
	final   string
	reason  string
	done    bool
	aborted bool
}

// finalText 将 rollout 终态转换为对外结果或明确的中断错误。
func (r codexRolloutWatchResult) finalText() (string, error) {
	if r.aborted {
		return "", fmt.Errorf("Codex App 本地任务已中断: %s", firstNonBlank(r.reason, "interrupted"))
	}
	return firstNonBlank(r.final, "Codex App 本地任务已完成，但没有返回文本。"), nil
}

// readCodexRolloutTaskDelta 读取本轮新增事件，并且只接受目标 turn 的终态。
func readCodexRolloutTaskDelta(state codexRolloutTaskState, offset int64, onProgress func(string)) (codexRolloutWatchResult, int64, error) {
	result := codexRolloutWatchResult{}
	next, err := readCodexRolloutEvents(state.Path, offset, func(event codexRolloutEvent) error {
		switch event.Kind {
		case codexRolloutProgress:
			if onProgress != nil && strings.TrimSpace(event.Text) != "" {
				onProgress(event.Text)
			}
		case codexRolloutTaskDone:
			if event.TurnID == state.TurnID {
				result = codexRolloutWatchResult{final: event.Text, done: true}
			}
		case codexRolloutTurnAborted:
			if event.TurnID == state.TurnID {
				result = codexRolloutWatchResult{reason: event.Reason, done: true, aborted: true}
			}
		case codexRolloutTaskStarted:
			if event.TurnID != "" && event.TurnID != state.TurnID && !result.done {
				return fmt.Errorf("Codex rollout 在当前任务结束前切换到新 turn")
			}
		}
		return nil
	})
	return result, next, err
}
