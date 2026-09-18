package messaging

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// codexResultReplaySelection 属于窗口的一次显式选择，不能用历史 turn 展示记录代替。
type codexResultReplaySelection struct {
	ID              string
	WorkspaceRoot   string
	ThreadID        string
	PresentedTurnID string `json:",omitempty"`
	ObservedTurnID  string `json:",omitempty"`
}

// 初次轻量读取可能没有 turn 基线；后台补查必须认领既有回放，不能再投一次终态。
func (h *Handler) codexReplayOwnsTerminal(snapshot codexFollowerSnapshot, turnID string) bool {
	if turnID == "" {
		return false
	}
	selection := h.ensureCodexSessions().remoteSelectionSnapshot(snapshot.BindingKey, snapshot.Target.ThreadID).Binding.ResultReplay
	if selection.ID == "" || selection.ThreadID != snapshot.Target.ThreadID {
		return false
	}
	if selection.PresentedTurnID == turnID {
		return true
	}
	outbox := h.currentTerminalOutbox()
	if outbox == nil {
		return false
	}
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	for _, entry := range outbox.entries {
		if entry.FollowerBindingKey == snapshot.BindingKey && entry.ReplaySelectionID == selection.ID && entry.ReplayTurnID == turnID {
			return true
		}
	}
	return false
}

func (h *Handler) queueLatestIdleCodexReplay(result codexSessionAcquireResult) codexSessionAcquireResult {
	result.suppressLatestIdleResult = true
	state := latestIdleCodexState(result)
	if result.externalActive || state.Active {
		return result
	}
	if result.syncErr != nil || result.runtimeErr != nil || !state.LastTurnBodyLoaded {
		// 空会话不制造任务结果；读取失败则保留本次选择的重试机会。
		if state.LastTurnID != "" || result.syncErr != nil || result.runtimeErr != nil {
			result.latestIdleResultNotice = "最近结果暂未读取成功，可再次选择当前会话重试"
		}
		return result
	}
	if state.LastTurnID == "" {
		return result
	}
	store := h.ensureCodexSessions()
	binding := store.remoteSelectionSnapshot(result.route.bindingKey, result.route.threadID).Binding
	selection := binding.ResultReplay
	if selection.PresentedTurnID == state.LastTurnID || selection.ObservedTurnID == state.LastTurnID {
		return result
	}
	// 该轮已由实时观察者负责，切换不再创建另一份历史投递。
	if binding.FollowTurnPending || binding.FollowTurnInitialized && binding.FollowTurnID != "" && binding.FollowTurnID != state.LastTurnID {
		return result
	}
	snapshot, ok := store.followerSnapshot(result.route.bindingKey)
	if !ok || selection.ID == "" || selection.ThreadID != result.route.threadID || selection.WorkspaceRoot != result.route.workspaceRoot {
		result.latestIdleResultNotice = "最近结果投递尚未准备好，可再次选择当前会话重试"
		return result
	}
	text := state.LastAgentMessageText
	failed, stopped := false, false
	switch strings.ToLower(strings.TrimSpace(state.LastTurnStatus)) {
	case "completed":
		if strings.TrimSpace(text) == "" {
			text = "最近任务已完成，未记录文本结果"
		}
	case "failed", "error":
		failed = true
		if strings.TrimSpace(text) == "" {
			text = firstNonBlank(state.LastTurnError, "最近任务执行失败，未记录文本结果")
		}
	case "interrupted", "cancelled", "canceled":
		stopped = true
		if strings.TrimSpace(text) == "" {
			text = "最近任务已停止，未记录文本结果"
		}
	default:
		return result
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join([]string{
		"weclaw.codex-result-replay.v1", result.route.bindingKey, selection.ID, state.LastTurnID,
	}, "\x00"))).String()
	draft := terminalOutboxDraft{
		ID: id, IdempotencyKey: id, Route: snapshot.Target.DeliveryRoute, AgentName: snapshot.AgentName,
		ResultTitle: "最近任务结果", RichResult: true, Text: text, Failed: failed, Stopped: stopped,
		ReplaySelectionID: selection.ID, ReplayTurnID: state.LastTurnID,
	}
	terminalDeliveryGuardFromFollower(snapshot).apply(&draft)
	outbox := h.currentTerminalOutbox()
	var err error
	if outbox == nil {
		err = ErrTerminalOutboxUnavailable
	} else {
		_, err = outbox.enqueue(draft)
	}
	if err != nil {
		result.latestIdleResultClaimErr = err
		result.latestIdleResultNotice = "最近结果暂未保存到投递队列，可再次选择当前会话重试"
	}
	return result
}

// commitCodexReplayDelivered 在平台确认成功后、outbox 删除正文前保存回执。
// 保存失败继续使用同一投递键重试，不能提前消费展示机会。
func (h *Handler) commitCodexReplayDelivered(entry *terminalOutboxEntry) error {
	if entry.ReplaySelectionID == "" {
		return nil
	}
	s := h.ensureCodexSessions()
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.bindings[entry.FollowerBindingKey]
	if binding.ResultReplay.ID != entry.ReplaySelectionID || binding.ResultReplay.ThreadID != entry.FollowerThreadID {
		return errCodexRemoteSelectionChanged
	}
	if binding.ResultReplay.PresentedTurnID == entry.ReplayTurnID {
		return nil
	}
	next := cloneCodexSessionBindings(s.bindings)
	binding.ResultReplay.PresentedTurnID = entry.ReplayTurnID
	next[entry.FollowerBindingKey] = binding
	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: next, Archived: sortedCodexArchivedThreadIDs(s.archived),
		Updated: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return fmt.Errorf("保存最近结果投递回执: %w", err)
	}
	s.bindings = next
	return nil
}
