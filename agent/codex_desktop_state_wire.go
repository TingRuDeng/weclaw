package agent

import (
	"encoding/json"
	"fmt"
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
	"thread-read-state-changed":       true,
	"thread-queued-followups-changed": true,
}

// applyEnvelope 校验并分派 thread-stream-state-changed 广播。
func (s *codexDesktopStateStore) applyEnvelope(epoch uint64, envelope codexDesktopEnvelope) (codexDesktopStateUpdate, error) {
	if err := validateCodexDesktopEnvelope(envelope); err != nil {
		return codexDesktopStateUpdate{}, err
	}
	if envelope.Type == codexDesktopEnvelopeBroadcast && codexDesktopIgnoredStateBroadcasts[envelope.Method] {
		return codexDesktopStateUpdate{}, nil
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
