package agent

import "strings"

type codexFinalAssembler struct {
	order           []string
	deltasByItem    map[string][]string
	snapshotsByItem map[string]string
	completedByItem map[string]string
}

func newCodexFinalAssembler() *codexFinalAssembler {
	return &codexFinalAssembler{
		deltasByItem:    make(map[string][]string),
		snapshotsByItem: make(map[string]string),
		completedByItem: make(map[string]string),
	}
}

func (a *codexFinalAssembler) addDelta(itemID string, text string) {
	itemID = normalizeCodexItemID(itemID)
	a.rememberItem(itemID)
	a.deltasByItem[itemID] = append(a.deltasByItem[itemID], text)
}

func (a *codexFinalAssembler) addSnapshot(itemID string, text string) {
	itemID = normalizeCodexItemID(itemID)
	a.rememberItem(itemID)
	a.snapshotsByItem[itemID] = text
}

func (a *codexFinalAssembler) addCompleted(itemID string, text string) {
	itemID = normalizeCodexItemID(itemID)
	a.rememberItem(itemID)
	a.completedByItem[itemID] = text
}

func (a *codexFinalAssembler) finalText() string {
	for i := len(a.order) - 1; i >= 0; i-- {
		if text := a.itemText(a.order[i]); text != "" {
			return text
		}
	}
	return ""
}

func (a *codexFinalAssembler) itemText(itemID string) string {
	if deltas := a.deltasByItem[itemID]; len(deltas) > 0 {
		return strings.TrimSpace(strings.Join(deltas, ""))
	}
	if text := strings.TrimSpace(a.completedByItem[itemID]); text != "" {
		return text
	}
	return strings.TrimSpace(a.snapshotsByItem[itemID])
}

func (a *codexFinalAssembler) rememberItem(itemID string) {
	for _, existing := range a.order {
		if existing == itemID {
			return
		}
	}
	a.order = append(a.order, itemID)
}

func normalizeCodexItemID(itemID string) string {
	if itemID == "" {
		return "__default__"
	}
	return itemID
}
