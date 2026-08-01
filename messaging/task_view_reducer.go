package messaging

import (
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

type taskViewEventKind uint8

const (
	taskViewProgress taskViewEventKind = iota + 1
	taskViewClosed
	taskViewTerminal
)

type taskViewEvent struct {
	kind                  taskViewEventKind
	at                    time.Time
	progress              agent.ProgressEvent
	allowLocalUnsequenced bool
	terminalState         string
}

// taskViewState 是任务卡和 /ps 的唯一进程内展示快照。
type taskViewState struct {
	lastProgress            string
	lastProgressEvent       agent.ProgressEvent
	lastProgressAt          time.Time
	lastProgressSourceSeq   uint64
	progressTimeline        []agent.ProgressEvent
	progressTimelineEnabled bool
	revision                uint64
	closed                  bool
	terminalState           string
	terminalAt              time.Time
}

// reduceTaskView 是无副作用 reducer；旧 sequence 和终态后的进展在此统一拒绝。
func reduceTaskView(current taskViewState, event taskViewEvent) (taskViewState, bool) {
	next := current
	switch event.kind {
	case taskViewProgress:
		display := strings.TrimSpace(event.progress.DisplayText())
		if display == "" || current.closed {
			return current, false
		}
		sequence := event.progress.Sequence
		if sequence == 0 && current.lastProgressSourceSeq > 0 && !event.allowLocalUnsequenced {
			return current, false
		}
		if sequence > 0 && current.lastProgressSourceSeq > 0 && sequence <= current.lastProgressSourceSeq {
			return current, false
		}
		if sequence > 0 {
			next.lastProgressSourceSeq = sequence
		}
		next.revision++
		event.progress.Text = display
		next.lastProgress = display
		next.lastProgressEvent = event.progress
		next.lastProgressAt = event.at
		next.progressTimeline = appendTaskProgressTimeline(current.progressTimeline, event.progress)
		next.progressTimelineEnabled = current.progressTimelineEnabled || isStructuredTaskProgress(event.progress)
		return next, true
	case taskViewClosed:
		if current.closed {
			return current, false
		}
		next.closed = true
		return next, true
	case taskViewTerminal:
		if current.closed && strings.TrimSpace(current.terminalState) != "" {
			return current, false
		}
		next.closed = true
		next.terminalState = strings.TrimSpace(event.terminalState)
		next.terminalAt = event.at
		changed := next.closed != current.closed ||
			next.terminalState != current.terminalState ||
			!next.terminalAt.Equal(current.terminalAt)
		return next, changed
	default:
		return current, false
	}
}
