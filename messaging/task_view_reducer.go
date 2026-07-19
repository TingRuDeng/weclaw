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
	lastProgress          string
	lastProgressEvent     agent.ProgressEvent
	lastProgressAt        time.Time
	lastProgressSourceSeq uint64
	revision              uint64
	closed                bool
	terminalState         string
	terminalAt            time.Time
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
		return next, true
	case taskViewClosed:
		if current.closed {
			return current, false
		}
		next.closed = true
		return next, true
	case taskViewTerminal:
		next.closed = true
		next.terminalState = strings.TrimSpace(event.terminalState)
		next.terminalAt = event.at
		return next, next != current
	default:
		return current, false
	}
}
