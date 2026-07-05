package agent

import (
	"encoding/json"
	"log"
	"strings"
)

func (a *ACPAgent) handleCodexDelta(params json.RawMessage) {
	var p struct {
		Msg struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"msg"`
		ConversationID string `json:"conversationId"`
		ThreadID       string `json:"threadId"` // some versions use threadId
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	// Try conversationId first (codex uses this), fallback to threadId
	key := p.ConversationID
	if key == "" {
		key = p.ThreadID
	}

	delta := p.Msg.Delta
	if delta == "" {
		return
	}

	a.dispatchToTurnCh(key, &codexTurnEvent{Delta: delta})
}

// handleCodexItemDelta handles "item/agentMessage/delta" events.
// These contain incremental text deltas for the agent's response.
func (a *ACPAgent) handleCodexItemDelta(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if p.Delta == "" {
		return
	}

	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{ItemID: p.ItemID, Delta: p.Delta})
}

// handleCodexItemStarted handles "item/started" events.
// When type=agentMessage, extracts text from content array.
func (a *ACPAgent) handleCodexItemStarted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Item     struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if p.Item.Type != "agentMessage" {
		return
	}

	for _, c := range p.Item.Content {
		if c.Type == "text" && c.Text != "" {
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{ItemID: p.Item.ID, Text: c.Text})
		}
	}
}

// handleCodexItemCompleted 将 completed 文本作为兜底最终文本来源。
func (a *ACPAgent) handleCodexItemCompleted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Item     struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if p.Item.Type != "agentMessage" {
		return
	}
	for _, c := range p.Item.Content {
		if c.Type == "text" && c.Text != "" {
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "item_completed", ItemID: p.Item.ID, Text: c.Text})
		}
	}
}

// handleCodexTurnEvent handles "turn/started" and "turn/completed" notifications.
func (a *ACPAgent) handleCodexTurnEvent(method string, params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if method == "turn/completed" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "completed"})
	}
}

func (a *ACPAgent) handleCodexError(params json.RawMessage) {
	if isRecoverableCodexTransportText(string(params)) {
		log.Printf("[acp] ignoring recoverable codex transport error: %.200s", string(params))
		return
	}
	text := formatCodexError(params)
	if text == "" && a.stderr != nil {
		stderrText := a.stderr.LastError()
		if isRecoverableCodexTransportText(stderrText) {
			log.Printf("[acp] ignoring recoverable codex stderr transport error: %.200s", stderrText)
			return
		}
		text = formatCodexStderrError(stderrText)
	}
	if text == "" {
		text = "Codex 返回未知错误"
	}
	a.dispatchToTurnCh("", &codexTurnEvent{Kind: "error", Text: text})
}

func formatCodexError(params json.RawMessage) string {
	var p struct {
		Error struct {
			Message        string `json:"message"`
			CodexErrorInfo string `json:"codexErrorInfo"`
			Code           string `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
		Code    string `json:"code"`
		Detail  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}

	message := firstNonEmpty(p.Error.Message, p.Message, p.Detail.Message)
	info := firstNonEmpty(p.Error.CodexErrorInfo, p.Error.Code, p.Code, p.Detail.Code)
	if isRecoverableCodexTransportText(message) || isRecoverableCodexTransportText(info) {
		return ""
	}
	if info == "deactivated_workspace" {
		return joinCodexErrorParts("Codex 工作区不可用", message, info)
	}
	if info == "usageLimitExceeded" && message != "" {
		return "Codex 账号额度已用完：" + message + " (" + info + ")"
	}
	if info != "" && message != "" {
		return message + " (" + info + ")"
	}
	if message != "" {
		return message
	}
	return info
}

// formatCodexStderrError 从 Codex stderr 中提取账号态错误，补足空泛的 error 事件。
func formatCodexStderrError(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "deactivated_workspace") {
		return joinCodexErrorParts("Codex 工作区不可用", text, "deactivated_workspace")
	}
	if strings.Contains(lower, "402 payment required") {
		return joinCodexErrorParts("Codex 认证或工作区不可用", text, "")
	}
	return text
}

// isRecoverableCodexTransportText 判断 Codex responses WebSocket 失败是否属于可恢复传输噪声。
func isRecoverableCodexTransportText(text string) bool {
	lower := strings.ToLower(text)
	hasWebSocketSignal := strings.Contains(lower, "responses_websocket") ||
		strings.Contains(lower, "websocket") ||
		strings.Contains(lower, "ws://")
	hasForbiddenSignal := strings.Contains(lower, "403 forbidden")
	hasRecoverSignal := strings.Contains(lower, "falling back from websockets to https transport") ||
		strings.Contains(lower, "failed to connect to websocket") ||
		strings.Contains(lower, "reconnecting")
	return hasWebSocketSignal && hasForbiddenSignal && hasRecoverSignal
}

// isCodexAuthStateError 判断错误是否来自登录态或工作区状态；额度耗尽不能刷新进程。
func isCodexAuthStateError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "deactivated_workspace") ||
		(strings.Contains(lower, "402 payment required") && !strings.Contains(lower, "usagelimitexceeded"))
}

// isCodexUsageLimitError 判断 Codex 当前账号额度是否耗尽。
func isCodexUsageLimitError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "usagelimitexceeded") ||
		strings.Contains(lower, "you've hit your usage limit")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func joinCodexErrorParts(title string, message string, code string) string {
	var parts []string
	if title != "" {
		parts = append(parts, title)
	}
	if message != "" {
		parts = append(parts, message)
	}
	if code != "" {
		parts = append(parts, "("+code+")")
	}
	return strings.Join(parts, "：")
}

// dispatchToTurnCh sends an event to the turn channel for a thread.
func (a *ACPAgent) dispatchToTurnCh(threadID string, evt *codexTurnEvent) bool {
	a.notifyMu.Lock()
	ch, ok := a.turnCh[threadID]
	if !ok {
		// 只有一个活跃 turn 时才 fallback，避免多会话事件串到错误用户。
		if len(a.turnCh) == 1 {
			for _, c := range a.turnCh {
				ch = c
				ok = true
				break
			}
		} else if len(a.turnCh) > 1 {
			log.Printf("[acp] dropping turn event without routable thread (thread=%q, activeTurns=%d, kind=%s)", threadID, len(a.turnCh), evt.Kind)
		}
	}
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- evt:
			return true
		default:
		}
	}
	return false
}
