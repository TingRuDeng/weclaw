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

// handleCodexTurnEvent 处理 turn 生命周期事件，兼容 Codex app-server 的成功与失败形态。
func (a *ACPAgent) handleCodexTurnEvent(method string, params json.RawMessage) {
	var p struct {
		ThreadID string          `json:"threadId"`
		Status   string          `json:"status"`
		Error    json.RawMessage `json:"error"`
		Message  string          `json:"message"`
		Code     string          `json:"code"`
		Turn     struct {
			ID     string          `json:"id"`
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	turnID := strings.TrimSpace(p.Turn.ID)
	if method == "turn/started" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "started", TurnID: turnID})
		return
	}
	if method == "turn/completed" {
		a.dispatchToTurnCh(p.ThreadID, codexCompletedEvent(turnID, firstNonEmpty(p.Turn.Status, p.Status), p.Turn.Error))
		return
	}
	if method == "turn/failed" {
		text := firstNonEmpty(formatCodexTurnError(p.Error), p.Message, p.Code, p.Status)
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "error", TurnID: turnID, Text: joinCodexErrorParts("Codex turn 执行失败", text, "")})
	}
}

func codexCompletedEvent(turnID string, status string, rawError json.RawMessage) *codexTurnEvent {
	status = strings.TrimSpace(status)
	switch status {
	case "completed":
		return &codexTurnEvent{Kind: "completed", TurnID: turnID}
	case "interrupted":
		return &codexTurnEvent{Kind: "error", TurnID: turnID, Text: "Codex turn 已中断"}
	case "failed":
		text := firstNonEmpty(formatCodexTurnError(rawError), "未知错误")
		return &codexTurnEvent{Kind: "error", TurnID: turnID, Text: joinCodexErrorParts("Codex turn 执行失败", text, "")}
	default:
		return &codexTurnEvent{Kind: "error", TurnID: turnID, Text: joinCodexErrorParts("Codex turn 终态无效", status, "")}
	}
}

func formatCodexTurnError(rawError json.RawMessage) string {
	if len(rawError) == 0 || string(rawError) == "null" {
		return ""
	}
	wrapper, err := json.Marshal(struct {
		Error json.RawMessage `json:"error"`
	}{Error: rawError})
	if err != nil {
		return ""
	}
	return formatCodexError(wrapper)
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
		// 新版 app-server 可能在传输回退前发送空 error，权威终态仍由 turn/completed 给出。
		log.Printf("[acp] ignoring codex error without actionable details: %.200s", string(params))
		return
	}
	a.dispatchToTurnCh("", &codexTurnEvent{Kind: "error", Text: text})
}

func formatCodexError(params json.RawMessage) string {
	var p struct {
		Error struct {
			Message           string          `json:"message"`
			CodexErrorInfo    string          `json:"codexErrorInfo"`
			Code              string          `json:"code"`
			AdditionalDetails json.RawMessage `json:"additionalDetails"`
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
	summary := formatCodexErrorSummary(message, info)
	return appendCodexAdditionalDetails(summary, p.Error.AdditionalDetails)
}

func formatCodexErrorSummary(message string, info string) string {
	if info == "deactivated_workspace" {
		return joinCodexErrorParts("Codex 工作区不可用", message, info)
	}
	if info == "usageLimitExceeded" && message != "" {
		return "Codex 账号额度已用完：" + message + " (" + info + ")"
	}
	if info != "" && message != "" {
		return message + " (" + info + ")"
	}
	return firstNonEmpty(message, info)
}

func appendCodexAdditionalDetails(summary string, raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return summary
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		text = string(raw)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return summary
	}
	if summary == "" {
		return text
	}
	return summary + "；" + text
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
	if isCodexUsageLimitError(text) {
		return text
	}
	// 普通 stderr 可能属于更早的请求，不能替代当前 turn/completed 的权威终态。
	return ""
}

// isRecoverableCodexTransportText 判断 Codex responses WebSocket 失败是否属于可恢复传输噪声。
func isRecoverableCodexTransportText(text string) bool {
	lower := strings.ToLower(text)
	hasWebSocketSignal := strings.Contains(lower, "responses_websocket") ||
		strings.Contains(lower, "responsestreamdisconnected") ||
		strings.Contains(lower, "websocket") ||
		strings.Contains(lower, "ws://")
	hasForbiddenSignal := strings.Contains(lower, "403 forbidden")
	hasFallbackSignal := strings.Contains(lower, "falling back from websockets to https transport")
	hasReconnectSignal := strings.Contains(lower, "reconnecting")
	hasConnectFailure := strings.Contains(lower, "failed to connect to websocket")
	return hasWebSocketSignal && (hasFallbackSignal || hasReconnectSignal || hasForbiddenSignal && hasConnectFailure)
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
