package messaging

import (
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

// recoverReleasedCodexFollowerStreams 修补“解绑墓碑已落盘、卡片冻结尚未落盘”时的崩溃窗口。
// 匹配项先在内存中 hold，准备失败也绝不能退回为服务重启终态。
func (h *Handler) recoverReleasedCodexFollowerStreams(outbox *terminalOutbox) {
	h.recoverReleasedCodexFollowerStreamsForTargets(outbox, h.ensureCodexSessions().releasedFollowerSnapshots())
}

func committedCodexReleaseTargets(targets []codexReleasedFollowerSnapshot) []codexReleasedFollowerSnapshot {
	committed := make([]codexReleasedFollowerSnapshot, 0, len(targets))
	for _, target := range targets {
		if target.Committed {
			committed = append(committed, target)
		}
	}
	return committed
}

func (h *Handler) recoverReleasedCodexFollowerStreamsForTargets(
	outbox *terminalOutbox,
	targets []codexReleasedFollowerSnapshot,
) {
	for _, entry := range outbox.holdReleasedCodexFollowerRecoveries(targets) {
		func() {
			defer outbox.endReleasedRecoveryAttempt(entry.ID)
			reply, ok := outbox.registry.ReplierForRoute(entry.Route)
			if !ok || reply == nil {
				log.Printf("[terminal-outbox] 已解绑 Codex 卡片冻结等待平台恢复 id=%s", entry.ID)
				return
			}
			preparer, ok := optionalDurableStreamDetachPreparer(reply)
			if !ok || entry.Stream == nil {
				log.Printf("[terminal-outbox] 已解绑 Codex 卡片不支持可恢复冻结 id=%s", entry.ID)
				return
			}
			operationID := uuid.NewString()
			notice := "已解除当前窗口的会话绑定；本地 Codex 任务继续运行。"
			checkpoint, err := preparer.PrepareDetachFromReference(*entry.Stream, notice, operationID)
			if err != nil {
				log.Printf("[terminal-outbox] 准备已解绑 Codex 卡片冻结失败 id=%s: %s",
					entry.ID, observability.SanitizeText(err.Error()))
				return
			}
			if err := outbox.detachStreamReservation(entry.ID, pendingStreamSupersede{
				ID: operationID, Route: entry.Route, Checkpoint: checkpoint,
			}); err != nil {
				log.Printf("[terminal-outbox] 持久化已解绑 Codex 卡片冻结失败 id=%s: %s",
					entry.ID, observability.SanitizeText(err.Error()))
			}
		}()
	}
}

// holdCodexFollowerTerminalDeliveries prevents a persisted ready bit from a
// previous process from authorizing delivery before this process re-establishes
// Host authority, history replay and the exact-turn observer watermark.
func (o *terminalOutbox) holdCodexFollowerTerminalDeliveries(targets []codexFollowerSnapshot) {
	if len(targets) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, entry := range o.entries {
		if !terminalEntryHasWork(entry) {
			continue
		}
		for _, target := range targets {
			if !terminalOutboxEntryMatchesCodexFollowerGuard(entry, target) {
				continue
			}
			o.startupHeld[entry.ID] = true
			o.preparing[entry.ID] = true
			break
		}
	}
}

func terminalOutboxEntryMatchesCodexFollowerGuard(entry *terminalOutboxEntry, target codexFollowerSnapshot) bool {
	guard := terminalDeliveryGuardFromEntry(entry)
	return guard.complete() && guard.FollowerBindingKey == target.BindingKey &&
		guard.FollowerRevision == target.Revision &&
		guard.FollowerThreadID == strings.TrimSpace(target.Target.ThreadID) &&
		guard.AuthorizedIdentity == strings.TrimSpace(target.Target.AuthorizedIdentity) &&
		sameDeliveryEndpoint(entry.Route, target.Target.DeliveryRoute)
}

func (o *terminalOutbox) releaseCodexFollowerTerminalDeliveries(target codexFollowerSnapshot) {
	o.mu.Lock()
	changed := false
	for _, entry := range o.entries {
		if !o.startupHeld[entry.ID] || !terminalOutboxEntryMatchesCodexFollowerGuard(entry, target) {
			continue
		}
		delete(o.startupHeld, entry.ID)
		if !o.followerHeld[entry.ID] && !o.releaseHeld[entry.ID] {
			delete(o.preparing, entry.ID)
		}
		changed = true
	}
	o.mu.Unlock()
	if changed {
		o.signal()
	}
}

func (o *terminalOutbox) reconcileCodexFollowerTerminalDeliveryHolds(targets []codexFollowerSnapshot) {
	o.mu.Lock()
	changed := false
	for id := range o.startupHeld {
		entry := o.entryLocked(id)
		matched := false
		for _, target := range targets {
			if terminalOutboxEntryMatchesCodexFollowerGuard(entry, target) {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		delete(o.startupHeld, id)
		if !o.followerHeld[id] && !o.releaseHeld[id] {
			delete(o.preparing, id)
		}
		changed = true
	}
	o.mu.Unlock()
	if changed {
		o.signal()
	}
}

func (o *terminalOutbox) holdReleasedCodexFollowerRecoveries(targets []codexReleasedFollowerSnapshot) []*terminalOutboxEntry {
	if len(targets) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var held []*terminalOutboxEntry
	for _, entry := range o.entries {
		if !terminalOutboxEntryIsActiveStreamRecovery(entry) || entry.Trace == nil {
			continue
		}
		for _, target := range targets {
			if !terminalOutboxEntryMatchesCodexRelease(entry, target) {
				continue
			}
			o.preparing[entry.ID] = true
			o.releaseHeld[entry.ID] = true
			delete(o.followerHeld, entry.ID)
			if o.releaseBusy[entry.ID] {
				break
			}
			o.releaseBusy[entry.ID] = true
			held = append(held, cloneTerminalOutboxEntry(entry))
			break
		}
	}
	return held
}

// holdCodexFollowerRecoveries 在 worker 启动前暂停仍有 durable follower 的活动卡恢复，
// 避免把可重新观察的本地 turn 误报为“服务重启导致停止”。
func (o *terminalOutbox) holdCodexFollowerRecoveries(targets []codexFollowerSnapshot) {
	if len(targets) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, entry := range o.entries {
		if !terminalOutboxEntryIsActiveStreamRecovery(entry) || entry.Trace == nil {
			continue
		}
		for _, target := range targets {
			if !terminalOutboxEntryMatchesCodexFollower(entry, target) {
				continue
			}
			o.preparing[entry.ID] = true
			o.followerHeld[entry.ID] = true
			break
		}
	}
}

func (o *terminalOutbox) heldCodexFollowerRecoveries(target codexFollowerSnapshot) []*terminalOutboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	var held []*terminalOutboxEntry
	for _, entry := range o.entries {
		if !o.followerHeld[entry.ID] || o.releaseHeld[entry.ID] ||
			!terminalOutboxEntryIsActiveStreamRecovery(entry) || entry.Trace == nil {
			continue
		}
		if !terminalOutboxEntryMatchesCodexFollower(entry, target) {
			continue
		}
		held = append(held, cloneTerminalOutboxEntry(entry))
	}
	return held
}

func terminalOutboxEntryMatchesCodexFollower(entry *terminalOutboxEntry, target codexFollowerSnapshot) bool {
	if !terminalOutboxEntryIsActiveStreamRecovery(entry) || entry.Trace == nil || entry.AgentName != target.AgentName ||
		entry.Trace.ConversationID != target.ConversationID ||
		!sameDeliveryEndpoint(entry.Route, target.Target.DeliveryRoute) {
		return false
	}
	if strings.TrimSpace(entry.Trace.ThreadID) == strings.TrimSpace(target.Target.ThreadID) {
		return true
	}
	return strings.TrimSpace(target.RecoveryThreadID) != "" &&
		strings.TrimSpace(target.RecoveryReservationID) != "" &&
		entry.ID == strings.TrimSpace(target.RecoveryReservationID) &&
		strings.TrimSpace(entry.Trace.ThreadID) == strings.TrimSpace(target.RecoveryThreadID)
}

func terminalOutboxEntryMatchesCodexRelease(entry *terminalOutboxEntry, target codexReleasedFollowerSnapshot) bool {
	if !terminalOutboxEntryIsActiveStreamRecovery(entry) || entry.Trace == nil ||
		entry.AgentName != target.AgentName || entry.Trace.ConversationID != target.ConversationID {
		return false
	}
	reservationID := strings.TrimSpace(target.RecoveryReservationID)
	if reservationID != "" && entry.ID != reservationID {
		return false
	}
	threadID := strings.TrimSpace(entry.Trace.ThreadID)
	if threadID == strings.TrimSpace(target.ThreadID) {
		return true
	}
	return reservationID != "" && strings.TrimSpace(target.RecoveryThreadID) != "" &&
		threadID == strings.TrimSpace(target.RecoveryThreadID)
}

func terminalOutboxEntryIsActiveStreamRecovery(entry *terminalOutboxEntry) bool {
	if entry == nil || entry.Stream == nil {
		return false
	}
	if entry.ActiveStreamRecovery {
		return true
	}
	// v1 旧状态没有显式类型；仅精确识别历史活动卡重启草稿。
	return entry.Stopped && entry.Checkpoint == nil && entry.Text == activeStreamRestartText &&
		!entry.CheckpointDelivered && !entry.TextDelivered && !entry.NotificationDelivered
}

func sameDeliveryEndpoint(left platform.DeliveryRoute, right platform.DeliveryRoute) bool {
	return left.Platform == right.Platform &&
		strings.TrimSpace(left.AccountID) == strings.TrimSpace(right.AccountID) &&
		strings.TrimSpace(left.ChatID) == strings.TrimSpace(right.ChatID)
}

func (o *terminalOutbox) releaseHeldCodexFollowerRecovery(id string) {
	o.mu.Lock()
	delete(o.followerHeld, id)
	delete(o.releaseHeld, id)
	delete(o.releaseBusy, id)
	delete(o.preparing, id)
	o.mu.Unlock()
	o.signal()
}

func (o *terminalOutbox) endReleasedRecoveryAttempt(id string) {
	o.mu.Lock()
	delete(o.releaseBusy, id)
	o.mu.Unlock()
}

// reconcileCodexFollowerHolds 释放已经不再对应活动 follower 或解绑意图的内存 hold。
// hold 本身不落盘；若不在每轮快照后清理，路由变更会让恢复条目永久停在 preparing。
func (o *terminalOutbox) reconcileCodexFollowerHolds(
	followers []codexFollowerSnapshot,
	releases []codexReleasedFollowerSnapshot,
) {
	o.mu.Lock()
	changed := false
	heldIDs := make(map[string]struct{}, len(o.followerHeld)+len(o.releaseHeld))
	for id := range o.followerHeld {
		heldIDs[id] = struct{}{}
	}
	for id := range o.releaseHeld {
		heldIDs[id] = struct{}{}
	}
	for id := range heldIDs {
		if o.releaseBusy[id] {
			continue
		}
		entry := o.entryLocked(id)
		followerMatch := false
		releaseMatch := false
		if entry != nil {
			for _, follower := range followers {
				if terminalOutboxEntryMatchesCodexFollower(entry, follower) {
					followerMatch = true
					break
				}
			}
			for _, release := range releases {
				if terminalOutboxEntryMatchesCodexRelease(entry, release) {
					releaseMatch = true
					break
				}
			}
		}
		if releaseMatch {
			if !o.releaseHeld[id] || o.followerHeld[id] || !o.preparing[id] {
				changed = true
			}
			o.releaseHeld[id] = true
			delete(o.followerHeld, id)
			o.preparing[id] = true
			continue
		}
		if followerMatch {
			if o.releaseHeld[id] || !o.followerHeld[id] || !o.preparing[id] {
				changed = true
			}
			delete(o.releaseHeld, id)
			delete(o.releaseBusy, id)
			o.followerHeld[id] = true
			o.preparing[id] = true
			continue
		}
		if o.followerHeld[id] || o.releaseHeld[id] || o.preparing[id] {
			changed = true
		}
		delete(o.followerHeld, id)
		delete(o.releaseHeld, id)
		delete(o.releaseBusy, id)
		delete(o.preparing, id)
	}
	o.mu.Unlock()
	if changed {
		o.signal()
	}
}
