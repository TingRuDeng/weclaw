package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

type terminalOutboxTxOptions struct {
	notFoundErr  error
	validate     bool
	validateWrap string
	persistWrap  string
	signal       bool
}

// mutateEntry 封装单条记录的加锁、快照、变更、可选校验、原子落盘与失败回滚模板。
func (o *terminalOutbox) mutateEntry(
	id string,
	opts terminalOutboxTxOptions,
	mutate func(entry *terminalOutboxEntry) error,
) (*terminalOutboxEntry, error) {
	o.mu.Lock()
	entry := o.entryLocked(id)
	if entry == nil {
		o.mu.Unlock()
		return nil, opts.notFoundErr
	}
	committed, err := o.mutateEntryLocked(entry, opts, mutate)
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if opts.signal {
		o.signal()
	}
	return committed, nil
}

// mutateEntryLocked 在已持锁的前提下原子更新单条记录；任何校验或落盘错误都会回滚至修改前快照。
func (o *terminalOutbox) mutateEntryLocked(
	entry *terminalOutboxEntry,
	opts terminalOutboxTxOptions,
	mutate func(entry *terminalOutboxEntry) error,
) (*terminalOutboxEntry, error) {
	before := cloneTerminalOutboxEntry(entry)
	if err := mutate(entry); err != nil {
		*entry = *before
		return nil, err
	}
	if opts.validate {
		if err := validateTerminalOutboxEntry(entry); err != nil {
			*entry = *before
			if opts.validateWrap != "" {
				return nil, fmt.Errorf("%s: %w", opts.validateWrap, err)
			}
			return nil, err
		}
	}
	if err := o.persistLocked(); err != nil {
		*entry = *before
		if opts.persistWrap != "" {
			return nil, fmt.Errorf("%s: %w", opts.persistWrap, err)
		}
		return nil, err
	}
	return cloneTerminalOutboxEntry(entry), nil
}

// removeEntryLocked 原子删除指定索引条目并在落盘失败时恢复原切片。
func (o *terminalOutbox) removeEntryLocked(index int) error {
	previous := o.entries
	remaining := make([]*terminalOutboxEntry, 0, len(o.entries)-1)
	remaining = append(remaining, o.entries[:index]...)
	remaining = append(remaining, o.entries[index+1:]...)
	o.entries = remaining
	if err := o.persistLocked(); err != nil {
		o.entries = previous
		return err
	}
	return nil
}

func (o *terminalOutbox) enqueueAndAttempt(ctx context.Context, draft terminalOutboxDraft, preferred platform.Replier) error {
	entry, err := o.enqueue(draft)
	if err != nil {
		return err
	}
	if err := o.attempt(ctx, entry.ID, preferred); err != nil {
		log.Printf("[terminal-outbox] delivery pending id=%s platform=%s: %s",
			entry.ID, entry.Route.Platform, observability.SanitizeText(err.Error()))
	}
	o.signal()
	return nil
}

func (o *terminalOutbox) enqueue(draft terminalOutboxDraft) (*terminalOutboxEntry, error) {
	return o.enqueueWithState(draft, false)
}

// reserve 先持久化可恢复文本，但在当前进程完成 checkpoint 替换前不允许 worker 投递。
// preparing 只保存在内存中；若进程退出，重启后的 outbox 会立即投递磁盘上的恢复文本。
func (o *terminalOutbox) reserve(draft terminalOutboxDraft) (*terminalOutboxEntry, error) {
	return o.enqueueWithState(draft, true)
}

func (o *terminalOutbox) enqueueWithState(draft terminalOutboxDraft, preparing bool) (*terminalOutboxEntry, error) {
	if !draft.Route.Valid() {
		return nil, fmt.Errorf("invalid terminal delivery route")
	}
	if draft.Stream == nil && draft.Checkpoint == nil && strings.TrimSpace(draft.Text) == "" && strings.TrimSpace(draft.Notification) == "" {
		return nil, fmt.Errorf("terminal delivery has no payload")
	}
	now := o.now()
	entryID := strings.TrimSpace(draft.ID)
	if entryID == "" {
		entryID = uuid.NewString()
	}
	entry := &terminalOutboxEntry{
		ID: entryID, Route: draft.Route,
		ReplaySelectionID: draft.ReplaySelectionID, ReplayTurnID: draft.ReplayTurnID,
		AgentName: strings.TrimSpace(draft.AgentName), Failed: draft.Failed, Stopped: draft.Stopped,
		AuthorizedIdentity: strings.TrimSpace(draft.AuthorizedIdentity),
		FollowerBindingKey: strings.TrimSpace(draft.FollowerBindingKey),
		FollowerRevision:   draft.FollowerRevision, FollowerThreadID: strings.TrimSpace(draft.FollowerThreadID),
		Stream: cloneDurableStreamReference(draft.Stream), Checkpoint: draft.Checkpoint,
		ResultTitle: strings.TrimSpace(draft.ResultTitle), RichResult: draft.RichResult,
		Text: draft.Text, Notification: draft.Notification,
		IdempotencyKey:       strings.TrimSpace(draft.IdempotencyKey),
		ActiveStreamRecovery: draft.ActiveStreamRecovery,
		CreatedAt:            now, UpdatedAt: now, NextAttempt: now,
	}
	if strings.TrimSpace(draft.Trace.TraceID) != "" {
		trace := draft.Trace
		trace.RouteKey = ""
		entry.Trace = &trace
	}
	if err := validateTerminalOutboxEntry(entry); err != nil {
		return nil, err
	}
	o.mu.Lock()
	if existing := o.entryLocked(entry.ID); existing != nil {
		if !sameTerminalOutboxPayload(existing, entry) {
			o.mu.Unlock()
			return nil, fmt.Errorf("terminal outbox id conflicts with an existing delivery")
		}
		var clone *terminalOutboxEntry
		if !sameDeliveryRoute(existing.Route, entry.Route) {
			guard := terminalDeliveryGuardFromEntry(existing)
			if !guard.complete() || !sameDeliveryEndpoint(existing.Route, entry.Route) ||
				o.preparing[entry.ID] || o.processing[entry.ID] {
				o.mu.Unlock()
				return nil, fmt.Errorf("terminal outbox id conflicts with an existing delivery")
			}
			var err error
			clone, err = o.mutateEntryLocked(existing, terminalOutboxTxOptions{
				persistWrap: "persist reanchored terminal delivery",
			}, func(target *terminalOutboxEntry) error {
				target.Route = entry.Route
				target.Attempts = 0
				target.LastError = ""
				target.DeadLetter = false
				target.DeadLetterAt = time.Time{}
				target.UpdatedAt = now
				target.NextAttempt = now
				return nil
			})
			if err != nil {
				o.mu.Unlock()
				return nil, err
			}
		} else {
			clone = cloneTerminalOutboxEntry(existing)
		}
		o.mu.Unlock()
		o.signal()
		return clone, nil
	}
	evictedIndex := -1
	var evicted *terminalOutboxEntry
	if len(o.entries) >= o.entryLimit() {
		evictedIndex = o.oldestEvictableDeadLetterLocked()
		if evictedIndex < 0 {
			o.mu.Unlock()
			return nil, fmt.Errorf("terminal outbox capacity exceeded")
		}
		evicted = o.entries[evictedIndex]
		o.entries = append(o.entries[:evictedIndex], o.entries[evictedIndex+1:]...)
	}
	o.entries = append(o.entries, entry)
	if preparing {
		o.preparing[entry.ID] = true
	}
	if err := o.persistLocked(); err != nil {
		o.entries = o.entries[:len(o.entries)-1]
		if evicted != nil {
			o.entries = append(o.entries, nil)
			copy(o.entries[evictedIndex+1:], o.entries[evictedIndex:])
			o.entries[evictedIndex] = evicted
		}
		delete(o.preparing, entry.ID)
		o.mu.Unlock()
		return nil, fmt.Errorf("persist terminal delivery: %w", err)
	}
	clone := cloneTerminalOutboxEntry(entry)
	o.mu.Unlock()
	if evicted != nil {
		log.Printf("[terminal-outbox] evicted oldest dead letter id=%s to reserve new terminal delivery", evicted.ID)
	}
	o.recordTrace(clone, "terminal.outbox.enqueued", "pending", "terminal delivery queued")
	return clone, nil
}

func sameTerminalOutboxPayload(existing *terminalOutboxEntry, candidate *terminalOutboxEntry) bool {
	if existing == nil || candidate == nil {
		return existing == nil && candidate == nil
	}
	return existing.ID == candidate.ID &&
		existing.ReplaySelectionID == candidate.ReplaySelectionID && existing.ReplayTurnID == candidate.ReplayTurnID &&
		existing.AgentName == candidate.AgentName && existing.Failed == candidate.Failed &&
		existing.AuthorizedIdentity == candidate.AuthorizedIdentity &&
		existing.FollowerBindingKey == candidate.FollowerBindingKey &&
		existing.FollowerRevision == candidate.FollowerRevision && existing.FollowerThreadID == candidate.FollowerThreadID &&
		existing.Stopped == candidate.Stopped && existing.ResultTitle == candidate.ResultTitle &&
		existing.RichResult == candidate.RichResult && existing.Text == candidate.Text &&
		existing.Notification == candidate.Notification && existing.IdempotencyKey == candidate.IdempotencyKey && existing.Stream == nil &&
		candidate.Stream == nil && existing.Checkpoint == nil && candidate.Checkpoint == nil &&
		!existing.ActiveStreamRecovery && !candidate.ActiveStreamRecovery
}

func (o *terminalOutbox) entryLimit() int {
	if o.maxEntries > 0 {
		return o.maxEntries
	}
	return terminalOutboxMaxEntries
}

func (o *terminalOutbox) attemptLimit() int {
	if o.maxAttempts > 0 {
		return o.maxAttempts
	}
	return terminalOutboxMaxAttempts
}

func (o *terminalOutbox) oldestEvictableDeadLetterLocked() int {
	index := -1
	for candidate, entry := range o.entries {
		if !entry.DeadLetter || o.preparing[entry.ID] || o.processing[entry.ID] {
			continue
		}
		if index < 0 ||
			entry.DeadLetterAt.Before(o.entries[index].DeadLetterAt) ||
			(entry.DeadLetterAt.Equal(o.entries[index].DeadLetterAt) && entry.CreatedAt.Before(o.entries[index].CreatedAt)) {
			index = candidate
		}
	}
	return index
}

// stageReservationResult 在冻结流之前先持久化真实终态结果；进程若在 checkpoint 准备期间退出，
// 重启后的 worker 仍能分别恢复卡片终态和最终文本。
func (o *terminalOutbox) stageReservationResult(id string, draft terminalOutboxDraft) error {
	if !draft.Route.Valid() || strings.TrimSpace(draft.Text) == "" {
		return fmt.Errorf("invalid staged terminal result")
	}
	_, err := o.mutateEntry(id, terminalOutboxTxOptions{
		notFoundErr: ErrTerminalOutboxNotFound,
		validate:    true,
		persistWrap: "persist staged terminal result",
	}, func(entry *terminalOutboxEntry) error {
		entry.Route = draft.Route
		entry.AgentName = strings.TrimSpace(draft.AgentName)
		if err := mergeTerminalDeliveryGuard(entry, draft); err != nil {
			return err
		}
		entry.Failed = draft.Failed
		entry.Stopped = draft.Stopped
		entry.ResultTitle = strings.TrimSpace(draft.ResultTitle)
		entry.RichResult = draft.RichResult
		entry.Text = draft.Text
		entry.Notification = draft.Notification
		entry.IdempotencyKey = strings.TrimSpace(draft.IdempotencyKey)
		entry.ActiveStreamRecovery = false
		entry.CheckpointDelivered = false
		entry.TextDelivered = false
		entry.NotificationDelivered = false
		entry.UpdatedAt = o.now()
		entry.NextAttempt = entry.UpdatedAt
		entry.LastError = ""
		entry.DeadLetter = false
		entry.DeadLetterAt = time.Time{}
		if strings.TrimSpace(draft.Trace.TraceID) != "" {
			trace := draft.Trace
			trace.RouteKey = ""
			entry.Trace = &trace
		}
		return nil
	})
	return err
}

// commitReservation 用 checkpoint 终态替换恢复文本；持久化失败时保留原草稿并允许重试投递。
func (o *terminalOutbox) commitReservation(id string, draft terminalOutboxDraft) error {
	if !draft.Route.Valid() {
		o.releaseReservation(id)
		return fmt.Errorf("invalid terminal delivery route")
	}
	if draft.Stream == nil && draft.Checkpoint == nil && strings.TrimSpace(draft.Text) == "" && strings.TrimSpace(draft.Notification) == "" {
		o.releaseReservation(id)
		return fmt.Errorf("terminal delivery has no payload")
	}
	o.mu.Lock()
	entry := o.entryLocked(id)
	if entry == nil {
		delete(o.preparing, id)
		o.mu.Unlock()
		return fmt.Errorf("terminal outbox reservation not found")
	}
	_, err := o.mutateEntryLocked(entry, terminalOutboxTxOptions{
		validate:    true,
		persistWrap: "persist prepared terminal delivery",
	}, func(target *terminalOutboxEntry) error {
		target.Route = draft.Route
		target.AgentName = strings.TrimSpace(draft.AgentName)
		if err := mergeTerminalDeliveryGuard(target, draft); err != nil {
			return err
		}
		target.Failed = draft.Failed
		target.Stopped = draft.Stopped
		nextStream := cloneDurableStreamReference(draft.Stream)
		if nextStream == nil && draft.Checkpoint == nil {
			nextStream = cloneDurableStreamReference(target.Stream)
		}
		target.Stream = nextStream
		target.Checkpoint = cloneTerminalCheckpoint(draft.Checkpoint)
		target.ResultTitle = strings.TrimSpace(draft.ResultTitle)
		target.RichResult = draft.RichResult
		target.Text = draft.Text
		target.Notification = draft.Notification
		target.IdempotencyKey = strings.TrimSpace(draft.IdempotencyKey)
		target.ActiveStreamRecovery = false
		target.UpdatedAt = o.now()
		target.NextAttempt = target.UpdatedAt
		target.LastError = ""
		target.DeadLetter = false
		target.DeadLetterAt = time.Time{}
		target.CheckpointDelivered = false
		target.TextDelivered = false
		target.NotificationDelivered = false
		if strings.TrimSpace(draft.Trace.TraceID) != "" {
			trace := draft.Trace
			trace.RouteKey = ""
			target.Trace = &trace
		} else {
			target.Trace = nil
		}
		return nil
	})
	delete(o.preparing, id)
	o.mu.Unlock()
	o.signal()
	return err
}

func (o *terminalOutbox) releaseReservation(id string) {
	o.mu.Lock()
	delete(o.preparing, id)
	o.mu.Unlock()
	o.signal()
}

func (o *terminalOutbox) discardReservation(id string) error {
	o.mu.Lock()
	for index, entry := range o.entries {
		if entry.ID != id {
			continue
		}
		wasPreparing := o.preparing[id]
		if len(entry.PendingSupersedes) > 0 {
			delete(o.preparing, id)
			_, err := o.mutateEntryLocked(entry, terminalOutboxTxOptions{validate: true}, func(target *terminalOutboxEntry) error {
				target.Stream = nil
				target.Checkpoint = nil
				target.Text = ""
				target.Notification = ""
				target.ActiveStreamRecovery = false
				target.CheckpointDelivered = true
				target.TextDelivered = true
				target.NotificationDelivered = true
				target.UpdatedAt = o.now()
				target.LastError = ""
				target.DeadLetter = false
				target.DeadLetterAt = time.Time{}
				return nil
			})
			if err != nil {
				if wasPreparing {
					o.preparing[id] = true
				}
				o.mu.Unlock()
				return err
			}
			o.mu.Unlock()
			o.signal()
			return nil
		}
		delete(o.preparing, id)
		if err := o.removeEntryLocked(index); err != nil {
			if wasPreparing {
				o.preparing[id] = true
			}
			o.mu.Unlock()
			return err
		}
		o.mu.Unlock()
		return nil
	}
	delete(o.preparing, id)
	o.mu.Unlock()
	return nil
}

func (o *terminalOutbox) refreshStreamReservation(id string, route platform.DeliveryRoute, reference platform.DurableStreamReference) error {
	if !route.Valid() || strings.TrimSpace(reference.Kind) == "" || len(reference.Payload) == 0 || !json.Valid(reference.Payload) {
		return fmt.Errorf("invalid active stream recovery")
	}
	_, err := o.mutateEntry(id, terminalOutboxTxOptions{
		notFoundErr: ErrTerminalOutboxNotFound,
		validate:    true,
		persistWrap: "persist active stream recovery",
	}, func(entry *terminalOutboxEntry) error {
		entry.Route = route
		entry.Stream = cloneDurableStreamReference(&reference)
		entry.ActiveStreamRecovery = true
		entry.Checkpoint = nil
		entry.CheckpointDelivered = false
		entry.TextDelivered = false
		entry.NotificationDelivered = false
		entry.UpdatedAt = o.now()
		entry.NextAttempt = entry.UpdatedAt
		entry.LastError = ""
		entry.DeadLetter = false
		entry.DeadLetterAt = time.Time{}
		return nil
	})
	return err
}

func (o *terminalOutbox) refreshStreamReservationTrace(id string, trace observability.TraceContext) error {
	if strings.TrimSpace(trace.TraceID) == "" {
		return nil
	}
	trace.RouteKey = ""
	_, err := o.mutateEntry(id, terminalOutboxTxOptions{
		notFoundErr: ErrTerminalOutboxNotFound,
		validate:    true,
		persistWrap: "persist active stream recovery trace",
	}, func(entry *terminalOutboxEntry) error {
		entry.Trace = &trace
		entry.UpdatedAt = o.now()
		return nil
	})
	return err
}

func (o *terminalOutbox) refreshReservationDeliveryGuard(id string, guard terminalDeliveryGuard) error {
	if !guard.complete() {
		return fmt.Errorf("incomplete terminal follower delivery guard")
	}
	_, err := o.mutateEntry(id, terminalOutboxTxOptions{
		notFoundErr: ErrTerminalOutboxNotFound,
		validate:    true,
		persistWrap: "persist terminal delivery guard",
	}, func(entry *terminalOutboxEntry) error {
		entry.AuthorizedIdentity = strings.TrimSpace(guard.AuthorizedIdentity)
		entry.FollowerBindingKey = strings.TrimSpace(guard.FollowerBindingKey)
		entry.FollowerRevision = guard.FollowerRevision
		entry.FollowerThreadID = strings.TrimSpace(guard.FollowerThreadID)
		entry.UpdatedAt = o.now()
		return nil
	})
	return err
}

func (o *terminalOutbox) entryLocked(id string) *terminalOutboxEntry {
	for _, entry := range o.entries {
		if entry.ID == id {
			return entry
		}
	}
	return nil
}

func (o *terminalOutbox) persistLocked() error {
	return writeTerminalOutbox(o.path, o.entries, o.syncDirectory)
}

func cloneTerminalOutboxEntry(entry *terminalOutboxEntry) *terminalOutboxEntry {
	if entry == nil {
		return nil
	}
	clone := *entry
	clone.Stream = cloneDurableStreamReference(entry.Stream)
	clone.Checkpoint = cloneTerminalCheckpoint(entry.Checkpoint)
	clone.PendingSupersedes = make([]pendingStreamSupersede, len(entry.PendingSupersedes))
	for index, pending := range entry.PendingSupersedes {
		clone.PendingSupersedes[index] = clonePendingStreamSupersede(pending)
	}
	if entry.Trace != nil {
		trace := *entry.Trace
		clone.Trace = &trace
	}
	return &clone
}

func cloneTerminalOutboxEntries(entries []*terminalOutboxEntry) []*terminalOutboxEntry {
	clones := make([]*terminalOutboxEntry, len(entries))
	for index, entry := range entries {
		clones[index] = cloneTerminalOutboxEntry(entry)
	}
	return clones
}

func cloneDurableStreamReference(reference *platform.DurableStreamReference) *platform.DurableStreamReference {
	if reference == nil {
		return nil
	}
	clone := *reference
	clone.Payload = append(json.RawMessage(nil), reference.Payload...)
	return &clone
}

func cloneTerminalCheckpoint(checkpoint *platform.TerminalCheckpoint) *platform.TerminalCheckpoint {
	if checkpoint == nil {
		return nil
	}
	clone := *checkpoint
	clone.Payload = append(json.RawMessage(nil), checkpoint.Payload...)
	return &clone
}

func cloneSupersedeCheckpoint(checkpoint platform.SupersedeCheckpoint) platform.SupersedeCheckpoint {
	checkpoint.Payload = append(json.RawMessage(nil), checkpoint.Payload...)
	return checkpoint
}

func clonePendingStreamSupersede(pending pendingStreamSupersede) pendingStreamSupersede {
	pending.Checkpoint = cloneSupersedeCheckpoint(pending.Checkpoint)
	return pending
}

func loadTerminalOutbox(path string) ([]*terminalOutboxEntry, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("outbox path must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("outbox permissions are too broad: %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state terminalOutboxState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Version != terminalOutboxVersion {
		return nil, fmt.Errorf("unsupported outbox version %d", state.Version)
	}
	if len(state.Entries) > terminalOutboxMaxEntries {
		return nil, fmt.Errorf("terminal outbox has too many entries")
	}
	seen := make(map[string]struct{}, len(state.Entries))
	seenPending := make(map[string]struct{})
	for _, entry := range state.Entries {
		if err := validateTerminalOutboxEntry(entry); err != nil {
			return nil, err
		}
		if _, exists := seen[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate terminal outbox id %s", entry.ID)
		}
		if _, exists := seenPending[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate terminal outbox operation id %s", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		for _, pending := range entry.PendingSupersedes {
			if _, exists := seen[pending.ID]; exists {
				return nil, fmt.Errorf("duplicate terminal outbox operation id %s", pending.ID)
			}
			if _, exists := seenPending[pending.ID]; exists {
				return nil, fmt.Errorf("duplicate pending stream supersede id %s", pending.ID)
			}
			seenPending[pending.ID] = struct{}{}
		}
	}
	return state.Entries, nil
}

func validateTerminalOutboxEntry(entry *terminalOutboxEntry) error {
	if entry == nil {
		return fmt.Errorf("nil terminal outbox entry")
	}
	if _, err := uuid.Parse(entry.ID); err != nil {
		return fmt.Errorf("invalid terminal outbox id")
	}
	if !entry.Route.Valid() {
		return fmt.Errorf("invalid terminal outbox route")
	}
	guard := terminalDeliveryGuardFromEntry(entry)
	if !guard.empty() && !guard.complete() {
		return fmt.Errorf("incomplete terminal follower delivery guard")
	}
	if entry.ReplaySelectionID != "" || entry.ReplayTurnID != "" {
		if !guard.complete() || entry.ReplaySelectionID == "" || entry.ReplayTurnID == "" || !entry.RichResult || strings.TrimSpace(entry.Text) == "" {
			return fmt.Errorf("incomplete result replay delivery")
		}
	}
	if entry.Stream == nil && entry.Checkpoint == nil && strings.TrimSpace(entry.Text) == "" && strings.TrimSpace(entry.Notification) == "" && len(entry.PendingSupersedes) == 0 {
		return fmt.Errorf("terminal outbox entry has no payload")
	}
	if entry.Stream != nil {
		if strings.TrimSpace(entry.Stream.Kind) == "" || len(entry.Stream.Payload) == 0 || !json.Valid(entry.Stream.Payload) {
			return fmt.Errorf("invalid durable stream reference")
		}
	}
	if entry.Checkpoint != nil {
		if strings.TrimSpace(entry.Checkpoint.Kind) == "" || len(entry.Checkpoint.Payload) == 0 || !json.Valid(entry.Checkpoint.Payload) {
			return fmt.Errorf("invalid terminal checkpoint")
		}
	}
	if entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() || entry.NextAttempt.IsZero() {
		return fmt.Errorf("terminal outbox timestamps are missing")
	}
	if entry.DeadLetter && entry.DeadLetterAt.IsZero() {
		return fmt.Errorf("terminal outbox dead letter timestamp is missing")
	}
	seenPending := make(map[string]struct{}, len(entry.PendingSupersedes))
	for index := range entry.PendingSupersedes {
		pending := &entry.PendingSupersedes[index]
		if err := validatePendingStreamSupersede(pending); err != nil {
			return err
		}
		if _, exists := seenPending[pending.ID]; exists {
			return fmt.Errorf("duplicate pending stream supersede id %s", pending.ID)
		}
		seenPending[pending.ID] = struct{}{}
	}
	return nil
}

func validatePendingStreamSupersede(pending *pendingStreamSupersede) error {
	if pending == nil {
		return fmt.Errorf("nil pending stream supersede")
	}
	if _, err := uuid.Parse(pending.ID); err != nil {
		return fmt.Errorf("invalid pending stream supersede id")
	}
	if !pending.Route.Valid() {
		return fmt.Errorf("invalid pending stream supersede route")
	}
	if strings.TrimSpace(pending.Checkpoint.Kind) == "" || len(pending.Checkpoint.Payload) == 0 || !json.Valid(pending.Checkpoint.Payload) {
		return fmt.Errorf("invalid pending stream supersede checkpoint")
	}
	if pending.Attempts < 0 || pending.NextAttempt.IsZero() {
		return fmt.Errorf("pending stream supersede retry state is invalid")
	}
	if pending.DeadLetter && pending.DeadLetterAt.IsZero() {
		return fmt.Errorf("pending stream supersede dead letter timestamp is missing")
	}
	return nil
}

func syncTerminalOutboxDirectory(dir string) error {
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(dirFile.Sync(), dirFile.Close())
}

func writeTerminalOutbox(path string, entries []*terminalOutboxEntry, syncDirectory func(string) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("outbox path must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := terminalOutboxState{Version: terminalOutboxVersion, Entries: entries}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".terminal-outbox-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	// Rename is the commit point. Keep in-memory state aligned with the file
	// already visible at path even when the directory metadata cannot be synced.
	if err := syncDirectory(dir); err != nil {
		log.Printf("[terminal-outbox] state file replaced but parent directory sync failed: %v", err)
	}
	return nil
}
