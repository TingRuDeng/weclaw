package agent

import (
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
	UpdatedAt       time.Time
	projection      codexDesktopProjectionState
}

type codexDesktopStateUpdate struct {
	Snapshot      codexDesktopThreadSnapshot
	Events        []*codexTurnEvent
	Applied       bool
	NeedsSnapshot bool
}

type codexDesktopStateOptions struct {
	now             func() time.Time
	requestSnapshot func(string)
	actions         *codexDesktopActions
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

type codexDesktopStateStore struct {
	mu              sync.Mutex
	threads         map[string]codexDesktopThreadSnapshot
	queued          map[string][]codexDesktopQueuedPatchSet
	needsSnapshot   map[string]uint64
	now             func() time.Time
	requestSnapshot func(string)
	actions         *codexDesktopActions
	actionSeen      map[string]map[string]bool
}

// newCodexDesktopStateStore 创建 revision 严格递增的 Desktop 状态缓存。
func newCodexDesktopStateStore(options codexDesktopStateOptions) *codexDesktopStateStore {
	if options.now == nil {
		options.now = time.Now
	}
	return &codexDesktopStateStore{
		threads:       make(map[string]codexDesktopThreadSnapshot),
		queued:        make(map[string][]codexDesktopQueuedPatchSet),
		needsSnapshot: make(map[string]uint64), now: options.now,
		requestSnapshot: options.requestSnapshot,
		actions:         options.actions, actionSeen: make(map[string]map[string]bool),
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
	s.threads[spec.threadID] = snapshot
	replayed, replayErr := s.replayQueuedLocked(spec.threadID)
	events = append(events, replayed...)
	snapshot = s.threads[spec.threadID]
	actionEvents, actionErr := s.projectPendingActionEventsLocked(snapshot)
	events = append(events, actionEvents...)
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
	s.threads[spec.threadID] = snapshot
	actionEvents, actionErr := s.projectPendingActionEventsLocked(snapshot)
	events = append(events, actionEvents...)
	s.evictIdleLocked(spec.threadID)
	s.mu.Unlock()
	return codexDesktopStateUpdate{
		Snapshot: cloneCodexDesktopSnapshot(snapshot), Events: events, Applied: true,
	}, actionErr
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
		delete(s.needsSnapshot, candidate)
		delete(s.actionSeen, candidate)
	}
}
