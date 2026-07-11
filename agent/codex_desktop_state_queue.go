package agent

// queuePatchSetLocked 有界保存断档 patch，并确保每个 thread 只触发一次基线请求。
func (s *codexDesktopStateStore) queuePatchSetLocked(spec codexDesktopPatchSetSpec) (codexDesktopStateUpdate, bool) {
	queue := s.queued[spec.threadID]
	queuedCount := countCodexDesktopQueuedPatches(queue)
	withinPatchLimit := queuedCount+len(spec.patches) <= codexDesktopMaxQueuedPatches
	if withinPatchLimit && len(queue) < codexDesktopMaxQueuedPatches {
		queue = append(queue, codexDesktopQueuedPatchSet{
			epoch: spec.epoch, baseRevision: spec.baseRevision, revision: spec.revision,
			patches: cloneCodexDesktopPatches(spec.patches),
		})
		s.queued[spec.threadID] = queue
	}
	previousTarget, requested := s.needsSnapshot[spec.threadID]
	if spec.revision > previousTarget {
		s.needsSnapshot[spec.threadID] = spec.revision
	}
	current := s.threads[spec.threadID]
	return codexDesktopStateUpdate{
		Snapshot: cloneCodexDesktopSnapshot(current), NeedsSnapshot: true,
	}, !requested
}

// countCodexDesktopQueuedPatches 计算队列中的实际 patch 条数。
func countCodexDesktopQueuedPatches(queue []codexDesktopQueuedPatchSet) int {
	total := 0
	for _, patchSet := range queue {
		total += len(patchSet.patches)
	}
	return total
}

// replayQueuedLocked 在 snapshot 后只重放严格连续的同代次 patch。
func (s *codexDesktopStateStore) replayQueuedLocked(threadID string) ([]*codexTurnEvent, error) {
	queue := s.queued[threadID]
	current := s.threads[threadID]
	remaining := queue[:0]
	var events []*codexTurnEvent
	for _, queued := range queue {
		if queued.epoch != current.ConnectionEpoch || queued.revision <= current.Revision {
			continue
		}
		if queued.baseRevision != current.Revision {
			remaining = append(remaining, queued)
			continue
		}
		next, err := applyCodexDesktopPatches(current.Raw, queued.patches)
		if err != nil {
			s.queued[threadID] = append(remaining, queued)
			return events, err
		}
		if err := validateCodexDesktopRawThreadID(threadID, next); err != nil {
			s.queued[threadID] = append(remaining, queued)
			return events, err
		}
		spec := codexDesktopSnapshotSpec{
			threadID: threadID, epoch: queued.epoch, revision: queued.revision, raw: next,
		}
		var projected []*codexTurnEvent
		current, projected = buildCodexDesktopSnapshot(spec, s.now(), &current.projection)
		events = append(events, projected...)
		s.threads[threadID] = current
	}
	s.storeRemainingPatches(threadID, remaining)
	s.evictIdleLocked(threadID)
	return events, nil
}

// storeRemainingPatches 删除空队列，避免为已恢复 thread 保留无效状态。
func (s *codexDesktopStateStore) storeRemainingPatches(threadID string, remaining []codexDesktopQueuedPatchSet) {
	if len(remaining) == 0 {
		delete(s.queued, threadID)
		return
	}
	s.queued[threadID] = remaining
}
