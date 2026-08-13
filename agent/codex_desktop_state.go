package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	codexDesktopMaxThreads       = 512
	codexDesktopMaxQueuedPatches = 300
)

type codexDesktopPendingAction struct {
	ID     string
	Method string
	Params map[string]any
}

type codexDesktopThreadSnapshot struct {
	ThreadID        string
	ConnectionEpoch uint64
	Revision        uint64
	Raw             map[string]any
	State           CodexThreadState
	Requests        map[string]codexDesktopPendingAction
	QueuedFollowUps []json.RawMessage
	UpdatedAt       time.Time
	projection      codexDesktopProjectionState
}

type codexDesktopStateUpdate struct {
	Snapshot      codexDesktopThreadSnapshot
	Events        []*codexTurnEvent
	Applied       bool
	NeedsSnapshot bool
}

type codexDesktopReplayBatch struct {
	Epoch    uint64
	Revision uint64
	Events   []*codexTurnEvent
}

type codexDesktopStateOptions struct {
	now                func() time.Time
	requestSnapshot    func(string)
	approvalStateProbe func(context.Context, string, string, string) (ApprovalRequestState, error)
	actions            *codexDesktopActions
}

type codexDesktopSnapshotSpec struct {
	threadID string
	epoch    uint64
	revision uint64
	raw      map[string]any
}

type codexDesktopPatchSetSpec struct {
	threadID            string
	epoch, baseRevision uint64
	revision            uint64
	patches             []codexDesktopPatch
}

type codexDesktopQueuedPatchSet struct {
	epoch, baseRevision, revision uint64
	patches                       []codexDesktopPatch
}

type codexDesktopQueuedFollowUps struct {
	epoch     uint64
	messages  []json.RawMessage
	updatedAt time.Time
}

type codexDesktopStateStore struct {
	mu                 sync.Mutex
	threads            map[string]codexDesktopThreadSnapshot
	revisionWake       map[string]chan struct{}
	queued             map[string][]codexDesktopQueuedPatchSet
	followUps          map[string]codexDesktopQueuedFollowUps
	needsSnapshot      map[string]uint64
	now                func() time.Time
	requestSnapshot    func(string)
	approvalStateProbe func(context.Context, string, string, string) (ApprovalRequestState, error)
	actions            *codexDesktopActions
	actionSeen         map[string]map[string]bool
}

// newCodexDesktopStateStore 创建 revision 严格递增的 Desktop 状态缓存。
func newCodexDesktopStateStore(options codexDesktopStateOptions) *codexDesktopStateStore {
	if options.now == nil {
		options.now = time.Now
	}
	return &codexDesktopStateStore{
		threads:       make(map[string]codexDesktopThreadSnapshot),
		revisionWake:  make(map[string]chan struct{}),
		queued:        make(map[string][]codexDesktopQueuedPatchSet),
		followUps:     make(map[string]codexDesktopQueuedFollowUps),
		needsSnapshot: make(map[string]uint64), now: options.now,
		requestSnapshot:    options.requestSnapshot,
		approvalStateProbe: options.approvalStateProbe,
		actions:            options.actions, actionSeen: make(map[string]map[string]bool),
	}
}

// applySnapshot 原子替换完整历史，并按 revision 判断能否恢复 live 状态。
func (s *codexDesktopStateStore) applySnapshot(spec codexDesktopSnapshotSpec) (codexDesktopStateUpdate, error) {
	spec.threadID = strings.TrimSpace(spec.threadID)
	if spec.threadID == "" || spec.raw == nil {
		return codexDesktopStateUpdate{}, fmt.Errorf("Codex Desktop snapshot 缺少 thread 或 state")
	}
	if err := validateCodexDesktopRawThreadID(spec.threadID, spec.raw); err != nil {
		return codexDesktopStateUpdate{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.threads[spec.threadID]
	if exists && spec.epoch < current.ConnectionEpoch {
		return codexDesktopStateUpdate{Snapshot: cloneCodexDesktopSnapshot(current)}, nil
	}
	if exists && current.ConnectionEpoch == spec.epoch && spec.revision <= current.Revision {
		return codexDesktopStateUpdate{Snapshot: cloneCodexDesktopSnapshot(current)}, nil
	}
	previous := codexDesktopProjectionPointer(current, exists && current.ConnectionEpoch == spec.epoch)
	snapshot, events := buildCodexDesktopSnapshot(spec, s.now(), previous)
	s.attachQueuedFollowUpsLocked(&snapshot)
	s.threads[spec.threadID] = snapshot
	replayed, replayErr := s.replayQueuedLocked(spec.threadID)
	events = append(events, replayed...)
	snapshot = s.threads[spec.threadID]
	s.signalRevisionLocked(spec.threadID)
	actionEvents, actionErr := s.projectPendingActionEventsLocked(snapshot)
	events = append(events, actionEvents...)
	stampCodexDesktopWatermark(events, snapshot.ConnectionEpoch, snapshot.Revision)
	target := s.needsSnapshot[spec.threadID]
	needsSnapshot := target > snapshot.Revision
	if !needsSnapshot {
		delete(s.needsSnapshot, spec.threadID)
	}
	return codexDesktopStateUpdate{
		Snapshot: cloneCodexDesktopSnapshot(snapshot), Events: events,
		Applied: true, NeedsSnapshot: needsSnapshot,
	}, errors.Join(replayErr, actionErr)
}

// applyPatchSet 只接受与当前 revision 连续的 patch；其余进入有界等待队列。
func (s *codexDesktopStateStore) applyPatchSet(spec codexDesktopPatchSetSpec) (codexDesktopStateUpdate, error) {
	spec.threadID = strings.TrimSpace(spec.threadID)
	if spec.threadID == "" {
		return codexDesktopStateUpdate{}, fmt.Errorf("Codex Desktop patches 缺少 thread")
	}
	s.mu.Lock()
	current, exists := s.threads[spec.threadID]
	if exists && spec.epoch < current.ConnectionEpoch {
		s.mu.Unlock()
		return codexDesktopStateUpdate{Snapshot: cloneCodexDesktopSnapshot(current)}, nil
	}
	if exists && current.ConnectionEpoch == spec.epoch && spec.revision <= current.Revision {
		s.mu.Unlock()
		return codexDesktopStateUpdate{Snapshot: cloneCodexDesktopSnapshot(current)}, nil
	}
	if !exists || current.ConnectionEpoch != spec.epoch || spec.baseRevision != current.Revision {
		update, shouldRequest := s.queuePatchSetLocked(spec)
		s.mu.Unlock()
		if shouldRequest && s.requestSnapshot != nil {
			s.requestSnapshot(spec.threadID)
		}
		return update, nil
	}
	next, err := applyCodexDesktopPatches(current.Raw, spec.patches)
	if err != nil {
		s.mu.Unlock()
		return codexDesktopStateUpdate{}, err
	}
	if err := validateCodexDesktopRawThreadID(spec.threadID, next); err != nil {
		s.mu.Unlock()
		return codexDesktopStateUpdate{}, err
	}
	snapshotSpec := codexDesktopSnapshotSpec{
		threadID: spec.threadID, epoch: spec.epoch, revision: spec.revision, raw: next,
	}
	snapshot, events := buildCodexDesktopSnapshot(snapshotSpec, s.now(), &current.projection)
	s.attachQueuedFollowUpsLocked(&snapshot)
	s.threads[spec.threadID] = snapshot
	s.signalRevisionLocked(spec.threadID)
	actionEvents, actionErr := s.projectPendingActionEventsLocked(snapshot)
	events = append(events, actionEvents...)
	stampCodexDesktopWatermark(events, snapshot.ConnectionEpoch, snapshot.Revision)
	s.evictIdleLocked(spec.threadID)
	s.mu.Unlock()
	return codexDesktopStateUpdate{
		Snapshot: cloneCodexDesktopSnapshot(snapshot), Events: events, Applied: true,
	}, actionErr
}

// replayActiveTurnBatch 在同一 state 锁内取得 revision、可见历史与尚未
// 被其他消费者取得的交互，避免水位和审批来自不同快照。
func (s *codexDesktopStateStore) replayActiveTurnBatch(threadID string) codexDesktopReplayBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.threads[threadID]
	if !ok || !snapshot.State.Active || strings.TrimSpace(snapshot.State.ActiveTurnID) == "" {
		return codexDesktopReplayBatch{}
	}
	turn, ok := snapshot.projection.turns[snapshot.State.ActiveTurnID]
	var events []*codexTurnEvent
	if ok {
		events = projectCodexDesktopItems(turn.id, nil, turn)
	}
	actionEvents, err := s.projectPendingActionEventsLocked(snapshot)
	if err == nil {
		events = append(events, actionEvents...)
	}
	stampCodexDesktopWatermark(events, snapshot.ConnectionEpoch, snapshot.Revision)
	return codexDesktopReplayBatch{Epoch: snapshot.ConnectionEpoch, Revision: snapshot.Revision, Events: events}
}

func (s *codexDesktopStateStore) activeWatchSnapshot(threadID string) (CodexThreadState, codexDesktopReplayBatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.threads[threadID]
	if !ok {
		return CodexThreadState{}, codexDesktopReplayBatch{}, false
	}
	state := snapshot.State
	batch := codexDesktopReplayBatch{Epoch: snapshot.ConnectionEpoch, Revision: snapshot.Revision}
	if !state.Active || strings.TrimSpace(state.ActiveTurnID) == "" {
		return state, batch, true
	}
	if turn, exists := snapshot.projection.turns[state.ActiveTurnID]; exists {
		batch.Events = projectCodexDesktopItems(turn.id, nil, turn)
	}
	actionEvents, err := s.projectPendingActionEventsLocked(snapshot)
	if err == nil {
		batch.Events = append(batch.Events, actionEvents...)
	}
	stampCodexDesktopWatermark(batch.Events, snapshot.ConnectionEpoch, snapshot.Revision)
	return state, batch, true
}

// awaitingFinalAnswer 表示 Desktop 已把 turn 标记为 completed，但投影器仍在
// 等待后续 history 中的 final_answer。watcher 不得在主动刷新屏障前结算空结果。
func (s *codexDesktopStateStore) awaitingFinalAnswer(threadID string, turnID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.threads[strings.TrimSpace(threadID)]
	if !ok {
		return false
	}
	turnID = firstNonEmpty(strings.TrimSpace(turnID), strings.TrimSpace(snapshot.State.LastTurnID))
	return turnID != "" && snapshot.projection.terminalCandidates[turnID]
}

func stampCodexDesktopWatermark(events []*codexTurnEvent, epoch uint64, revision uint64) {
	for _, event := range events {
		if event != nil {
			event.DesktopEpoch = epoch
			event.DesktopRevision = revision
		}
	}
}

// snapshot 返回私有深拷贝，调用者不能修改缓存基线。
func (s *codexDesktopStateStore) snapshot(threadID string) (codexDesktopThreadSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.threads[threadID]
	return cloneCodexDesktopSnapshot(snapshot), ok
}

// threadCount 返回当前缓存线程数，仅用于容量验证和运行时诊断。
func (s *codexDesktopStateStore) threadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.threads)
}

// buildCodexDesktopSnapshot 从私有 raw 副本构建状态、请求和增量事件。
func buildCodexDesktopSnapshot(spec codexDesktopSnapshotSpec, updatedAt time.Time, previous *codexDesktopProjectionState) (codexDesktopThreadSnapshot, []*codexTurnEvent) {
	cloned := cloneCodexDesktopJSON(spec.raw).(map[string]any)
	state, requests, projection, events := projectCodexDesktopSnapshot(spec.threadID, cloned, previous)
	return codexDesktopThreadSnapshot{
		ThreadID: spec.threadID, ConnectionEpoch: spec.epoch, Revision: spec.revision, Raw: cloned,
		State: state, Requests: requests, UpdatedAt: updatedAt, projection: projection,
	}, events
}

// cloneCodexDesktopSnapshot 深拷贝所有可变字段后再暴露缓存结果。
func cloneCodexDesktopSnapshot(snapshot codexDesktopThreadSnapshot) codexDesktopThreadSnapshot {
	if snapshot.Raw != nil {
		snapshot.Raw = cloneCodexDesktopJSON(snapshot.Raw).(map[string]any)
	}
	requests := make(map[string]codexDesktopPendingAction, len(snapshot.Requests))
	for key, request := range snapshot.Requests {
		if request.Params != nil {
			request.Params = cloneCodexDesktopJSON(request.Params).(map[string]any)
		}
		requests[key] = request
	}
	snapshot.Requests = requests
	snapshot.QueuedFollowUps = cloneCodexDesktopRawMessages(snapshot.QueuedFollowUps)
	snapshot.projection = cloneCodexDesktopProjection(snapshot.projection)
	return snapshot
}

// validateCodexDesktopRawThreadID 防止损坏状态跨 thread 覆盖缓存。
func validateCodexDesktopRawThreadID(threadID string, raw map[string]any) error {
	rawThreadID := codexDesktopString(raw["id"])
	if rawThreadID == "" || rawThreadID != threadID {
		return fmt.Errorf("Codex Desktop conversationId %q 与 state.id %q 不一致", threadID, rawThreadID)
	}
	return nil
}

// evictIdleLocked 超限时只淘汰最旧且无 active turn、无待处理请求的 thread。
func (s *codexDesktopStateStore) evictIdleLocked(currentThreadID string) {
	for len(s.threads) > codexDesktopMaxThreads {
		candidate := ""
		for threadID, snapshot := range s.threads {
			if threadID == currentThreadID || snapshot.State.Active || len(snapshot.Requests) > 0 {
				continue
			}
			if candidate == "" || snapshot.UpdatedAt.Before(s.threads[candidate].UpdatedAt) {
				candidate = threadID
			}
		}
		if candidate == "" {
			return
		}
		delete(s.threads, candidate)
		delete(s.queued, candidate)
		delete(s.followUps, candidate)
		delete(s.needsSnapshot, candidate)
		delete(s.actionSeen, candidate)
	}
}

// evictOrphanFollowUpsLocked keeps pre-snapshot Desktop draft broadcasts bounded.
func (s *codexDesktopStateStore) evictOrphanFollowUpsLocked(currentThreadID string) {
	for {
		orphanCount := 0
		candidate := ""
		for threadID, followUps := range s.followUps {
			if _, known := s.threads[threadID]; known {
				continue
			}
			orphanCount++
			if threadID == currentThreadID {
				continue
			}
			if candidate == "" || followUps.updatedAt.Before(s.followUps[candidate].updatedAt) ||
				(followUps.updatedAt.Equal(s.followUps[candidate].updatedAt) && threadID < candidate) {
				candidate = threadID
			}
		}
		if orphanCount <= codexDesktopMaxThreads || candidate == "" {
			return
		}
		delete(s.followUps, candidate)
	}
}
