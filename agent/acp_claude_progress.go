package agent

import "strings"

// claudeACPProgressState 只聚合 Claude 已允许展示的 agent_message_chunk。
// 思考、工具和计划事件仍参与任务执行，但不会直接进入用户进度卡。
type claudeACPProgressState struct {
	messageID       string
	messageText     string
	messageSequence uint64
	emittedIDs      map[string]struct{}
}

func newClaudeACPProgressState() *claudeACPProgressState {
	return &claudeACPProgressState{emittedIDs: make(map[string]struct{})}
}

// progressEvent 在消息边界明确后，原样发送上一条完整的用户可见消息。
// 最后一条消息不会在 prompt 终态强制刷新，由共享终态流程作为最终结果写回完成卡。
func (s *claudeACPProgressState) progressEvent(update *sessionUpdate) (ProgressEvent, bool) {
	if update == nil {
		return ProgressEvent{}, false
	}
	switch update.SessionUpdate {
	case "agent_message_chunk":
		return s.appendMessageChunk(update)
	case "tool_call", "tool_call_update":
		return s.flushMessage()
	default:
		return ProgressEvent{}, false
	}
}

func (s *claudeACPProgressState) appendMessageChunk(update *sessionUpdate) (ProgressEvent, bool) {
	text := extractChunkText(update)
	if text == "" {
		return ProgressEvent{}, false
	}
	messageID := strings.TrimSpace(update.MessageID)
	if messageID != "" {
		if _, emitted := s.emittedIDs[messageID]; emitted {
			return ProgressEvent{}, false
		}
	}

	if s.messageText == "" {
		s.startMessage(messageID, text, update.Sequence)
		return ProgressEvent{}, false
	}
	if s.messageID != "" && messageID != "" && messageID != s.messageID {
		event, ok := s.flushMessage()
		s.startMessage(messageID, text, update.Sequence)
		return event, ok
	}
	if s.messageID == "" && messageID != "" {
		// 兼容旧 ACP runtime 偶发缺失首个 chunk 的 messageId：不臆造边界，
		// 把后续首次出现的稳定 ID 归入当前消息。
		s.messageID = messageID
	}
	s.messageText += text
	s.messageSequence = update.Sequence
	return ProgressEvent{}, false
}

func (s *claudeACPProgressState) startMessage(messageID string, text string, sequence uint64) {
	s.messageID = messageID
	s.messageText = text
	s.messageSequence = sequence
}

func (s *claudeACPProgressState) flushMessage() (ProgressEvent, bool) {
	messageID := s.messageID
	text := strings.TrimSpace(s.messageText)
	sequence := s.messageSequence
	s.messageID = ""
	s.messageText = ""
	s.messageSequence = 0
	if text == "" {
		return ProgressEvent{}, false
	}

	eventID := ""
	if messageID != "" {
		if _, emitted := s.emittedIDs[messageID]; emitted {
			return ProgressEvent{}, false
		}
		s.emittedIDs[messageID] = struct{}{}
		eventID = "agent-message:" + messageID
	}
	return ProgressEvent{
		ID: eventID, Kind: ProgressKindMessage, State: ProgressStateCompleted,
		Sequence: sequence, Text: text,
	}, true
}
