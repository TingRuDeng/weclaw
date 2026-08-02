package agent

import "strings"

const (
	codexProgressPrefix       = "进展："
	codexTurnDiagnosticsLimit = 5
)

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

// codexNativeMessageProgressEvent 原样转发 Codex 已完成的用户可见 agentMessage。
// 只去除消息整体首尾空白，正文中的 Markdown 和换行保持不变。
func codexNativeMessageProgressEvent(evt *codexTurnEvent) (ProgressEvent, bool) {
	if evt == nil || evt.Kind != "item_completed" {
		return ProgressEvent{}, false
	}
	text := strings.TrimSpace(evt.Text)
	if text == "" {
		return ProgressEvent{}, false
	}
	id := strings.TrimSpace(evt.ItemID)
	if id != "" {
		id = "agent-message:" + id
	}
	return ProgressEvent{
		ID: id, Kind: ProgressKindMessage, State: ProgressStateCompleted,
		Sequence: evt.Sequence, Text: text,
	}, true
}
