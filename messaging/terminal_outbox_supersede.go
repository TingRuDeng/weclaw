package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func (o *terminalOutbox) reanchorStreamReservation(
	id string,
	newRoute platform.DeliveryRoute,
	newReference platform.DurableStreamReference,
	pending pendingStreamSupersede,
) error {
	if !newRoute.Valid() || strings.TrimSpace(newReference.Kind) == "" || len(newReference.Payload) == 0 || !json.Valid(newReference.Payload) {
		return fmt.Errorf("invalid reanchored stream recovery")
	}
	if pending.NextAttempt.IsZero() {
		pending.NextAttempt = o.now()
	}
	if err := validatePendingStreamSupersede(&pending); err != nil {
		return err
	}

	o.mu.Lock()
	entry := o.entryLocked(id)
	if entry == nil {
		o.mu.Unlock()
		return ErrTerminalOutboxNotFound
	}
	duplicate := false
	for _, candidate := range o.entries {
		if candidate.ID == pending.ID {
			o.mu.Unlock()
			return fmt.Errorf("pending stream supersede id conflicts with terminal outbox entry")
		}
		for _, existing := range candidate.PendingSupersedes {
			if existing.ID != pending.ID {
				continue
			}
			if candidate.ID != id {
				o.mu.Unlock()
				return fmt.Errorf("pending stream supersede id already belongs to another entry")
			}
			if !sameDeliveryRoute(existing.Route, pending.Route) || existing.Checkpoint.Kind != pending.Checkpoint.Kind ||
				string(existing.Checkpoint.Payload) != string(pending.Checkpoint.Payload) {
				o.mu.Unlock()
				return fmt.Errorf("pending stream supersede id conflicts with an existing operation")
			}
			duplicate = true
		}
	}
	committed, err := o.mutateEntryLocked(entry, terminalOutboxTxOptions{
		validate:    true,
		persistWrap: "persist reanchored stream recovery",
	}, func(target *terminalOutboxEntry) error {
		target.Route = newRoute
		target.Stream = cloneDurableStreamReference(&newReference)
		target.ActiveStreamRecovery = true
		target.Checkpoint = nil
		target.CheckpointDelivered = false
		target.TextDelivered = false
		target.NotificationDelivered = false
		target.UpdatedAt = o.now()
		target.NextAttempt = target.UpdatedAt
		target.Attempts = 0
		target.LastError = ""
		target.DeadLetter = false
		target.DeadLetterAt = time.Time{}
		if !duplicate {
			target.PendingSupersedes = append(target.PendingSupersedes, clonePendingStreamSupersede(pending))
		}
		return nil
	})
	o.mu.Unlock()
	if err != nil {
		return err
	}
	if !duplicate {
		o.recordTrace(committed, "task.card_supersede_pending", "pending", "old progress card supersede queued")
	}
	o.signal()
	return nil
}

// detachStreamReservation 原子移除活动卡片的“服务重启即中断”终态恢复，
// 只保留可幂等重试的非终态冻结操作。
func (o *terminalOutbox) detachStreamReservation(id string, pending pendingStreamSupersede) error {
	if pending.NextAttempt.IsZero() {
		pending.NextAttempt = o.now()
	}
	if err := validatePendingStreamSupersede(&pending); err != nil {
		return err
	}
	o.mu.Lock()
	entry := o.entryLocked(id)
	if entry == nil {
		o.mu.Unlock()
		return ErrTerminalOutboxNotFound
	}
	if !terminalOutboxEntryIsActiveStreamRecovery(entry) {
		o.mu.Unlock()
		return fmt.Errorf("terminal outbox reservation is no longer an active stream recovery")
	}
	for _, candidate := range o.entries {
		if candidate.ID == pending.ID {
			o.mu.Unlock()
			return fmt.Errorf("pending stream supersede id conflicts with terminal outbox entry")
		}
		for _, existing := range candidate.PendingSupersedes {
			if existing.ID == pending.ID {
				o.mu.Unlock()
				return fmt.Errorf("pending stream supersede id already exists")
			}
		}
	}
	wasPreparing := o.preparing[id]
	wasFollowerHeld := o.followerHeld[id]
	wasReleaseHeld := o.releaseHeld[id]
	wasReleaseBusy := o.releaseBusy[id]
	delete(o.preparing, id)
	delete(o.followerHeld, id)
	delete(o.releaseHeld, id)
	delete(o.releaseBusy, id)
	committed, err := o.mutateEntryLocked(entry, terminalOutboxTxOptions{
		validate:    true,
		persistWrap: "persist detached stream recovery",
	}, func(target *terminalOutboxEntry) error {
		target.Stream = nil
		target.Checkpoint = nil
		target.Text = ""
		target.Notification = ""
		target.ActiveStreamRecovery = false
		target.CheckpointDelivered = true
		target.TextDelivered = true
		target.NotificationDelivered = true
		target.Attempts = 0
		target.UpdatedAt = o.now()
		target.NextAttempt = target.UpdatedAt
		target.LastError = ""
		target.DeadLetter = false
		target.DeadLetterAt = time.Time{}
		target.PendingSupersedes = append(target.PendingSupersedes, clonePendingStreamSupersede(pending))
		return nil
	})
	if err != nil {
		if wasPreparing {
			o.preparing[id] = true
		}
		if wasFollowerHeld {
			o.followerHeld[id] = true
		}
		if wasReleaseHeld {
			o.releaseHeld[id] = true
		}
		if wasReleaseBusy {
			o.releaseBusy[id] = true
		}
		o.mu.Unlock()
		return err
	}
	o.mu.Unlock()
	o.recordTrace(committed, "task.card_detach_pending", "pending", "released progress card freeze queued")
	o.signal()
	return nil
}

// beginStreamReanchor 暂停同一 reservation 的后台投递，直到调用方完成
// durable 提交后的内存权威切换。该占用不落盘，进程重启后 pending 可立即恢复。
func (o *terminalOutbox) beginStreamReanchor(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.entryLocked(id) == nil {
		return ErrTerminalOutboxNotFound
	}
	if o.processing[id] {
		return fmt.Errorf("terminal outbox reservation is busy")
	}
	o.processing[id] = true
	return nil
}

func (o *terminalOutbox) endStreamReanchor(id string) {
	o.endAttempt(id)
}

type duePendingStreamSupersede struct {
	entryID     string
	pendingID   string
	nextAttempt time.Time
	createdAt   time.Time
}

func (o *terminalOutbox) duePendingStreamSupersedes() []duePendingStreamSupersede {
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	var due []duePendingStreamSupersede
	for _, entry := range o.entries {
		if o.processing[entry.ID] || o.startupHeld[entry.ID] {
			continue
		}
		for _, pending := range entry.PendingSupersedes {
			if pending.DeadLetter || pending.NextAttempt.After(now) {
				continue
			}
			due = append(due, duePendingStreamSupersede{
				entryID: entry.ID, pendingID: pending.ID,
				nextAttempt: pending.NextAttempt, createdAt: entry.CreatedAt,
			})
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		if !due[i].nextAttempt.Equal(due[j].nextAttempt) {
			return due[i].nextAttempt.Before(due[j].nextAttempt)
		}
		if !due[i].createdAt.Equal(due[j].createdAt) {
			return due[i].createdAt.Before(due[j].createdAt)
		}
		if due[i].entryID != due[j].entryID {
			return due[i].entryID < due[j].entryID
		}
		return due[i].pendingID < due[j].pendingID
	})
	return due
}

func (o *terminalOutbox) attemptPendingStreamSupersede(parent context.Context, entryID string, pendingID string) error {
	entry, pending, ok := o.beginPendingStreamSupersedeAttempt(entryID, pendingID)
	if !ok {
		return nil
	}
	defer o.endAttempt(entryID)
	reply, err := o.resolveStageReplier(pending.Route, nil)
	if err == nil {
		durable, supported := optionalDurableSupersedeReplier(reply)
		if !supported {
			err = platform.ErrUnsupported
		} else {
			ctx, cancel := context.WithTimeout(parent, terminalOutboxDeliveryTimeout)
			err = durable.DeliverSupersede(ctx, cloneSupersedeCheckpoint(pending.Checkpoint))
			cancel()
		}
	}
	if err != nil {
		return o.recordPendingStreamSupersedeFailure(entryID, pendingID, err)
	}
	if err := o.completePendingStreamSupersede(entryID, pendingID); err != nil {
		return err
	}
	o.recordTrace(entry, "task.card_superseded", "completed", "old progress card superseded")
	return nil
}

func (o *terminalOutbox) beginPendingStreamSupersedeAttempt(entryID string, pendingID string) (*terminalOutboxEntry, pendingStreamSupersede, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.processing[entryID] || o.startupHeld[entryID] {
		return nil, pendingStreamSupersede{}, false
	}
	entry := o.entryLocked(entryID)
	if entry == nil {
		return nil, pendingStreamSupersede{}, false
	}
	for _, pending := range entry.PendingSupersedes {
		if pending.ID != pendingID || pending.DeadLetter {
			continue
		}
		o.processing[entryID] = true
		return cloneTerminalOutboxEntry(entry), clonePendingStreamSupersede(pending), true
	}
	return nil, pendingStreamSupersede{}, false
}

func (o *terminalOutbox) completePendingStreamSupersede(entryID string, pendingID string) error {
	o.mu.Lock()
	entry := o.entryLocked(entryID)
	if entry == nil {
		o.mu.Unlock()
		return nil
	}
	index := pendingStreamSupersedeIndex(entry, pendingID)
	if index < 0 {
		o.mu.Unlock()
		return nil
	}
	before := cloneTerminalOutboxEntry(entry)
	entry.PendingSupersedes = append(entry.PendingSupersedes[:index], entry.PendingSupersedes[index+1:]...)
	entry.UpdatedAt = o.now()
	removeParent := len(entry.PendingSupersedes) == 0 && !terminalEntryHasWork(entry) && !entry.DeadLetter
	previousEntries := o.entries
	if removeParent {
		remaining := make([]*terminalOutboxEntry, 0, len(o.entries)-1)
		for _, candidate := range o.entries {
			if candidate.ID != entryID {
				remaining = append(remaining, candidate)
			}
		}
		o.entries = remaining
	}
	if err := o.persistLocked(); err != nil {
		if removeParent {
			o.entries = previousEntries
		}
		*entry = *before
		o.mu.Unlock()
		return fmt.Errorf("persist completed stream supersede: %w", err)
	}
	o.mu.Unlock()
	return nil
}

func (o *terminalOutbox) recordPendingStreamSupersedeFailure(entryID string, pendingID string, deliveryErr error) error {
	o.mu.Lock()
	entry := o.entryLocked(entryID)
	if entry == nil {
		o.mu.Unlock()
		return deliveryErr
	}
	index := pendingStreamSupersedeIndex(entry, pendingID)
	if index < 0 {
		o.mu.Unlock()
		return deliveryErr
	}
	deadLetter := false
	clone, err := o.mutateEntryLocked(entry, terminalOutboxTxOptions{
		validate:     true,
		validateWrap: fmt.Sprintf("supersede delivery failed: %v; validate retry state", deliveryErr),
		persistWrap:  fmt.Sprintf("supersede delivery failed: %v; persist retry state", deliveryErr),
	}, func(target *terminalOutboxEntry) error {
		pending := &target.PendingSupersedes[index]
		pending.Attempts++
		target.UpdatedAt = o.now()
		pending.LastError = truncateTerminalOutboxError(deliveryErr)
		if pending.Attempts >= o.attemptLimit() {
			pending.DeadLetter = true
			pending.DeadLetterAt = target.UpdatedAt
			pending.NextAttempt = target.UpdatedAt
		} else {
			pending.NextAttempt = target.UpdatedAt.Add(terminalOutboxBackoff(pending.Attempts))
		}
		deadLetter = pending.DeadLetter
		return nil
	})
	o.mu.Unlock()
	if err != nil {
		return err
	}
	if deadLetter {
		o.recordTrace(clone, "task.card_supersede_dead_letter", "failed", deliveryErr.Error())
	} else {
		o.recordTrace(clone, "task.card_supersede_retry", "failed", deliveryErr.Error())
	}
	return deliveryErr
}

func pendingStreamSupersedeIndex(entry *terminalOutboxEntry, pendingID string) int {
	if entry == nil {
		return -1
	}
	for index := range entry.PendingSupersedes {
		if entry.PendingSupersedes[index].ID == pendingID {
			return index
		}
	}
	return -1
}
