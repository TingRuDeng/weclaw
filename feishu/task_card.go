package feishu

import (
	"strings"
	"sync"
	"time"
)

const taskCardRecordTTL = 30 * time.Minute

type taskCardRegistry struct {
	mu    sync.Mutex
	cards map[string]*taskCardState
	now   func() time.Time
}

type taskCardState struct {
	taskCardID         string
	title              string
	status             string
	content            string
	summary            string
	approvals          []string
	sequence           int
	approvalPanelID    string
	approvalPanelSeq   int
	approvalPanelRows  []approvalPanelItem
	inlineActiveStatus bool
	collapsible        bool
	expanded           bool
	recoveryChanged    func()
	updatedAt          time.Time
}

func newTaskCardRegistry() *taskCardRegistry {
	return &taskCardRegistry{cards: make(map[string]*taskCardState), now: time.Now}
}

func (r *taskCardRegistry) record(cardID string, opts cardOptions) {
	r.recordWithSequence(cardID, opts, 0)
}

func (r *taskCardRegistry) recordWithSequence(cardID string, opts cardOptions, sequence int) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return
	}
	if sequence < 0 {
		sequence = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeLocked()
	r.cards[cardID] = &taskCardState{
		taskCardID:         cardID,
		title:              opts.Title,
		status:             normalizeCardStatus(opts.Status),
		content:            opts.Content,
		summary:            opts.Summary,
		approvals:          append([]string(nil), opts.Approvals...),
		sequence:           sequence,
		inlineActiveStatus: opts.InlineActiveStatus,
		collapsible:        opts.Collapsible,
		expanded:           opts.Expanded,
		updatedAt:          r.nowOrDefault(),
	}
}

func (r *taskCardRegistry) setExpandedWithSequence(cardID string, expanded bool) (cardOptions, int, bool) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return cardOptions{}, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[cardID]
	if state == nil || !state.collapsible {
		return cardOptions{}, 0, false
	}
	state.expanded = expanded
	state.sequence++
	state.updatedAt = r.nowOrDefault()
	return state.cardOptions(), state.sequence, true
}

func (r *taskCardRegistry) updateContent(cardID string, content string) {
	r.update(cardID, "", content)
}

func (r *taskCardRegistry) snapshot(cardID string) (cardOptions, bool) {
	opts, _, ok := r.snapshotWithSequence(cardID)
	return opts, ok
}

func (r *taskCardRegistry) snapshotWithSequence(cardID string) (cardOptions, int, bool) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return cardOptions{}, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[cardID]
	if state == nil {
		return cardOptions{}, 0, false
	}
	return state.cardOptions(), state.sequence, true
}

func (r *taskCardRegistry) setDurableReferenceChangeHandler(cardID string, handler func()) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return
	}
	r.mu.Lock()
	if state := r.cards[cardID]; state != nil {
		state.recoveryChanged = handler
	}
	r.mu.Unlock()
}

func (r *taskCardRegistry) notifyDurableReferenceChange(cardID string) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return
	}
	r.mu.Lock()
	state := r.cards[cardID]
	var handler func()
	if state != nil {
		handler = state.recoveryChanged
	}
	r.mu.Unlock()
	if handler != nil {
		handler()
	}
}

func (r *taskCardRegistry) updateContentWithSequence(cardID string, content string) (cardOptions, int, bool) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return cardOptions{}, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[cardID]
	if state == nil {
		return cardOptions{}, 0, false
	}
	state.content = content
	state.sequence++
	state.updatedAt = r.nowOrDefault()
	return state.cardOptions(), state.sequence, true
}

func (r *taskCardRegistry) updatePresentationWithSequence(cardID, summary, content string) (cardOptions, int, bool) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return cardOptions{}, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[cardID]
	if state == nil {
		return cardOptions{}, 0, false
	}
	state.summary, state.content = summary, content
	state.sequence++
	state.updatedAt = r.nowOrDefault()
	return state.cardOptions(), state.sequence, true
}

func (r *taskCardRegistry) enableStructuredPresentationWithSequence(cardID, summary, content string) (cardOptions, int, bool) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return cardOptions{}, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[cardID]
	if state == nil {
		return cardOptions{}, 0, false
	}
	state.summary, state.content = summary, content
	state.collapsible = true
	state.expanded = true
	state.sequence++
	state.updatedAt = r.nowOrDefault()
	return state.cardOptions(), state.sequence, true
}

func (r *taskCardRegistry) update(cardID string, status string, content string) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[cardID]
	if state == nil {
		return
	}
	if strings.TrimSpace(status) != "" {
		state.status = normalizeCardStatus(status)
	}
	if strings.TrimSpace(content) != "" {
		state.content = content
	}
	state.updatedAt = r.nowOrDefault()
}

func (r *taskCardRegistry) updateAndSnapshot(cardID string, status string, content string, replaceContent bool) (cardOptions, bool) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return cardOptions{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[cardID]
	if state == nil {
		return cardOptions{}, false
	}
	if strings.TrimSpace(status) != "" {
		normalized := normalizeCardStatus(status)
		state.status = normalized
		if replaceContent && (normalized == cardStatusDone || normalized == cardStatusError || normalized == cardStatusStopped) {
			state.content = strings.TrimSpace(content)
			if state.content == "" {
				state.summary = ""
				state.collapsible = false
				state.expanded = false
			}
		} else if normalized == cardStatusSuperseded && strings.TrimSpace(content) != "" {
			state.content = content
		}
		if normalized == cardStatusDone || normalized == cardStatusError || normalized == cardStatusStopped || normalized == cardStatusSuperseded {
			state.recoveryChanged = nil
		}
	} else if strings.TrimSpace(content) != "" {
		state.content = content
	}
	state.updatedAt = r.nowOrDefault()
	return state.cardOptions(), true
}

func (r *taskCardRegistry) addApproval(cardID string, action parsedCardAction) (cardOptions, bool) {
	opts, _, ok := r.addApprovalWithSequence(cardID, action)
	return opts, ok
}

func (r *taskCardRegistry) addApprovalWithSequence(cardID string, action parsedCardAction) (cardOptions, int, bool) {
	if r == nil || strings.TrimSpace(cardID) == "" {
		return cardOptions{}, 0, false
	}
	r.mu.Lock()
	state := r.cards[cardID]
	if state == nil {
		r.mu.Unlock()
		return cardOptions{}, 0, false
	}
	state.approvals = append(state.approvals, approvalRecordLine(action))
	state.sequence++
	state.updatedAt = r.nowOrDefault()
	opts := state.cardOptions()
	sequence := state.sequence
	recoveryChanged := state.recoveryChanged
	r.mu.Unlock()
	if recoveryChanged != nil {
		recoveryChanged()
	}
	return opts, sequence, true
}

func (r *taskCardRegistry) nextSequence(cardID string, current int) int {
	next := current + 1
	if r == nil || strings.TrimSpace(cardID) == "" {
		return next
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.cards[cardID]
	if state == nil {
		return next
	}
	if state.sequence < current {
		state.sequence = current
	}
	state.sequence++
	state.updatedAt = r.nowOrDefault()
	return state.sequence
}

func (s *taskCardState) cardOptions() cardOptions {
	return cardOptions{
		Status:             s.status,
		Title:              s.title,
		Content:            s.content,
		Summary:            s.summary,
		Approvals:          append([]string(nil), s.approvals...),
		Collapsible:        s.collapsible,
		Expanded:           s.expanded,
		InlineActiveStatus: s.inlineActiveStatus,
		taskCardID:         s.taskCardID,
	}
}

func (r *taskCardRegistry) purgeLocked() {
	now := r.nowOrDefault()
	for cardID, state := range r.cards {
		if now.Sub(state.updatedAt) > taskCardRecordTTL {
			delete(r.cards, cardID)
		}
	}
}

func (r *taskCardRegistry) nowOrDefault() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func approvalRecordLine(action parsedCardAction) string {
	status, _ := approvalHandledStatus(action)
	label := strings.TrimSpace(action.Label)
	if label == "" {
		label = strings.TrimSpace(action.Choice)
	}
	line := status
	if label != "" {
		line += "：" + label
	}
	if summary := strings.TrimSpace(action.Summary); summary != "" {
		line += "\n" + summary
	}
	return line
}
