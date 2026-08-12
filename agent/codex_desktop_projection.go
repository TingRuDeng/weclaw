package agent

import (
	"sort"
	"strings"
)

const codexDesktopMaxProjectedTurns = 200

type codexDesktopProjectionState struct {
	turns              map[string]codexDesktopProjectedTurn
	order              []string
	terminal           map[string]bool
	terminalCandidates map[string]bool
	activeTombstones   map[string]codexDesktopProjectedTurn
}

type codexDesktopProjectedTurn struct {
	id, status string
	items      map[string]codexDesktopProjectedItem
	order      []string
	errorText  string
}

type codexDesktopProjectedItem struct {
	id, itemType, status, phase, text string
}

type codexDesktopTextEventSpec struct {
	turnID   string
	previous codexDesktopProjectedItem
	existed  bool
	current  codexDesktopProjectedItem
}

// projectCodexDesktopSnapshot 生成统一状态和相对上一 revision 的事件。
func projectCodexDesktopSnapshot(threadID string, raw map[string]any, previous *codexDesktopProjectionState) (CodexThreadState, map[string]codexDesktopPendingAction, codexDesktopProjectionState, []*codexTurnEvent) {
	projection := buildCodexDesktopProjection(raw)
	projection.terminal = copyCodexDesktopTerminals(previous)
	projection.terminalCandidates = copyCodexDesktopTerminalCandidates(previous)
	projection.activeTombstones = carryCodexDesktopActiveTombstones(previous, projection.turns)
	events := projectCodexDesktopEvents(previous, &projection)
	requests := projectCodexDesktopRequests(raw)
	state := buildCodexDesktopThreadState(codexDesktopThreadStateSpec{
		threadID: threadID, raw: raw, projection: projection, requests: requests,
	})
	return state, requests, projection, events
}

// buildCodexDesktopProjection 把原始 conversation state 压缩为事件差分指纹。
func buildCodexDesktopProjection(raw map[string]any) codexDesktopProjectionState {
	projection := codexDesktopProjectionState{
		turns: make(map[string]codexDesktopProjectedTurn), terminal: make(map[string]bool),
		terminalCandidates: make(map[string]bool),
		activeTombstones:   make(map[string]codexDesktopProjectedTurn),
	}
	for _, value := range codexDesktopTurnValues(raw) {
		turn, _ := value.(map[string]any)
		projected := buildCodexDesktopProjectedTurn(turn)
		if projected.id == "" {
			continue
		}
		projection.turns[projected.id] = projected
		projection.order = append(projection.order, projected.id)
	}
	trimCodexDesktopProjection(&projection)
	return projection
}

// carryCodexDesktopActiveTombstones 跨过 Desktop 活动实体先移除、终态实体后归档的中间修订。
func carryCodexDesktopActiveTombstones(previous *codexDesktopProjectionState, current map[string]codexDesktopProjectedTurn) map[string]codexDesktopProjectedTurn {
	result := make(map[string]codexDesktopProjectedTurn)
	if previous == nil {
		return result
	}
	for turnID, turn := range previous.activeTombstones {
		result[turnID] = turn
	}
	for turnID, turn := range previous.turns {
		if _, exists := current[turnID]; !exists && isCodexDesktopActiveStatus(turn.status) {
			result[turnID] = turn
		}
	}
	for turnID, turn := range current {
		if isCodexDesktopActiveStatus(turn.status) {
			delete(result, turnID)
		}
	}
	return result
}

// codexDesktopTurnValues 合并短会话 turns 与长会话有序 turnHistory。
func codexDesktopTurnValues(raw map[string]any) []any {
	turns, _ := raw["turns"].([]any)
	result := append([]any(nil), turns...)
	seen := codexDesktopTurnIDs(result)
	history, entities := codexDesktopHistory(raw)
	islands, _ := history["islands"].([]any)
	for _, islandValue := range islands {
		island, _ := islandValue.(map[string]any)
		entries, _ := island["entries"].([]any)
		for _, entryValue := range entries {
			entry, _ := entryValue.(map[string]any)
			result = appendCodexDesktopHistoryTurn(result, seen, entities[codexDesktopString(entry["value"])])
		}
	}
	keys := make([]string, 0, len(entities))
	for key := range entities {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = appendCodexDesktopHistoryTurn(result, seen, entities[key])
	}
	return result
}

func codexDesktopHistory(raw map[string]any) (map[string]any, map[string]any) {
	turnHistory, _ := raw["turnHistory"].(map[string]any)
	history, _ := turnHistory["history"].(map[string]any)
	entities, _ := history["entitiesByKey"].(map[string]any)
	return history, entities
}

func codexDesktopTurnIDs(values []any) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		turn, _ := value.(map[string]any)
		if id := firstNonEmpty(codexDesktopString(turn["turnId"]), codexDesktopString(turn["id"])); id != "" {
			result[id] = true
		}
	}
	return result
}

func appendCodexDesktopHistoryTurn(result []any, seen map[string]bool, value any) []any {
	turn, _ := value.(map[string]any)
	id := firstNonEmpty(codexDesktopString(turn["turnId"]), codexDesktopString(turn["id"]))
	if id == "" || seen[id] {
		return result
	}
	seen[id] = true
	return append(result, turn)
}

// buildCodexDesktopProjectedTurn 提取单个 turn 的状态与 item 指纹。
func buildCodexDesktopProjectedTurn(turn map[string]any) codexDesktopProjectedTurn {
	projected := codexDesktopProjectedTurn{
		id:     firstNonEmpty(codexDesktopString(turn["turnId"]), codexDesktopString(turn["id"])),
		status: codexDesktopString(turn["status"]), items: make(map[string]codexDesktopProjectedItem),
		errorText: codexDesktopErrorText(turn["error"]),
	}
	items, _ := turn["items"].([]any)
	for _, value := range items {
		item, _ := value.(map[string]any)
		projectedItem := buildCodexDesktopProjectedItem(item)
		if projectedItem.id == "" {
			continue
		}
		projected.items[projectedItem.id] = projectedItem
		projected.order = append(projected.order, projectedItem.id)
	}
	return projected
}

// buildCodexDesktopProjectedItem 提取文本或进度事件所需字段。
func buildCodexDesktopProjectedItem(item map[string]any) codexDesktopProjectedItem {
	itemType := codexDesktopString(item["type"])
	return codexDesktopProjectedItem{
		id: codexDesktopID(item["id"]), itemType: itemType,
		status: codexDesktopString(item["status"]), phase: codexDesktopString(item["phase"]),
		text: codexDesktopItemText(item),
	}
}

// projectCodexDesktopEvents 按 turn 隔离生成 started、item 和终态事件。
func projectCodexDesktopEvents(previous *codexDesktopProjectionState, current *codexDesktopProjectionState) []*codexTurnEvent {
	var events []*codexTurnEvent
	baseline := previous == nil
	for _, turnID := range current.order {
		turn := current.turns[turnID]
		previousTurn, existed := codexDesktopPreviousTurn(previous, turnID)
		if isCodexDesktopActiveStatus(turn.status) && (!existed || !isCodexDesktopActiveStatus(previousTurn.status)) {
			events = append(events, &codexTurnEvent{Kind: "started", TurnID: turnID})
		}
		if !baseline || isCodexDesktopActiveStatus(turn.status) {
			var previousPointer *codexDesktopProjectedTurn
			if existed {
				previousPointer = &previousTurn
			}
			events = append(events, projectCodexDesktopItems(turnID, previousPointer, turn)...)
		}
		terminal := isCodexDesktopTerminalStatus(turn.status)
		if baseline && terminal {
			current.terminal[turnID] = true
			delete(current.terminalCandidates, turnID)
			continue
		}
		if terminal && !current.terminal[turnID] {
			transitioned := existed && isCodexDesktopActiveStatus(previousTurn.status)
			ready := turn.status != "completed" || codexDesktopTurnHasFinalAnswer(turn)
			switch {
			case transitioned && !ready:
				// Desktop 可能先把 turn 标成 completed，后续 revision 才补 final_answer。
				// 无正文终态由 watcher 的主动刷新屏障收敛，普通状态 revision 不得提前结束。
				current.terminalCandidates[turnID] = true
			case ready && (transitioned || current.terminalCandidates[turnID]):
				events = append(events, codexDesktopTerminalEvent(turn))
				current.terminal[turnID] = true
				delete(current.terminalCandidates, turnID)
			}
		}
		if terminal {
			delete(current.activeTombstones, turnID)
		} else {
			delete(current.terminalCandidates, turnID)
		}
	}
	return events
}

func codexDesktopTurnHasFinalAnswer(turn codexDesktopProjectedTurn) bool {
	for _, itemID := range turn.order {
		item := turn.items[itemID]
		phase := strings.ToLower(strings.TrimSpace(item.phase))
		if strings.EqualFold(item.itemType, "agentMessage") && strings.TrimSpace(item.text) != "" &&
			(phase == "final_answer" || phase == "") {
			return true
		}
	}
	return false
}

// projectCodexDesktopItems 对同一 turn 的 item 做有序差分。
func projectCodexDesktopItems(turnID string, previous *codexDesktopProjectedTurn, current codexDesktopProjectedTurn) []*codexTurnEvent {
	var events []*codexTurnEvent
	for _, itemID := range current.order {
		item := current.items[itemID]
		var old codexDesktopProjectedItem
		hadItem := false
		if previous != nil {
			old, hadItem = previous.items[itemID]
		}
		switch strings.ToLower(item.itemType) {
		case "agentmessage":
			spec := codexDesktopTextEventSpec{
				turnID: turnID, previous: old, existed: hadItem, current: item,
			}
			if event := codexDesktopTextEvent(spec); event != nil {
				events = append(events, event)
			}
		case "commandexecution":
			if event := codexDesktopActivityEvent(turnID, old, hadItem, item); event != nil {
				events = append(events, event)
			}
		}
	}
	return events
}

// codexDesktopActivityEvent 只标记 turn 仍在继续，不携带命令、输出或用户进度正文。
func codexDesktopActivityEvent(
	turnID string,
	previous codexDesktopProjectedItem,
	existed bool,
	current codexDesktopProjectedItem,
) *codexTurnEvent {
	if existed && previous.status == current.status {
		return nil
	}
	return &codexTurnEvent{Kind: "activity", TurnID: turnID, ItemID: current.id}
}

// codexDesktopTextEvent 只追加真实后缀；文本改写改发完整 snapshot。
func codexDesktopTextEvent(spec codexDesktopTextEventSpec) *codexTurnEvent {
	if spec.current.text == "" {
		return nil
	}
	currentCompleted := isCodexDesktopCompletedMessage(spec.current)
	previousCompleted := spec.existed && isCodexDesktopCompletedMessage(spec.previous)
	if currentCompleted && (!previousCompleted || spec.previous.text != spec.current.text || spec.previous.phase != spec.current.phase) {
		return &codexTurnEvent{
			Kind: "item_completed", TurnID: spec.turnID, MessagePhase: spec.current.phase,
			ItemID: spec.current.id, Text: spec.current.text,
		}
	}
	if spec.existed && spec.previous.text == spec.current.text {
		return nil
	}
	if strings.HasPrefix(spec.current.text, spec.previous.text) {
		return &codexTurnEvent{
			TurnID: spec.turnID, ItemID: spec.current.id, MessagePhase: spec.current.phase,
			Delta: strings.TrimPrefix(spec.current.text, spec.previous.text),
		}
	}
	return &codexTurnEvent{
		TurnID: spec.turnID, ItemID: spec.current.id, MessagePhase: spec.current.phase, Text: spec.current.text,
	}
}

func isCodexDesktopCompletedMessage(item codexDesktopProjectedItem) bool {
	status := strings.ToLower(strings.TrimSpace(item.status))
	if status == "completed" {
		return true
	}
	if status != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(item.phase)) {
	case "commentary", "final_answer":
		return true
	default:
		return false
	}
}

// codexDesktopTerminalEvent 把 Desktop turn 终态映射为现有统一事件。
func codexDesktopTerminalEvent(turn codexDesktopProjectedTurn) *codexTurnEvent {
	if turn.status == "completed" {
		return &codexTurnEvent{Kind: "completed", TurnID: turn.id}
	}
	message := firstNonEmpty(turn.errorText, "Codex turn 已"+turn.status)
	return &codexTurnEvent{Kind: "error", TurnID: turn.id, Text: message}
}
