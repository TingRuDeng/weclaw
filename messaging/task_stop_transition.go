package messaging

import "context"

type taskStopStatus uint8

const (
	taskStopAccepted taskStopStatus = iota
	taskStopDenied
	taskStopTerminal
	taskStopAlreadyRequested
)

type taskStopMode uint8

const (
	taskStopLocal taskStopMode = iota
	taskStopRemote
)

type taskStopRequest struct {
	actor  string
	detach bool
	mode   taskStopMode
}

type taskStopResult struct {
	status taskStopStatus
	cancel context.CancelFunc
}

// beginStopRequest 原子登记停止意图；远程模式在协议确认前不改变 watcher 可见的 phase。
func (t *activeAgentTask) beginStopRequest(req taskStopRequest) taskStopResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.owner != req.actor {
		return taskStopResult{status: taskStopDenied}
	}
	if t.phase == codexTaskTerminal {
		return taskStopResult{status: taskStopTerminal}
	}
	if t.stopRequested || t.phase == codexTaskStopping {
		return taskStopResult{status: taskStopAlreadyRequested}
	}
	if req.mode == taskStopRemote {
		t.stopRequested = true
		return taskStopResult{status: taskStopAccepted}
	}
	t.detached = req.detach
	t.phase = codexTaskStopping
	return taskStopResult{status: taskStopAccepted, cancel: t.cancel}
}

// commitRemoteStop 在远程协议接受中断后提交 stopping；并发终态始终优先。
func (t *activeAgentTask) commitRemoteStop() taskStopStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopRequested = false
	if t.phase == codexTaskTerminal {
		return taskStopTerminal
	}
	if t.phase == codexTaskStopping {
		return taskStopAlreadyRequested
	}
	t.phase = codexTaskStopping
	return taskStopAccepted
}

// rollbackRemoteStop 仅撤销仍未提交的远程停止占位，不回退已经形成的终态。
func (t *activeAgentTask) rollbackRemoteStop() {
	t.mu.Lock()
	t.stopRequested = false
	t.mu.Unlock()
}
