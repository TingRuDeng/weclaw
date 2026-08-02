package agent

import (
	"encoding/json"
	"strings"
)

const (
	codexProgressPrefix       = "进展："
	codexTurnDiagnosticsLimit = 5
	codexPlanStepMaxRunes     = 120
	codexRealtimeLineMaxRunes = 240
	codexGeneratingProgress   = "进展：Codex 正在生成回复。"
)

type codexProgressParams struct {
	ThreadID string            `json:"threadId"`
	Message  string            `json:"message"`
	Status   string            `json:"status"`
	Changes  json.RawMessage   `json:"changes"`
	Command  permissionCommand `json:"command"`
	Path     string            `json:"path"`
	FilePath string            `json:"filePath"`
	Files    []string          `json:"files"`
	Paths    []string          `json:"paths"`
}

type codexPlanUpdatedParams struct {
	ThreadID string          `json:"threadId"`
	Plan     []codexPlanStep `json:"plan"`
}

type codexPlanStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

type codexTurnDiagnostics struct {
	max    int
	events []string
}

func newCodexTurnDiagnostics(max int) *codexTurnDiagnostics {
	if max <= 0 {
		max = codexTurnDiagnosticsLimit
	}
	return &codexTurnDiagnostics{max: max}
}

func (d *codexTurnDiagnostics) remember(event string) {
	event = strings.TrimSpace(event)
	if event == "" {
		return
	}
	d.events = append(d.events, event)
	if len(d.events) > d.max {
		d.events = d.events[len(d.events)-d.max:]
	}
}

func (d *codexTurnDiagnostics) withError(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(d.events) == 0 {
		return reason
	}
	var b strings.Builder
	b.WriteString(reason)
	b.WriteString("\n\n最近事件：")
	for _, event := range d.events {
		b.WriteString("\n- ")
		b.WriteString(strings.TrimPrefix(event, codexProgressPrefix))
	}
	return b.String()
}

func (a *ACPAgent) handleCodexAutoApprovalReviewStartedAt(params json.RawMessage, sequence uint64) {
	p := decodeCodexProgressParams(params)
	a.dispatchProgressEventToThread(p.ThreadID, "进展：安全检查", &codexProgressEvent{
		ID: "security-check", Kind: "approval", Action: "安全检查", Status: "running",
	}, sequence)
}

func (a *ACPAgent) handleCodexAutoApprovalReviewCompletedAt(params json.RawMessage, sequence uint64) {
	p := decodeCodexProgressParams(params)
	a.dispatchProgressEventToThread(p.ThreadID, "进展：安全检查", &codexProgressEvent{
		ID: "security-check", Kind: "approval", Action: "安全检查", Status: "completed",
	}, sequence)
}

func (a *ACPAgent) handleCodexGuardianWarningAt(params json.RawMessage, sequence uint64) {
	p := decodeCodexProgressParams(params)
	eventStatus := "running"
	if strings.Contains(strings.ToLower(p.Message), "approved") {
		eventStatus = "completed"
	}
	a.dispatchProgressEventToThread(p.ThreadID, "进展：安全检查", &codexProgressEvent{
		ID: "security-check", Kind: "approval", Action: "安全检查", Status: eventStatus,
	}, sequence)
}

func (a *ACPAgent) handleCodexCommandProgress(params json.RawMessage) {
	a.handleCodexCommandProgressAt(params, 0)
}

func (a *ACPAgent) handleCodexFileProgress(params json.RawMessage) {
	a.handleCodexFileProgressAt(params, 0)
}

func (a *ACPAgent) handleCodexCommandProgressAt(params json.RawMessage, sequence uint64) {
	a.dispatchCodexCommandLine(params, sequence)
}

func (a *ACPAgent) handleCodexFileProgressAt(params json.RawMessage, sequence uint64) {
	a.dispatchCodexFileLine(params, sequence)
}

// handleCodexPlanUpdated 把 Codex App 的计划状态转换成任务卡片可读的当前步骤。
func (a *ACPAgent) handleCodexPlanUpdated(params json.RawMessage) {
	a.handleCodexPlanUpdatedAt(params, 0)
}

func (a *ACPAgent) handleCodexPlanUpdatedAt(params json.RawMessage, sequence uint64) {
	var p codexPlanUpdatedParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	step := currentCodexPlanStep(p.Plan)
	if step == "" {
		return
	}
	status := currentCodexPlanStatus(p.Plan)
	a.dispatchProgressEventToThread(
		p.ThreadID,
		codexProgressPrefix+trimRunes(step, codexPlanStepMaxRunes),
		&codexProgressEvent{ID: "plan", Kind: "plan", Action: step, Status: status},
		sequence,
	)
}

func currentCodexPlanStatus(plan []codexPlanStep) string {
	for _, status := range []string{"in_progress", "completed", "pending"} {
		for _, item := range plan {
			if codexPlanStatusMatches(item.Status, status) && strings.TrimSpace(item.Step) != "" {
				return status
			}
		}
	}
	return ""
}

// currentCodexPlanStep 优先展示进行中步骤，缺失时回退到最近完成或即将开始的步骤。
func currentCodexPlanStep(plan []codexPlanStep) string {
	if step := firstPlanStepByStatus(plan, "in_progress"); step != "" {
		return step
	}
	if step := lastPlanStepByStatus(plan, "completed"); step != "" {
		return step
	}
	return firstPlanStepByStatus(plan, "pending")
}

// firstPlanStepByStatus 返回指定状态下最靠前的非空步骤。
func firstPlanStepByStatus(plan []codexPlanStep, status string) string {
	for _, item := range plan {
		if codexPlanStatusMatches(item.Status, status) && strings.TrimSpace(item.Step) != "" {
			return strings.TrimSpace(item.Step)
		}
	}
	return ""
}

// lastPlanStepByStatus 返回指定状态下最靠后的非空步骤。
func lastPlanStepByStatus(plan []codexPlanStep, status string) string {
	for i := len(plan) - 1; i >= 0; i-- {
		if codexPlanStatusMatches(plan[i].Status, status) && strings.TrimSpace(plan[i].Step) != "" {
			return strings.TrimSpace(plan[i].Step)
		}
	}
	return ""
}

func codexPlanStatusMatches(actual string, expected string) bool {
	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		return strings.NewReplacer("_", "", "-", "").Replace(value)
	}
	return normalize(actual) == normalize(expected)
}

func (a *ACPAgent) dispatchCodexCommandLine(params json.RawMessage, sequence uint64) {
	p := decodeCodexProgressParams(params)
	a.dispatchCodexCommandProgress(p, sequence)
}

func (a *ACPAgent) dispatchCodexFileLine(params json.RawMessage, sequence uint64) {
	p := decodeCodexProgressParams(params)
	a.dispatchCodexFileProgress(p, sequence)
}

func (a *ACPAgent) dispatchCodexFileProgress(p codexProgressParams, sequence uint64) {
	line := codexFileProgressLine(p)
	if line == "" {
		return
	}
	a.dispatchProgressEventToThread(p.ThreadID, line, codexFileProgressEvent(p, line), sequence)
}

func (a *ACPAgent) dispatchCodexCommandProgress(p codexProgressParams, sequence uint64) {
	stage, ok := codexCommandProgressStage(p.Command)
	if !ok {
		return
	}
	a.dispatchProgressEventToThread(p.ThreadID, stage.action, &codexProgressEvent{
		ID: "command:" + stage.id, Kind: "command", Action: stage.action, Status: p.Status,
	}, sequence)
}

func (a *ACPAgent) dispatchProgressEventToThread(threadID string, text string, progress *codexProgressEvent, sequence uint64) {
	a.dispatchToTurnCh(threadID, &codexTurnEvent{
		Kind:     "progress",
		Text:     trimRunes(text, codexRealtimeLineMaxRunes),
		Progress: progress,
		Sequence: sequence,
	})
}

func decodeCodexProgressParams(params json.RawMessage) codexProgressParams {
	var p codexProgressParams
	_ = json.Unmarshal(params, &p)
	return p
}

func trimRunes(text string, limit int) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}
