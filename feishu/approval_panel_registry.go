package feishu

import "strings"

// upsertApprovalPanelItem 记录或替换同一审批项，并返回可渲染快照。
func (r *taskCardRegistry) upsertApprovalPanelItem(taskCardID string, item approvalPanelItem) (approvalPanelSnapshot, bool) {
	if r == nil || strings.TrimSpace(taskCardID) == "" || strings.TrimSpace(item.Key) == "" {
		return approvalPanelSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[taskCardID]
	if state == nil {
		return approvalPanelSnapshot{}, false
	}
	item.TaskCard = strings.TrimSpace(taskCardID)
	state.approvalPanelRows = upsertApprovalPanelRow(state.approvalPanelRows, item)
	state.approvalPanelSeq++
	state.updatedAt = r.nowOrDefault()
	return state.approvalPanelSnapshot(), true
}

func upsertApprovalPanelRow(rows []approvalPanelItem, item approvalPanelItem) []approvalPanelItem {
	for index := range rows {
		if rows[index].Key == item.Key {
			rows[index] = item
			return rows
		}
	}
	return append(rows, item)
}

func (r *taskCardRegistry) bindApprovalPanelCard(taskCardID string, panelCardID string) (approvalPanelSnapshot, bool) {
	if r == nil || strings.TrimSpace(taskCardID) == "" || strings.TrimSpace(panelCardID) == "" {
		return approvalPanelSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[taskCardID]
	if state == nil {
		return approvalPanelSnapshot{}, false
	}
	state.approvalPanelID = strings.TrimSpace(panelCardID)
	state.updatedAt = r.nowOrDefault()
	return state.approvalPanelSnapshot(), true
}

func (r *taskCardRegistry) completeApprovalPanelItem(action parsedCardAction) (approvalPanelSnapshot, bool) {
	if r == nil || strings.TrimSpace(action.TaskCard) == "" || strings.TrimSpace(action.Approval) == "" {
		return approvalPanelSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[action.TaskCard]
	if state == nil {
		return approvalPanelSnapshot{}, false
	}
	for index := range state.approvalPanelRows {
		if state.approvalPanelRows[index].Key == action.Approval {
			state.approvalPanelRows[index].Choice = action.Choice
			state.approvalPanelRows[index].Label = action.Label
			state.approvalPanelRows[index].Status = action.Status
			state.approvalPanelSeq++
			state.updatedAt = r.nowOrDefault()
			return state.approvalPanelSnapshot(), true
		}
	}
	return approvalPanelSnapshot{}, false
}

func (r *taskCardRegistry) removeApprovalPanelItem(taskCardID string, approvalKey string) (approvalPanelSnapshot, bool) {
	if r == nil || strings.TrimSpace(taskCardID) == "" || strings.TrimSpace(approvalKey) == "" {
		return approvalPanelSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[taskCardID]
	if state == nil {
		return approvalPanelSnapshot{}, false
	}
	rows, removed := removeApprovalPanelRow(state.approvalPanelRows, approvalKey)
	state.approvalPanelRows = rows
	if removed {
		state.approvalPanelSeq++
	}
	state.updatedAt = r.nowOrDefault()
	return state.approvalPanelSnapshot(), true
}

func removeApprovalPanelRow(rows []approvalPanelItem, approvalKey string) ([]approvalPanelItem, bool) {
	for index := range rows {
		if rows[index].Key == approvalKey {
			return append(rows[:index], rows[index+1:]...), true
		}
	}
	return rows, false
}

func (s *taskCardState) approvalPanelSnapshot() approvalPanelSnapshot {
	return approvalPanelSnapshot{
		CardID: s.approvalPanelID,
		Seq:    s.approvalPanelSeq,
		Items:  append([]approvalPanelItem(nil), s.approvalPanelRows...),
	}
}
