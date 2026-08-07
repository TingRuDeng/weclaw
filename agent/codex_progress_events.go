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

// codexNativeMessageProgressEvent 立即转发 Codex 明确标注为 commentary 的已完成消息。
// final_answer 始终不进入进度卡；无阶段消息由下方延迟判定器处理。
func codexNativeMessageProgressEvent(evt *codexTurnEvent) (ProgressEvent, bool) {
	if evt == nil || evt.Kind != "item_completed" || evt.MessagePhase != "commentary" {
		return ProgressEvent{}, false
	}
	return codexCompletedMessageProgressEvent(evt)
}

func codexCompletedMessageProgressEvent(evt *codexTurnEvent) (ProgressEvent, bool) {
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
		ID: id, Kind: ProgressKindCommentary, State: ProgressStateCompleted,
		Sequence: evt.Sequence, Text: text,
	}, true
}

// codexMessageProgressBuffer 延迟一条无 phase 的已完成消息。后续仍有执行事件时，
// 上一条才可确认为中间说明；正常 turn/completed 前的最后一条仍作为最终回答。
type codexMessageProgressBuffer struct {
	pending *codexTurnEvent
}

func (b *codexMessageProgressBuffer) beforeEvent(evt *codexTurnEvent, callbacks progressCallbacks) {
	if b == nil || b.pending == nil || evt == nil {
		return
	}
	if evt.Kind == "completed" {
		b.discard()
		return
	}
	if evt.Kind == "item_completed" && evt.MessagePhase == "" && sameCodexMessageItem(b.pending, evt) {
		return
	}
	b.flush(callbacks)
}

func (b *codexMessageProgressBuffer) observeCompleted(evt *codexTurnEvent, callbacks progressCallbacks) {
	if b == nil || evt == nil || evt.Kind != "item_completed" {
		return
	}
	if event, ok := codexNativeMessageProgressEvent(evt); ok {
		callbacks.emit(event)
		return
	}
	if evt.MessagePhase != "" || strings.TrimSpace(evt.Text) == "" {
		return
	}
	copyEvent := *evt
	b.pending = &copyEvent
}

func (b *codexMessageProgressBuffer) flush(callbacks progressCallbacks) {
	if b == nil || b.pending == nil {
		return
	}
	pending := b.pending
	b.pending = nil
	if event, ok := codexCompletedMessageProgressEvent(pending); ok {
		callbacks.emit(event)
	}
}

func (b *codexMessageProgressBuffer) discard() {
	if b != nil {
		b.pending = nil
	}
}

func sameCodexMessageItem(left *codexTurnEvent, right *codexTurnEvent) bool {
	if left == nil || right == nil {
		return false
	}
	leftID := strings.TrimSpace(left.ItemID)
	return leftID != "" && leftID == strings.TrimSpace(right.ItemID)
}
