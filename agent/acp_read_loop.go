package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// readLoop reads NDJSON lines from stdout and dispatches to pending requests or notification channels.
func (a *ACPAgent) readLoop() {
	a.mu.Lock()
	scanner := a.scanner
	epoch := a.wireEpoch
	a.mu.Unlock()
	if scanner == nil {
		return
	}

	for scanner.Scan() {
		if !a.handleCurrentACPWireLine(scanner, epoch, scanner.Text()) {
			break
		}
	}
	a.finishReadLoop(scanner, epoch)
	log.Println("[acp] read loop ended")
}

// handleCurrentACPWireLine 在同一临界区核对 reader generation 并完成分发。
// 账号切换断开旧连接后，旧 reader 即使还读到缓存帧也只能丢弃。
func (a *ACPAgent) handleCurrentACPWireLine(scanner *bufio.Scanner, epoch uint64, line string) bool {
	a.wireDispatchMu.Lock()
	defer a.wireDispatchMu.Unlock()
	a.mu.Lock()
	current := a.scanner == scanner && a.wireEpoch == epoch
	a.mu.Unlock()
	if !current {
		return false
	}
	a.handleACPWireLine(line)
	return true
}

// handleACPWireLine 解析单条 NDJSON，并区分请求响应与主动通知。
func (a *ACPAgent) handleACPWireLine(line string) {
	if line == "" {
		return
	}
	var msg rpcResponse
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		log.Printf("[acp] failed to parse message: %v", err)
		return
	}
	msg.Sequence = a.wireSequence.Add(1)
	if msg.ID != nil && msg.Method == "" {
		a.dispatchACPResponse(&msg)
		return
	}
	if a.dispatchACPNotification(msg, line) {
		return
	}
	if a.shouldLogUnhandledMethod(msg.Method, time.Now()) {
		log.Printf("[acp] unhandled method: %s", msg.Method)
	}
}

// dispatchACPResponse 将响应投递给对应 RPC 等待者。
func (a *ACPAgent) dispatchACPResponse(msg *rpcResponse) {
	a.pendingMu.Lock()
	ch, ok := a.pending[*msg.ID]
	a.pendingMu.Unlock()
	if ok {
		select {
		case ch <- msg:
		default:
		}
	}
}

// dispatchACPNotification 处理标准 ACP 通知并转交 Codex 专属分组。
func (a *ACPAgent) dispatchACPNotification(msg rpcResponse, line string) bool {
	switch msg.Method {
	case "session/update":
		a.handleSessionUpdateAt(msg.Params, msg.Sequence)
		return true
	case "session/request_permission":
		a.handlePermissionRequest(line)
		return true
	default:
		return a.dispatchCodexNotification(msg, line)
	}
}

// dispatchCodexNotification 按职责分组处理 Codex app-server 事件。
func (a *ACPAgent) dispatchCodexNotification(msg rpcResponse, line string) bool {
	return a.dispatchCodexMessageNotification(msg) ||
		a.dispatchCodexTurnNotification(msg) ||
		a.dispatchCodexProgressNotification(msg) ||
		a.dispatchCodexKnownNotification(msg, line)
}

// dispatchCodexMessageNotification 处理消息增量和 item 生命周期事件。
func (a *ACPAgent) dispatchCodexMessageNotification(msg rpcResponse) bool {
	switch msg.Method {
	case "codex/event/agent_message_delta":
		a.handleCodexDelta(msg.Params)
	case "item/agentMessage/delta":
		a.handleCodexItemDelta(msg.Params)
	case "item/started":
		a.handleCodexItemStarted(msg.Params)
	case "item/completed":
		a.handleCodexItemCompleted(msg.Params)
	default:
		return false
	}
	return true
}

// dispatchCodexTurnNotification 处理 turn 终态、计划、warning 和 error。
func (a *ACPAgent) dispatchCodexTurnNotification(msg rpcResponse) bool {
	switch msg.Method {
	case "turn/started", "turn/completed", "turn/failed":
		a.handleCodexTurnEvent(msg.Method, msg.Params)
	case "turn/plan/updated":
		a.handleCodexPlanUpdated(msg.Params)
	case "warning":
		a.handleCodexWarning(msg.Params)
	case "error":
		a.handleCodexError(msg.Params)
	default:
		return false
	}
	return true
}

// dispatchCodexProgressNotification 处理审批审查、guardian、命令和文件进度。
func (a *ACPAgent) dispatchCodexProgressNotification(msg rpcResponse) bool {
	switch msg.Method {
	case "item/autoApprovalReview/started":
		a.handleCodexAutoApprovalReviewStarted(msg.Params)
	case "item/autoApprovalReview/completed":
		a.handleCodexAutoApprovalReviewCompleted(msg.Params)
	case "guardianWarning":
		a.handleCodexGuardianWarning(msg.Params)
	case "item/commandExecution/outputDelta", "item/commandExecution/terminalInteraction":
		a.handleCodexCommandProgress(msg.Params)
	case "item/fileChange/outputDelta", "item/fileChange/patchUpdated", "turn/diff/updated":
		a.handleCodexFileProgress(msg.Params)
	default:
		return false
	}
	return true
}

// dispatchCodexKnownNotification 消费已知状态事件和 Codex 审批请求。
func (a *ACPAgent) dispatchCodexKnownNotification(msg rpcResponse, line string) bool {
	switch msg.Method {
	case "codex/event/agent_message", "codex/event/task_complete",
		"codex/event/item_completed", "codex/event/token_count", "thread/tokenUsage/updated",
		"account/rateLimits/updated", "thread/status/changed", "mcpServer/startupStatus/updated",
		"remoteControl/status/changed":
		return true
	case "thread/settings/updated":
		a.handleCodexThreadSettingsUpdated(msg.Params, msg.Sequence)
		return true
	case "turn/approval/request", "item/fileChange/requestApproval",
		"item/commandExecution/requestApproval", "item/permissions/requestApproval":
		a.handlePermissionRequest(line)
		return true
	default:
		return false
	}
}

// finishReadLoop 清理当前 runtime，并唤醒所有仍在等待的调用者。
func (a *ACPAgent) finishReadLoop(scanner *bufio.Scanner, epoch uint64) {
	exitReason := "ACP runtime exited"
	if err := scanner.Err(); err != nil {
		exitReason = fmt.Sprintf("ACP runtime read error: %v", err)
		log.Printf("[acp] read loop error: %v", err)
	}
	a.wireDispatchMu.Lock()
	a.mu.Lock()
	currentScanner := a.scanner == scanner && a.wireEpoch == epoch
	if currentScanner {
		a.started = false
		a.stdin = nil
		a.cmd = nil
		a.scanner = nil
	}
	a.mu.Unlock()
	a.wireDispatchMu.Unlock()
	if currentScanner {
		a.failRuntimeWaitersUncertain(exitReason)
	}
}

func (a *ACPAgent) shouldLogUnhandledMethod(method string, now time.Time) bool {
	method = strings.TrimSpace(method)
	if method == "" {
		return false
	}

	a.unhandledLogMu.Lock()
	defer a.unhandledLogMu.Unlock()
	if a.unhandledLogAt == nil {
		a.unhandledLogAt = make(map[string]time.Time)
	}
	last, ok := a.unhandledLogAt[method]
	if ok && now.Sub(last) < acpUnhandledMethodLogInterval {
		return false
	}
	a.unhandledLogAt[method] = now
	return true
}

func (a *ACPAgent) failRuntimeWaiters(reason string) {
	a.failPendingRequests(reason)
	a.failActiveTurns(reason)
}

// failRuntimeWaitersUncertain wakes turn observers before pending RPCs. A
// turn/start request may already have reached the shared app-server even when
// its response was lost, so the observer must enter reconciliation instead of
// reporting a confirmed failure and releasing the writer lease.
func (a *ACPAgent) failRuntimeWaitersUncertain(reason string) {
	a.interruptActiveTurns(reason)
	a.failPendingRequests(reason)
}

func (a *ACPAgent) failPendingRequests(reason string) {
	resp := &rpcResponse{
		Error: &rpcError{Code: -32000, Message: reason},
	}

	a.pendingMu.Lock()
	channels := make([]chan *rpcResponse, 0, len(a.pending))
	for id, ch := range a.pending {
		delete(a.pending, id)
		channels = append(channels, ch)
	}
	a.pendingMu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- resp:
		default:
		}
	}
}

func (a *ACPAgent) failActiveTurns(reason string) {
	evt := &codexTurnEvent{Kind: "error", Text: reason}
	a.dispatchActiveTurnControlEvent(evt)
}

func (a *ACPAgent) interruptActiveTurns(reason string) {
	type turnTarget struct {
		channel chan *codexTurnEvent
		turnID  string
	}
	a.notifyMu.Lock()
	targets := make([]turnTarget, 0, len(a.turnCh))
	for threadID, channel := range a.turnCh {
		turnID := ""
		if a.codexOwners != nil {
			if binding, ok := a.codexOwners.threadBinding(threadID); ok {
				turnID = binding.State.ActiveTurnID
			}
		}
		targets = append(targets, turnTarget{channel: channel, turnID: turnID})
	}
	a.notifyMu.Unlock()
	for _, target := range targets {
		dispatchCodexTurnControlEvent(target.channel, &codexTurnEvent{
			Kind: "interrupted", TurnID: target.turnID, Text: reason,
		})
	}
}

func (a *ACPAgent) dispatchActiveTurnControlEvent(evt *codexTurnEvent) {
	a.notifyMu.Lock()
	channels := make([]chan *codexTurnEvent, 0, len(a.turnCh))
	for _, ch := range a.turnCh {
		channels = append(channels, ch)
	}
	a.notifyMu.Unlock()

	for _, ch := range channels {
		dispatchCodexTurnControlEvent(ch, evt)
	}
}
