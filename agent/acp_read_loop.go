package agent

import (
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
	a.mu.Unlock()
	if scanner == nil {
		return
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg rpcResponse
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("[acp] failed to parse message: %v", err)
			continue
		}

		// Response to a request we made (has id, no method)
		if msg.ID != nil && msg.Method == "" {
			a.pendingMu.Lock()
			ch, ok := a.pending[*msg.ID]
			a.pendingMu.Unlock()
			if ok {
				ch <- &msg
			}
			continue
		}

		// 处理 agent 主动发出的请求或通知。
		switch msg.Method {
		case "session/update":
			a.handleSessionUpdate(msg.Params)

		case "session/request_permission":
			// 旧 ACP 权限请求会复用统一审批处理链路。
			a.handlePermissionRequest(line)

		// Codex app-server 事件，不同版本会发出不同 method。
		case "codex/event/agent_message_delta":
			a.handleCodexDelta(msg.Params)
		case "item/agentMessage/delta":
			a.handleCodexItemDelta(msg.Params)
		case "item/started":
			a.handleCodexItemStarted(msg.Params)
		case "turn/started", "turn/completed", "turn/failed":
			a.handleCodexTurnEvent(msg.Method, msg.Params)
		case "error":
			a.handleCodexError(msg.Params)
		case "item/completed":
			a.handleCodexItemCompleted(msg.Params)
		case "item/autoApprovalReview/started":
			a.handleCodexAutoApprovalReviewStarted(msg.Params)
		case "item/autoApprovalReview/completed":
			a.handleCodexAutoApprovalReviewCompleted(msg.Params)
		case "guardianWarning":
			a.handleCodexGuardianWarning(msg.Params)
		case "item/commandExecution/outputDelta":
			a.handleCodexCommandProgress(msg.Params)
		case "item/fileChange/outputDelta", "turn/diff/updated":
			a.handleCodexFileProgress(msg.Params)
		case "codex/event/agent_message", "codex/event/task_complete",
			"codex/event/item_completed", "codex/event/token_count",
			"thread/tokenUsage/updated",
			"account/rateLimits/updated", "thread/status/changed",
			"mcpServer/startupStatus/updated":
			// 这些是已知状态事件，当前桥接层不需要额外处理。
		case "turn/approval/request",
			"item/fileChange/requestApproval",
			"item/commandExecution/requestApproval":
			a.handlePermissionRequest(line)

		default:
			if a.shouldLogUnhandledMethod(msg.Method, time.Now()) {
				log.Printf("[acp] unhandled method: %s (raw: %.200s)", msg.Method, line)
			}
		}
	}

	exitReason := "ACP runtime exited"
	if err := scanner.Err(); err != nil {
		exitReason = fmt.Sprintf("ACP runtime read error: %v", err)
		log.Printf("[acp] read loop error: %v", err)
	}
	a.mu.Lock()
	currentScanner := a.scanner == scanner
	if a.scanner == scanner {
		a.started = false
		a.stdin = nil
		a.cmd = nil
		a.scanner = nil
	}
	a.mu.Unlock()
	if currentScanner {
		a.failRuntimeWaiters(exitReason)
	}
	log.Println("[acp] read loop ended")
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
	a.notifyMu.Lock()
	channels := make([]chan *codexTurnEvent, 0, len(a.turnCh))
	for _, ch := range a.turnCh {
		channels = append(channels, ch)
	}
	a.notifyMu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- evt:
		default:
		}
	}
}
