package agent

import (
	"encoding/json"
	"strings"
)

const (
	codexProgressPrefix          = "进展："
	codexTurnDiagnosticsLimit    = 5
	codexGuardianWarningMaxRunes = 120
)

type codexProgressParams struct {
	ThreadID string `json:"threadId"`
	Message  string `json:"message"`
	Status   string `json:"status"`
	Decision string `json:"decision"`
	Outcome  string `json:"outcome"`
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
	a.dispatchCodexProgress(params, "进展：Codex 正在执行命令并产生输出。")
}

func (a *ACPAgent) handleCodexFileProgress(params json.RawMessage) {
	a.dispatchCodexProgress(params, "进展：Codex 已产生代码或文件变更。")
}

func (a *ACPAgent) dispatchCodexProgress(params json.RawMessage, text string) {
	p := decodeCodexProgressParams(params)
	a.dispatchProgressToThread(p.ThreadID, text)
}

func (a *ACPAgent) dispatchProgressToThread(threadID string, text string) {
	a.dispatchToTurnCh(threadID, &codexTurnEvent{Kind: "progress", Text: text})
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
