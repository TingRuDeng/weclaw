package agent

import (
	"encoding/json"
	"strings"
)

const (
	codexProgressPrefix          = "进展："
	codexTurnDiagnosticsLimit    = 5
	codexGuardianWarningMaxRunes = 120
	codexPlanStepMaxRunes        = 120
	codexRealtimeLineMaxRunes    = 240
	codexGeneratingProgress      = "进展：Codex 正在生成回复。"
)

type codexProgressParams struct {
	ThreadID string            `json:"threadId"`
	Message  string            `json:"message"`
	Status   string            `json:"status"`
	Decision string            `json:"decision"`
	Outcome  string            `json:"outcome"`
	Delta    string            `json:"delta"`
	Output   string            `json:"output"`
	Text     string            `json:"text"`
	Diff     string            `json:"diff"`
	Changes  json.RawMessage   `json:"changes"`
	Command  permissionCommand `json:"command"`
	Cwd      string            `json:"cwd"`
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

func (a *ACPAgent) handleCodexAutoApprovalReviewStarted(params json.RawMessage) {
	a.dispatchCodexProgress(params, "进展：Codex 自动审批审核中。")
}

func (a *ACPAgent) handleCodexAutoApprovalReviewCompleted(params json.RawMessage) {
	status := "进展：Codex 自动审批审核已完成。"
	if isPositiveCodexReview(params) {
		status = "进展：Codex 自动审批已通过。"
	}
	a.dispatchCodexProgress(params, status)
}

func (a *ACPAgent) handleCodexGuardianWarning(params json.RawMessage) {
	p := decodeCodexProgressParams(params)
	status := "进展：Codex 收到安全提示。"
	if strings.Contains(strings.ToLower(p.Message), "approved") {
		status = "进展：Codex 自动审批已通过。"
	} else if strings.TrimSpace(p.Message) != "" {
		status = "进展：Codex 收到安全提示：" + trimRunes(p.Message, codexGuardianWarningMaxRunes)
	}
	a.dispatchProgressToThread(p.ThreadID, status)
}

func (a *ACPAgent) handleCodexCommandProgress(params json.RawMessage) {
	a.dispatchCodexCommandLine(params)
}

func (a *ACPAgent) handleCodexFileProgress(params json.RawMessage) {
	a.dispatchCodexFileLine(params)
}

// handleCodexPlanUpdated 把 Codex App 的计划状态转换成任务卡片可读的当前步骤。
func (a *ACPAgent) handleCodexPlanUpdated(params json.RawMessage) {
	var p codexPlanUpdatedParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	step := currentCodexPlanStep(p.Plan)
	if step == "" {
		return
	}
	a.dispatchProgressToThread(p.ThreadID, codexProgressPrefix+trimRunes(step, codexPlanStepMaxRunes))
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

func (a *ACPAgent) dispatchCodexProgress(params json.RawMessage, text string) {
	p := decodeCodexProgressParams(params)
	a.dispatchProgressToThread(p.ThreadID, text)
}

func (a *ACPAgent) dispatchCodexCommandLine(params json.RawMessage) {
	p := decodeCodexProgressParams(params)
	a.dispatchCodexCommandProgress(p)
}

func (a *ACPAgent) dispatchCodexFileLine(params json.RawMessage) {
	p := decodeCodexProgressParams(params)
	a.dispatchCodexFileProgress(p)
}

func (a *ACPAgent) dispatchCodexFileProgress(p codexProgressParams) {
	line := codexFileProgressLine(p)
	if line == "" {
		return
	}
	a.dispatchProgressEventToThread(p.ThreadID, line, codexFileProgressEvent(p, line))
}

func (a *ACPAgent) dispatchCodexCommandProgress(p codexProgressParams) {
	line := codexCommandProgressLine(p)
	if line == "" {
		return
	}
	a.dispatchProgressEventToThread(p.ThreadID, line, codexCommandProgressEvent(p, line))
}

func (a *ACPAgent) dispatchProgressToThread(threadID string, text string) {
	a.dispatchToTurnCh(threadID, &codexTurnEvent{Kind: "progress", Text: text})
}

func (a *ACPAgent) dispatchProgressEventToThread(threadID string, text string, progress *codexProgressEvent) {
	a.dispatchToTurnCh(threadID, &codexTurnEvent{
		Kind:     "progress",
		Text:     trimRunes(text, codexRealtimeLineMaxRunes),
		Progress: progress,
	})
}

func decodeCodexProgressParams(params json.RawMessage) codexProgressParams {
	var p codexProgressParams
	_ = json.Unmarshal(params, &p)
	return p
}

func isPositiveCodexReview(params json.RawMessage) bool {
	p := decodeCodexProgressParams(params)
	text := strings.ToLower(strings.Join([]string{p.Status, p.Decision, p.Outcome, p.Message}, " "))
	return strings.Contains(text, "approved") || strings.Contains(text, "accept") || strings.Contains(text, "allow")
}

// latestCodexRealtimeLine 从 Codex App 的原始事件中取最后一条可读输出。
func latestCodexRealtimeLine(p codexProgressParams) string {
	for _, value := range []string{p.Delta, p.Output, p.Text, p.Message, p.Status, p.Diff} {
		if line := lastNonEmptyCodexLine(value); line != "" {
			return line
		}
	}
	return ""
}

// codexCommandProgressLine 把命令进度转换成 Codex App 风格的可读行。
func codexCommandProgressLine(p codexProgressParams) string {
	if command := strings.TrimSpace(strings.Join(p.Command, " ")); command != "" {
		return "运行 " + command
	}
	return latestCodexRealtimeLine(p)
}

// codexCommandProgressEvent 保留命令主动作，并把最新输出作为次要详情交给 turn 聚合器。
func codexCommandProgressEvent(p codexProgressParams, line string) *codexProgressEvent {
	event := &codexProgressEvent{Kind: "command"}
	if command := strings.TrimSpace(strings.Join(p.Command, " ")); command != "" {
		event.Action = "运行 " + command
		event.Detail = latestCodexRealtimeLine(p)
		return event
	}
	event.Detail = line
	return event
}

func lastNonEmptyCodexLine(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
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
