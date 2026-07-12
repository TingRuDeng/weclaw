package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

type codexDesktopThreadStateSpec struct {
	threadID   string
	raw        map[string]any
	projection codexDesktopProjectionState
	requests   map[string]codexDesktopPendingAction
}

// projectCodexDesktopRequests 提取尚未解决的 Desktop 请求。
func projectCodexDesktopRequests(raw map[string]any) map[string]codexDesktopPendingAction {
	result := make(map[string]codexDesktopPendingAction)
	requests, _ := raw["requests"].([]any)
	for _, value := range requests {
		request, _ := value.(map[string]any)
		id := codexDesktopID(request["id"])
		if id == "" || isCodexDesktopResolvedRequest(request) {
			continue
		}
		params, _ := request["params"].(map[string]any)
		result[id] = codexDesktopPendingAction{
			ID: id, Method: codexDesktopString(request["method"]), Params: params,
		}
	}
	return result
}

// isCodexDesktopResolvedRequest 识别 Desktop 已完成且不应再次交互的请求。
func isCodexDesktopResolvedRequest(request map[string]any) bool {
	if completed, _ := request["completed"].(bool); completed {
		return true
	}
	switch strings.ToLower(codexDesktopString(request["status"])) {
	case "completed", "resolved", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

// codexDesktopString 只接受协议字符串并统一去除首尾空白。
func codexDesktopString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// codexDesktopProjectionPointer 仅在同一连接代次复用差分记忆。
func codexDesktopProjectionPointer(snapshot codexDesktopThreadSnapshot, valid bool) *codexDesktopProjectionState {
	if !valid {
		return nil
	}
	return &snapshot.projection
}

// buildCodexDesktopThreadState 生成 Messaging 可直接消费的统一 thread 状态。
func buildCodexDesktopThreadState(spec codexDesktopThreadStateSpec) CodexThreadState {
	state := CodexThreadState{
		ThreadID: spec.threadID, Model: codexDesktopString(spec.raw["latestModel"]),
		Effort: codexDesktopString(spec.raw["latestReasoningEffort"]),
	}
	for _, turnID := range spec.projection.order {
		turn := spec.projection.turns[turnID]
		if isCodexDesktopActiveStatus(turn.status) {
			state.Active, state.ActiveTurnID = true, turnID
		}
		projectCodexDesktopItemText(&state, turn)
	}
	for _, request := range spec.requests {
		state.WaitingOnUserInput = state.WaitingOnUserInput || request.Method == "item/tool/requestUserInput"
		state.WaitingOnApproval = state.WaitingOnApproval || strings.Contains(request.Method, "requestApproval")
	}
	return state
}

// projectCodexDesktopItemText 更新最近用户预览和最终助手文本。
func projectCodexDesktopItemText(state *CodexThreadState, turn codexDesktopProjectedTurn) {
	for _, itemID := range turn.order {
		item := turn.items[itemID]
		switch strings.ToLower(item.itemType) {
		case "usermessage":
			state.Preview = item.text
		case "agentmessage":
			state.LastAgentMessageText = item.text
		}
	}
}

// codexDesktopItemText 兼容直接 text 与 content 文本片段。
func codexDesktopItemText(item map[string]any) string {
	if text := codexDesktopString(item["text"]); text != "" {
		return text
	}
	content, _ := item["content"].([]any)
	var parts []string
	for _, value := range content {
		part, _ := value.(map[string]any)
		if strings.ToLower(codexDesktopString(part["type"])) == "text" {
			parts = append(parts, codexDesktopString(part["text"]))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// codexDesktopItemProgress 将命令和文件 item 映射为结构化进度。
func codexDesktopItemProgress(itemType string, item map[string]any) *codexProgressEvent {
	kind := ""
	switch strings.ToLower(itemType) {
	case "commandexecution":
		kind = "command"
	case "filechange":
		kind = "file"
	default:
		return nil
	}
	detail := firstNonEmpty(
		codexDesktopString(item["aggregatedOutput"]), codexDesktopString(item["output"]),
		codexDesktopString(item["message"]), codexDesktopString(item["status"]),
	)
	return &codexProgressEvent{
		Kind: kind, Action: codexDesktopDisplayValue(item["command"]), Detail: detail,
		FilePath: firstNonEmpty(codexDesktopString(item["filePath"]), codexDesktopString(item["path"])),
	}
}

// codexDesktopPreviousTurn 安全读取上一 revision 的同名 turn。
func codexDesktopPreviousTurn(previous *codexDesktopProjectionState, turnID string) (codexDesktopProjectedTurn, bool) {
	if previous == nil {
		return codexDesktopProjectedTurn{}, false
	}
	if turn, ok := previous.turns[turnID]; ok {
		return turn, true
	}
	turn, ok := previous.activeTombstones[turnID]
	return turn, ok
}

// copyCodexDesktopTerminals 复制已发送终态集合，保证每个 turn 只结束一次。
func copyCodexDesktopTerminals(previous *codexDesktopProjectionState) map[string]bool {
	result := make(map[string]bool)
	if previous != nil {
		for turnID, emitted := range previous.terminal {
			result[turnID] = emitted
		}
	}
	return result
}

// cloneCodexDesktopProjection 深拷贝内部差分记忆。
func cloneCodexDesktopProjection(source codexDesktopProjectionState) codexDesktopProjectionState {
	clone := codexDesktopProjectionState{
		turns: make(map[string]codexDesktopProjectedTurn, len(source.turns)),
		order: append([]string(nil), source.order...), terminal: copyCodexDesktopTerminals(&source),
		activeTombstones: make(map[string]codexDesktopProjectedTurn, len(source.activeTombstones)),
	}
	for turnID, turn := range source.turns {
		turn.order = append([]string(nil), turn.order...)
		turn.items = cloneCodexDesktopProjectedItems(turn.items)
		clone.turns[turnID] = turn
	}
	for turnID, turn := range source.activeTombstones {
		turn.order = append([]string(nil), turn.order...)
		turn.items = cloneCodexDesktopProjectedItems(turn.items)
		clone.activeTombstones[turnID] = turn
	}
	return clone
}

// cloneCodexDesktopProjectedItems 复制 item map 和进度指针。
func cloneCodexDesktopProjectedItems(source map[string]codexDesktopProjectedItem) map[string]codexDesktopProjectedItem {
	result := make(map[string]codexDesktopProjectedItem, len(source))
	for itemID, item := range source {
		if item.progress != nil {
			progress := *item.progress
			item.progress = &progress
		}
		result[itemID] = item
	}
	return result
}

// trimCodexDesktopProjection 保留 active turn，并将空闲历史限制在最近窗口。
func trimCodexDesktopProjection(projection *codexDesktopProjectionState) {
	if len(projection.order) <= codexDesktopMaxProjectedTurns {
		return
	}
	keep := make(map[string]bool)
	for _, turnID := range projection.order {
		if isCodexDesktopActiveStatus(projection.turns[turnID].status) {
			keep[turnID] = true
		}
	}
	for index := len(projection.order) - 1; index >= 0 && len(keep) < codexDesktopMaxProjectedTurns; index-- {
		keep[projection.order[index]] = true
	}
	projection.order = filterCodexDesktopTurns(projection, keep)
}

// filterCodexDesktopTurns 按原顺序删除不在保留集合中的 turn。
func filterCodexDesktopTurns(projection *codexDesktopProjectionState, keep map[string]bool) []string {
	var order []string
	for _, turnID := range projection.order {
		if keep[turnID] {
			order = append(order, turnID)
		} else {
			delete(projection.turns, turnID)
		}
	}
	return order
}

// isCodexDesktopActiveStatus 只接受实机或 app-server 已知 active 状态。
func isCodexDesktopActiveStatus(status string) bool {
	switch status {
	case "inProgress", "running", "active", "processing":
		return true
	default:
		return false
	}
}

// isCodexDesktopTerminalStatus 只接受明确终态，未知状态不猜测。
func isCodexDesktopTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "interrupted", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

// codexDesktopProjectedItemsEqual 判断进度 item 是否产生可见变化。
func codexDesktopProjectedItemsEqual(left codexDesktopProjectedItem, right codexDesktopProjectedItem) bool {
	if left.status != right.status || left.text != right.text {
		return false
	}
	if left.progress == nil || right.progress == nil {
		return left.progress == nil && right.progress == nil
	}
	return *left.progress == *right.progress
}

// codexDesktopErrorText 从字符串或 error 对象提取可读错误。
func codexDesktopErrorText(value any) string {
	if text := codexDesktopString(value); text != "" {
		return text
	}
	object, _ := value.(map[string]any)
	return codexDesktopString(object["message"])
}

// codexDesktopDisplayValue 把结构化命令转换成稳定显示文本。
func codexDesktopDisplayValue(value any) string {
	if value == nil {
		return ""
	}
	if text := codexDesktopString(value); text != "" {
		return text
	}
	encoded, _ := json.Marshal(value)
	return strings.TrimSpace(string(encoded))
}

// codexDesktopID 兼容字符串和 JSON 数字 request/item ID。
func codexDesktopID(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return string(typed)
	case float64:
		return fmt.Sprintf("%g", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	default:
		return ""
	}
}
