package agent

import (
	"context"
	"fmt"
)

// waitForRevision 等待目标连接代次的状态缓存达到 Desktop 已确认发送的 revision。
func (s *codexDesktopStateStore) waitForRevision(ctx context.Context, threadID string, epoch uint64, revision uint64) error {
	for {
		s.mu.Lock()
		snapshot, found := s.threads[threadID]
		if found && snapshot.ConnectionEpoch == epoch && snapshot.Revision >= revision {
			s.mu.Unlock()
			return nil
		}
		wake := s.revisionWake[threadID]
		if wake == nil {
			wake = make(chan struct{})
			s.revisionWake[threadID] = wake
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 Codex Desktop thread %s revision %d: %w", threadID, revision, ctx.Err())
		case <-wake:
		}
	}
}

// signalRevisionLocked 唤醒等待该 thread 状态推进的调用方。
func (s *codexDesktopStateStore) signalRevisionLocked(threadID string) {
	wake := s.revisionWake[threadID]
	if wake == nil {
		return
	}
	delete(s.revisionWake, threadID)
	close(wake)
}
