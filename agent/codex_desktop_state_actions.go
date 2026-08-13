package agent

import (
	"context"
	"sort"
	"strings"
)

func codexDesktopApprovalRequestState(snapshot codexDesktopThreadSnapshot, turnID string, requestID string) ApprovalRequestState {
	turnID = strings.TrimSpace(turnID)
	requestID = strings.TrimSpace(requestID)
	if turnID == "" || requestID == "" {
		return ApprovalRequestStateUnknown
	}
	activeTurnID := strings.TrimSpace(snapshot.State.ActiveTurnID)
	if snapshot.State.Active {
		if activeTurnID == "" {
			return ApprovalRequestStateUnknown
		}
		if activeTurnID != turnID {
			return ApprovalRequestStateTurnTerminal
		}
		if _, exists := snapshot.Requests[requestID]; exists {
			return ApprovalRequestStatePending
		}
		return ApprovalRequestStateResolvedExternally
	}
	if _, exists := snapshot.Requests[requestID]; exists || strings.TrimSpace(snapshot.State.LastTurnID) == "" {
		return ApprovalRequestStateUnknown
	}
	return ApprovalRequestStateTurnTerminal
}

// projectPendingActionEventsLocked 只投递首次出现且能映射 responder 的 pending action。
func (s *codexDesktopStateStore) projectPendingActionEventsLocked(snapshot codexDesktopThreadSnapshot) ([]*codexTurnEvent, error) {
	if s.actions == nil || len(snapshot.Requests) == 0 {
		return nil, nil
	}
	seen := s.actionSeen[snapshot.ThreadID]
	if seen == nil {
		seen = make(map[string]bool)
		s.actionSeen[snapshot.ThreadID] = seen
	}
	requestIDs := sortedCodexDesktopRequestIDs(snapshot.Requests)
	var events []*codexTurnEvent
	for _, requestID := range requestIDs {
		if seen[requestID] {
			continue
		}
		event, supported, err := s.projectPendingAction(snapshot.ThreadID, snapshot.Requests[requestID])
		if err != nil {
			return events, err
		}
		if !supported {
			continue
		}
		event.TurnID = firstNonEmpty(event.TurnID, snapshot.State.ActiveTurnID, snapshot.State.LastTurnID)
		if event.Approval != nil && s.approvalStateProbe != nil {
			threadID := snapshot.ThreadID
			turnID := event.TurnID
			requestID := event.Approval.Request.RequestID
			event.Approval.Request.StateProbe = func(ctx context.Context) (ApprovalRequestState, error) {
				return s.approvalStateProbe(ctx, threadID, turnID, requestID)
			}
		}
		s.wrapPendingActionResponder(snapshot.ThreadID, requestID, event)
		seen[requestID] = true
		events = append(events, event)
	}
	return events, nil
}

func (s *codexDesktopStateStore) replayPendingActionEvents(threadID string) []*codexTurnEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.threads[threadID]
	if !ok {
		return nil
	}
	events, err := s.projectPendingActionEventsLocked(snapshot)
	if err != nil {
		return nil
	}
	stampCodexDesktopWatermark(events, snapshot.ConnectionEpoch, snapshot.Revision)
	return events
}

// wrapPendingActionResponder 在发送失败时允许后续 snapshot 再次投递 pending 请求。
func (s *codexDesktopStateStore) wrapPendingActionResponder(threadID string, requestID string, event *codexTurnEvent) {
	if event.Approval != nil {
		respond := event.Approval.Respond
		event.Approval.Respond = func(ctx context.Context, decision string) error {
			err := respond(ctx, decision)
			s.resetPendingActionOnError(threadID, requestID, err)
			return err
		}
	}
	if event.UserInput != nil {
		respond := event.UserInput.Respond
		event.UserInput.Respond = func(ctx context.Context, answers UserInputAnswers) error {
			err := respond(ctx, answers)
			s.resetPendingActionOnError(threadID, requestID, err)
			return err
		}
		event.UserInput.Retry = func() {
			s.resetPendingActionOnError(threadID, requestID, context.Canceled)
		}
	}
}

// resetPendingActionOnError 清除失败投递标记，但保留 snapshot 中的 pending action。
func (s *codexDesktopStateStore) resetPendingActionOnError(threadID string, requestID string, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	delete(s.actionSeen[threadID], requestID)
	s.mu.Unlock()
}

// projectPendingAction 按 method 选择审批或结构化问答投影器。
func (s *codexDesktopStateStore) projectPendingAction(threadID string, action codexDesktopPendingAction) (*codexTurnEvent, bool, error) {
	if _, ok := codexDesktopApprovalMethods[action.Method]; ok {
		event, err := s.actions.approvalEvent(threadID, action)
		return event, true, err
	}
	if action.Method == "item/tool/requestUserInput" {
		event, err := s.actions.userInputEvent(threadID, action)
		return event, true, err
	}
	return nil, false, nil
}

// sortedCodexDesktopRequestIDs 保证同一 snapshot 的交互事件顺序稳定。
func sortedCodexDesktopRequestIDs(requests map[string]codexDesktopPendingAction) []string {
	ids := make([]string, 0, len(requests))
	for requestID := range requests {
		ids = append(ids, requestID)
	}
	sort.Strings(ids)
	return ids
}
