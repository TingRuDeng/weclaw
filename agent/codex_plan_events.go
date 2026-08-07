package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/observability"
)

type codexTurnPlanUpdatedParams struct {
	ThreadID string              `json:"threadId"`
	TurnID   string              `json:"turnId"`
	Plan     []codexTurnPlanStep `json:"plan"`
}

type codexTurnPlanStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

func (a *ACPAgent) handleCodexTurnPlanUpdatedAt(params json.RawMessage, sequence uint64) {
	var update codexTurnPlanUpdatedParams
	if json.Unmarshal(params, &update) != nil {
		return
	}
	event, ok := codexPlanProgressEvent(update, sequence)
	if !ok {
		return
	}
	a.dispatchToTurnCh(update.ThreadID, &codexTurnEvent{
		Kind: "progress", TurnID: update.TurnID, Sequence: sequence, Progress: &event,
	})
}

func codexPlanProgressEvent(update codexTurnPlanUpdatedParams, sequence uint64) (ProgressEvent, bool) {
	index := currentCodexPlanStep(update.Plan)
	if index < 0 {
		return ProgressEvent{}, false
	}
	step := update.Plan[index]
	text := observability.SanitizeText(step.Step)
	if text == "" {
		return ProgressEvent{}, false
	}
	planID := firstNonEmpty(strings.TrimSpace(update.TurnID), strings.TrimSpace(update.ThreadID))
	if planID == "" {
		return ProgressEvent{}, false
	}
	return ProgressEvent{
		ID: fmt.Sprintf("plan:%s:%d", planID, index+1), Kind: ProgressKindPlan,
		State: codexProgressState(step.Status, false), Sequence: sequence,
		Summary: text, Text: text,
	}, true
}

// currentCodexPlanStep 每次只投影一个语义步骤，避免同一 wire sequence 被拆成多条后
// 破坏消息层的全局顺序水位。新步骤会让上一条运行计划在时间线中原位收敛。
func currentCodexPlanStep(steps []codexTurnPlanStep) int {
	for index, step := range steps {
		if strings.EqualFold(strings.TrimSpace(step.Status), "inProgress") {
			return index
		}
	}
	for index, step := range steps {
		if strings.EqualFold(strings.TrimSpace(step.Status), "pending") {
			return index
		}
	}
	for index := len(steps) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(steps[index].Status), "completed") {
			return index
		}
	}
	return -1
}
