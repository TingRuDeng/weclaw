package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

type codexDesktopStateParams struct {
	ConversationID string                  `json:"conversationId"`
	Change         codexDesktopStateChange `json:"change"`
}

type codexDesktopStateChange struct {
	Type              string              `json:"type"`
	BaseRevision      uint64              `json:"baseRevision"`
	Revision          uint64              `json:"revision"`
	ConversationState map[string]any      `json:"conversationState"`
	Patches           []codexDesktopPatch `json:"patches"`
}

var codexDesktopIgnoredStateBroadcasts = map[string]bool{
	"thread-stream-following-changed": true,
	"thread-read-state-changed":       true,
	"client-status-changed":           true,
	"query-cache-invalidate":          true,
}

// applyEnvelope 校验并分派 thread-stream-state-changed 广播。
func (s *codexDesktopStateStore) applyEnvelope(epoch uint64, envelope codexDesktopEnvelope) (codexDesktopStateUpdate, error) {
	if err := validateCodexDesktopEnvelope(envelope); err != nil {
		return codexDesktopStateUpdate{}, err
	}
	if envelope.Type == codexDesktopEnvelopeBroadcast && codexDesktopIgnoredStateBroadcasts[envelope.Method] {
		return codexDesktopStateUpdate{}, nil
	}
	if envelope.Type == codexDesktopEnvelopeBroadcast && envelope.Method == "thread-queued-followups-changed" {
		var params struct {
			ConversationID string            `json:"conversationId"`
			Messages       []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(envelope.Params, &params); err != nil {
			return codexDesktopStateUpdate{}, fmt.Errorf("解析 Codex Desktop queued follow-ups: %w", err)
		}
		return s.applyQueuedFollowUps(epoch, params.ConversationID, params.Messages)
	}
	if envelope.Type != codexDesktopEnvelopeBroadcast || envelope.Method != "thread-stream-state-changed" {
		return codexDesktopStateUpdate{}, fmt.Errorf("不支持的 Codex Desktop 状态事件 %s/%s", envelope.Type, envelope.Method)
	}
	var params codexDesktopStateParams
	if err := json.Unmarshal(envelope.Params, &params); err != nil {
		return codexDesktopStateUpdate{}, fmt.Errorf("解析 Codex Desktop 状态事件: %w", err)
	}
	return s.applyStateChange(epoch, params)
}

func (s *codexDesktopStateStore) applyQueuedFollowUps(epoch uint64, threadID string, messages []json.RawMessage) (codexDesktopStateUpdate, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return codexDesktopStateUpdate{}, fmt.Errorf("Codex Desktop queued follow-ups 缺少 thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.threads[threadID]
	if exists && epoch < current.ConnectionEpoch {
		return codexDesktopStateUpdate{Snapshot: cloneCodexDesktopSnapshot(current)}, nil
	}
	if pending, ok := s.followUps[threadID]; ok && epoch < pending.epoch {
		return codexDesktopStateUpdate{Snapshot: cloneCodexDesktopSnapshot(current)}, nil
	}
	s.followUps[threadID] = codexDesktopQueuedFollowUps{
		epoch: epoch, messages: cloneCodexDesktopRawMessages(messages), updatedAt: s.now(),
	}
	s.evictOrphanFollowUpsLocked(threadID)
	if !exists || current.ConnectionEpoch != epoch {
		return codexDesktopStateUpdate{Snapshot: cloneCodexDesktopSnapshot(current)}, nil
	}
	current.QueuedFollowUps = cloneCodexDesktopRawMessages(messages)
	current.UpdatedAt = s.now()
	s.threads[threadID] = current
	return codexDesktopStateUpdate{Snapshot: cloneCodexDesktopSnapshot(current), Applied: true}, nil
}

func (s *codexDesktopStateStore) attachQueuedFollowUpsLocked(snapshot *codexDesktopThreadSnapshot) {
	queued, ok := s.followUps[snapshot.ThreadID]
	if !ok {
		return
	}
	if queued.epoch < snapshot.ConnectionEpoch {
		delete(s.followUps, snapshot.ThreadID)
		return
	}
	if queued.epoch == snapshot.ConnectionEpoch {
		snapshot.QueuedFollowUps = cloneCodexDesktopRawMessages(queued.messages)
	}
}

func cloneCodexDesktopRawMessages(messages []json.RawMessage) []json.RawMessage {
	if messages == nil {
		return nil
	}
	cloned := make([]json.RawMessage, len(messages))
	for index, message := range messages {
		cloned[index] = append(json.RawMessage(nil), message...)
	}
	return cloned
}

// applyStateChange 按 change 类型进入 snapshot 或 patches 原子路径。
func (s *codexDesktopStateStore) applyStateChange(epoch uint64, params codexDesktopStateParams) (codexDesktopStateUpdate, error) {
	switch params.Change.Type {
	case "snapshot":
		return s.applySnapshot(codexDesktopSnapshotSpec{
			threadID: params.ConversationID, epoch: epoch, revision: params.Change.Revision,
			raw: params.Change.ConversationState,
		})
	case "patches":
		return s.applyPatchSet(codexDesktopPatchSetSpec{
			threadID: params.ConversationID, epoch: epoch,
			baseRevision: params.Change.BaseRevision, revision: params.Change.Revision,
			patches: params.Change.Patches,
		})
	default:
		return codexDesktopStateUpdate{}, fmt.Errorf("Codex Desktop 状态 change type %q 无效", params.Change.Type)
	}
}
