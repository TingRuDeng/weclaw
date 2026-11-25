package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

func (o *terminalOutbox) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		for _, pending := range o.duePendingStreamSupersedes() {
			if ctx.Err() != nil {
				return
			}
			if err := o.attemptPendingStreamSupersede(ctx, pending.entryID, pending.pendingID); err != nil && ctx.Err() == nil {
				log.Printf("[terminal-outbox] retry pending supersede entry=%s pending=%s: %s",
					pending.entryID, pending.pendingID, observability.SanitizeText(err.Error()))
			}
		}
		for _, id := range o.dueIDs() {
			if ctx.Err() != nil {
				return
			}
			if err := o.attempt(ctx, id, nil); err != nil && ctx.Err() == nil {
				log.Printf("[terminal-outbox] retry pending id=%s: %s", id, observability.SanitizeText(err.Error()))
			}
		}
		delay, scheduled := o.nextAttemptDelay()
		if !scheduled {
			select {
			case <-ctx.Done():
				return
			case <-o.wake:
			}
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopAndDrainTimer(timer)
			return
		case <-o.wake:
			stopAndDrainTimer(timer)
		case <-timer.C:
		}
	}
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (o *terminalOutbox) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *terminalOutbox) dueIDs() []string {
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	due := make([]*terminalOutboxEntry, 0, len(o.entries))
	for _, entry := range o.entries {
		if terminalEntryHasWork(entry) && !entry.DeadLetter && !o.preparing[entry.ID] && !o.processing[entry.ID] && !entry.NextAttempt.After(now) {
			due = append(due, entry)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		if !due[i].NextAttempt.Equal(due[j].NextAttempt) {
			return due[i].NextAttempt.Before(due[j].NextAttempt)
		}
		if !due[i].CreatedAt.Equal(due[j].CreatedAt) {
			return due[i].CreatedAt.Before(due[j].CreatedAt)
		}
		return due[i].ID < due[j].ID
	})
	ids := make([]string, 0, len(due))
	for _, entry := range due {
		ids = append(ids, entry.ID)
	}
	return ids
}

func (o *terminalOutbox) nextAttemptDelay() (time.Duration, bool) {
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	var next time.Time
	for _, entry := range o.entries {
		if !o.processing[entry.ID] && !o.startupHeld[entry.ID] {
			for _, pending := range entry.PendingSupersedes {
				if !pending.DeadLetter && (next.IsZero() || pending.NextAttempt.Before(next)) {
					next = pending.NextAttempt
				}
			}
		}
		if terminalEntryHasWork(entry) && !entry.DeadLetter && !o.preparing[entry.ID] && !o.processing[entry.ID] &&
			(next.IsZero() || entry.NextAttempt.Before(next)) {
			next = entry.NextAttempt
		}
	}
	if next.IsZero() {
		return 0, false
	}
	if !next.After(now) {
		return 0, true
	}
	return next.Sub(now), true
}

func (o *terminalOutbox) status() TerminalOutboxStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	return terminalOutboxStatus(o.entries, o.preparing, o.processing, o.entryLimit())
}

func (o *terminalOutbox) redrive(id string) (TerminalOutboxRedriveResult, error) {
	id = strings.TrimSpace(id)
	now := o.now()
	o.mu.Lock()
	before := cloneTerminalOutboxEntries(o.entries)
	requested := 0
	for _, entry := range o.entries {
		matchedParent := id == "" || entry.ID == id
		matchedPending := false
		if id != "" && !matchedParent {
			matchedPending = pendingStreamSupersedeIndex(entry, id) >= 0
			if !matchedPending {
				continue
			}
		}
		changed := false
		if matchedParent && (terminalEntryHasWork(entry) || entry.DeadLetter) {
			entry.NextAttempt = now
			entry.DeadLetter = false
			entry.DeadLetterAt = time.Time{}
			changed = true
		}
		for index := range entry.PendingSupersedes {
			pending := &entry.PendingSupersedes[index]
			if !matchedParent && pending.ID != id {
				continue
			}
			pending.NextAttempt = now
			pending.DeadLetter = false
			pending.DeadLetterAt = time.Time{}
			changed = true
		}
		if changed {
			entry.UpdatedAt = now
			requested++
		}
	}
	if id != "" && requested == 0 {
		o.mu.Unlock()
		return TerminalOutboxRedriveResult{}, ErrTerminalOutboxNotFound
	}
	if requested > 0 {
		if err := o.persistLocked(); err != nil {
			o.entries = before
			o.mu.Unlock()
			return TerminalOutboxRedriveResult{}, fmt.Errorf("persist terminal outbox redrive: %w", err)
		}
	}
	status := terminalOutboxStatus(o.entries, o.preparing, o.processing)
	o.mu.Unlock()
	if requested > 0 {
		o.signal()
	}
	return TerminalOutboxRedriveResult{Requested: requested, Status: status}, nil
}

func terminalOutboxStatus(entries []*terminalOutboxEntry, preparing map[string]bool, processing map[string]bool, limits ...int) TerminalOutboxStatus {
	limit := terminalOutboxMaxEntries
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	type statusCandidate struct {
		value      TerminalOutboxEntryStatus
		deadLetter bool
		blocked    bool
	}
	candidates := make([]statusCandidate, 0, len(entries))
	for _, entry := range entries {
		isPreparing := preparing != nil && preparing[entry.ID]
		isProcessing := processing != nil && processing[entry.ID]
		if terminalEntryHasWork(entry) || entry.DeadLetter {
			candidates = append(candidates, statusCandidate{
				value: TerminalOutboxEntryStatus{
					ID: entry.ID, AgentName: entry.AgentName, Attempts: entry.Attempts,
					CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt, NextAttempt: entry.NextAttempt,
					LastError: observability.SanitizeText(entry.LastError), Preparing: isPreparing, Processing: isProcessing,
					DeadLetter: entry.DeadLetter, DeadLetterAt: entry.DeadLetterAt,
				},
				deadLetter: entry.DeadLetter, blocked: isPreparing || isProcessing,
			})
		}
		for _, pending := range entry.PendingSupersedes {
			candidates = append(candidates, statusCandidate{
				value: TerminalOutboxEntryStatus{
					ID: pending.ID, Kind: "supersede", ParentID: entry.ID, AgentName: entry.AgentName,
					Attempts: pending.Attempts, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
					NextAttempt: pending.NextAttempt, LastError: observability.SanitizeText(pending.LastError),
					Processing: isProcessing, DeadLetter: pending.DeadLetter, DeadLetterAt: pending.DeadLetterAt,
				},
				deadLetter: pending.DeadLetter, blocked: isProcessing,
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].value.CreatedAt.Equal(candidates[j].value.CreatedAt) {
			return candidates[i].value.CreatedAt.Before(candidates[j].value.CreatedAt)
		}
		return candidates[i].value.ID < candidates[j].value.ID
	})
	status := TerminalOutboxStatus{}
	var recentErrorAt time.Time
	for _, candidate := range candidates {
		entry := candidate.value
		if candidate.deadLetter {
			status.DeadLetter++
		} else {
			status.Pending++
		}
		if entry.Preparing {
			status.Preparing++
		}
		if entry.Processing {
			status.Processing++
		}
		if !candidate.deadLetter && (status.OldestCreatedAt.IsZero() || entry.CreatedAt.Before(status.OldestCreatedAt)) {
			status.OldestCreatedAt = entry.CreatedAt
		}
		if !candidate.deadLetter && !candidate.blocked &&
			(status.NextAttempt.IsZero() || entry.NextAttempt.Before(status.NextAttempt)) {
			status.NextAttempt = entry.NextAttempt
		}
		if entry.LastError != "" && (recentErrorAt.IsZero() || entry.UpdatedAt.After(recentErrorAt)) {
			recentErrorAt = entry.UpdatedAt
			status.RecentError = observability.SanitizeText(entry.LastError)
		}
		if len(status.Entries) < terminalOutboxStatusMaxEntries {
			status.Entries = append(status.Entries, entry)
		}
	}
	status.AtCapacity = len(entries) >= limit
	status.Truncated = len(candidates) > len(status.Entries)
	return status
}

func terminalEntryHasWork(entry *terminalOutboxEntry) bool {
	if entry == nil {
		return false
	}
	if entry.Stream != nil {
		return true
	}
	if entry.Checkpoint != nil && !entry.CheckpointDelivered {
		return true
	}
	if strings.TrimSpace(entry.Text) != "" && !entry.TextDelivered {
		return true
	}
	return strings.TrimSpace(entry.Notification) != "" && !entry.NotificationDelivered
}

func (o *terminalOutbox) attempt(parent context.Context, id string, preferred platform.Replier) error {
	if o.deliveryBarrier != nil {
		o.deliveryBarrier.RLock()
		defer o.deliveryBarrier.RUnlock()
	}
	entry, ok := o.beginAttempt(id)
	if !ok {
		return nil
	}
	defer o.endAttempt(id)
	allowed, reason := true, ""
	if o.deliveryDecision != nil {
		allowed, reason = o.deliveryDecision(entry)
	}
	if !allowed {
		if reason == "attach_not_ready" {
			if err := o.deferDelivery(id, reason); err != nil {
				return fmt.Errorf("defer terminal delivery until follower attach is ready: %w", err)
			}
			return nil
		}
		if err := o.discardReservation(id); err != nil {
			return fmt.Errorf("discard unauthorized terminal delivery: %w", err)
		}
		return nil
	}
	o.recordTrace(entry, "terminal.delivery.attempt", "running", fmt.Sprintf("attempt=%d", entry.Attempts+1))
	var preparationErr error
	if entry.Stream != nil && entry.Checkpoint == nil {
		reply, err := o.resolveStageReplier(entry.Route, preferred)
		if err == nil {
			entry, err = o.prepareStreamRecovery(id, entry, reply)
		}
		preparationErr = err
	}

	type stageResult struct {
		stage terminalOutboxStage
		err   error
	}
	results := make(chan stageResult, 3)
	stageCount := 0
	// 飞书的独立结果卡必须排在进度卡终态更新之后。两者并发投递时，
	// 结果卡可能先到达会话，用户会看到最终结果出现在进度卡前面。
	waitForCheckpoint := entry.Route.Platform == platform.PlatformFeishu &&
		entry.RichResult && entry.Checkpoint != nil && !entry.CheckpointDelivered
	checkpointDone := make(chan struct{})
	startStage := func(stage terminalOutboxStage, deliver func(context.Context, platform.Replier) error) {
		stageCount++
		go func() {
			reply, err := o.resolveStageReplier(entry.Route, preferred)
			if err == nil {
				ctx, cancel := context.WithTimeout(parent, terminalOutboxDeliveryTimeout)
				err = deliver(ctx, reply)
				cancel()
			}
			if err == nil {
				if stage == terminalOutboxTextStage && entry.ReplaySelectionID != "" {
					if o.replayDelivered == nil {
						err = fmt.Errorf("result replay receipt store unavailable")
					} else {
						err = o.replayDelivered(entry)
					}
				}
				if err == nil {
					err = o.markDelivered(id, stage)
				}
			}
			if stage == terminalOutboxCheckpointStage {
				close(checkpointDone)
			}
			results <- stageResult{stage: stage, err: err}
		}()
	}

	if preparationErr != nil {
		stageCount++
		results <- stageResult{stage: terminalOutboxCheckpointStage, err: preparationErr}
	} else if entry.Checkpoint != nil && !entry.CheckpointDelivered {
		checkpoint := cloneTerminalCheckpoint(entry.Checkpoint)
		startStage(terminalOutboxCheckpointStage, func(ctx context.Context, reply platform.Replier) error {
			durable, ok := optionalDurableTerminalReplier(reply)
			if !ok {
				return platform.ErrUnsupported
			}
			return durable.DeliverTerminal(ctx, *checkpoint)
		})
	}
	if strings.TrimSpace(entry.Text) != "" && !entry.TextDelivered {
		text := entry.Text
		resultKey := terminalOutboxIdempotencyKey(entry, "text")
		if entry.RichResult {
			resultKey = terminalOutboxIdempotencyKey(entry, "result")
		}
		startStage(terminalOutboxTextStage, func(ctx context.Context, reply platform.Replier) error {
			if waitForCheckpoint {
				<-checkpointDone
			}
			return sendOutboxResult(ctx, reply, entry, text, resultKey)
		})
	}
	if strings.TrimSpace(entry.Notification) != "" && !entry.NotificationDelivered {
		notification := entry.Notification
		startStage(terminalOutboxNotificationStage, func(ctx context.Context, reply platform.Replier) error {
			return sendOutboxText(ctx, reply, notification, terminalOutboxIdempotencyKey(entry, "notification"))
		})
	}
	var deliveryErrors []error
	for index := 0; index < stageCount; index++ {
		result := <-results
		if result.err != nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("%s delivery: %w", result.stage, result.err))
		}
	}
	if err := errors.Join(deliveryErrors...); err != nil {
		return o.recordFailure(id, err)
	}
	return o.removeDelivered(id)
}

func terminalOutboxIdempotencyKey(entry *terminalOutboxEntry, stage string) string {
	base := ""
	if entry != nil {
		base = strings.TrimSpace(entry.IdempotencyKey)
		if base == "" {
			base = entry.ID
		}
	}
	return base + ":" + stage
}

func (o *terminalOutbox) prepareStreamRecovery(id string, entry *terminalOutboxEntry, reply platform.Replier) (*terminalOutboxEntry, error) {
	if entry == nil || entry.Stream == nil {
		return entry, nil
	}
	content := firstNonBlank(entry.Text, activeStreamRestartText)
	state := platform.StreamTerminalCompleted
	if entry.Stopped {
		state = platform.StreamTerminalStopped
	} else if entry.Failed {
		state = platform.StreamTerminalFailed
	}
	var checkpoint platform.TerminalCheckpoint
	var err error
	if stateful, ok := reply.(platform.StatefulDurableStreamTerminalPreparer); ok {
		checkpoint, err = stateful.PrepareTerminalFromReferenceWithState(*entry.Stream, content, state)
	} else if preparer, ok := reply.(platform.DurableStreamTerminalPreparer); ok {
		checkpoint, err = preparer.PrepareTerminalFromReference(*entry.Stream, content, state == platform.StreamTerminalFailed)
	} else {
		return entry, platform.ErrUnsupported
	}
	if err != nil {
		return entry, err
	}
	if strings.TrimSpace(checkpoint.Kind) == "" || len(checkpoint.Payload) == 0 || !json.Valid(checkpoint.Payload) {
		return entry, fmt.Errorf("invalid recovered terminal checkpoint")
	}

	prepared, err := o.mutateEntry(id, terminalOutboxTxOptions{
		notFoundErr: ErrTerminalOutboxNotFound,
		validate:    true,
		persistWrap: "persist recovered terminal checkpoint",
	}, func(current *terminalOutboxEntry) error {
		current.Stream = nil
		current.Checkpoint = cloneTerminalCheckpoint(&checkpoint)
		current.UpdatedAt = o.now()
		current.NextAttempt = current.UpdatedAt
		return nil
	})
	if err != nil {
		return entry, err
	}
	return prepared, nil
}

func (o *terminalOutbox) beginAttempt(id string) (*terminalOutboxEntry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.processing[id] {
		return nil, false
	}
	entry := o.entryLocked(id)
	if entry == nil || entry.DeadLetter || !terminalEntryHasWork(entry) {
		return nil, false
	}
	o.processing[id] = true
	return cloneTerminalOutboxEntry(entry), true
}

func (o *terminalOutbox) endAttempt(id string) {
	o.mu.Lock()
	delete(o.processing, id)
	o.mu.Unlock()
	o.signal()
}

func (o *terminalOutbox) resolveReplier(route platform.DeliveryRoute, preferred platform.Replier) (platform.Replier, error) {
	if preferred != nil {
		if reporter, ok := optionalDeliveryRouteReporter(preferred); ok && sameDeliveryRoute(reporter.DeliveryRoute(), route) {
			return preferred, nil
		}
	}
	if reply, ok := o.registry.ReplierForRoute(route); ok && reply != nil {
		return reply, nil
	}
	return nil, fmt.Errorf("no outbound replier for platform=%s", route.Platform)
}

// resolveStageReplier 优先为每个投递阶段创建独立回复器，避免卡片调用持有序列化锁时阻塞最终文本。
func (o *terminalOutbox) resolveStageReplier(route platform.DeliveryRoute, preferred platform.Replier) (platform.Replier, error) {
	if o.registry != nil {
		if reply, ok := o.registry.ReplierForRoute(route); ok && reply != nil {
			return reply, nil
		}
	}
	return o.resolveReplier(route, preferred)
}

func sameDeliveryRoute(left platform.DeliveryRoute, right platform.DeliveryRoute) bool {
	return left.Platform == right.Platform && strings.TrimSpace(left.AccountID) == strings.TrimSpace(right.AccountID) &&
		strings.TrimSpace(left.ChatID) == strings.TrimSpace(right.ChatID) && strings.TrimSpace(left.ReplyToID) == strings.TrimSpace(right.ReplyToID)
}

func sendOutboxText(ctx context.Context, reply platform.Replier, text string, key string) error {
	idempotent, ok := optionalIdempotentTextReplier(reply)
	if !ok {
		return platform.ErrUnsupported
	}
	return idempotent.SendTextIdempotent(ctx, text, key)
}

func sendOutboxResult(ctx context.Context, reply platform.Replier, entry *terminalOutboxEntry, text string, key string) error {
	if entry == nil || !entry.RichResult {
		return sendOutboxText(ctx, reply, text, key)
	}
	result := platform.TerminalResult{
		Title: firstNonBlank(entry.ResultTitle, entry.AgentName, "WeClaw"),
		Text:  text,
		State: terminalOutboxResultState(entry),
	}
	idempotent, ok := optionalIdempotentResultReplier(reply)
	if !ok {
		return sendOutboxText(ctx, reply, text, key)
	}
	if err := idempotent.SendResultIdempotent(ctx, result, key); err != nil {
		if errors.Is(err, platform.ErrUnsupported) {
			return sendOutboxText(ctx, reply, text, key)
		}
		return err
	}
	return nil
}

func (o *terminalOutbox) deferDelivery(id string, reason string) error {
	summary := "delivery deferred: " + strings.TrimSpace(reason)
	firstDeferral := false
	deferred, err := o.mutateEntry(id, terminalOutboxTxOptions{}, func(entry *terminalOutboxEntry) error {
		now := o.now()
		firstDeferral = entry.LastError != summary
		entry.UpdatedAt = now
		entry.NextAttempt = now.Add(terminalOutboxRetryMin)
		entry.LastError = summary
		return nil
	})
	if err != nil {
		return err
	}
	if deferred != nil && firstDeferral {
		o.recordTrace(deferred, "terminal.delivery.deferred", "waiting", reason)
	}
	return nil
}

func terminalOutboxResultState(entry *terminalOutboxEntry) platform.StreamTerminalState {
	if entry != nil && entry.Stopped {
		return platform.StreamTerminalStopped
	}
	if entry != nil && entry.Failed {
		return platform.StreamTerminalFailed
	}
	return platform.StreamTerminalCompleted
}

type terminalOutboxStage int

const (
	terminalOutboxCheckpointStage terminalOutboxStage = iota
	terminalOutboxTextStage
	terminalOutboxNotificationStage
)

func (s terminalOutboxStage) String() string {
	switch s {
	case terminalOutboxCheckpointStage:
		return "checkpoint"
	case terminalOutboxTextStage:
		return "text"
	case terminalOutboxNotificationStage:
		return "notification"
	default:
		return "unknown"
	}
}

func (o *terminalOutbox) markDelivered(id string, stage terminalOutboxStage) error {
	_, err := o.mutateEntry(id, terminalOutboxTxOptions{}, func(entry *terminalOutboxEntry) error {
		switch stage {
		case terminalOutboxCheckpointStage:
			entry.CheckpointDelivered = true
		case terminalOutboxTextStage:
			entry.TextDelivered = true
		case terminalOutboxNotificationStage:
			entry.NotificationDelivered = true
		}
		entry.UpdatedAt = o.now()
		entry.LastError = ""
		return nil
	})
	return err
}

func (o *terminalOutbox) recordFailure(id string, deliveryErr error) error {
	clone, err := o.mutateEntry(id, terminalOutboxTxOptions{
		persistWrap: fmt.Sprintf("delivery failed: %v; persist retry state", deliveryErr),
	}, func(entry *terminalOutboxEntry) error {
		entry.Attempts++
		entry.UpdatedAt = o.now()
		entry.LastError = truncateTerminalOutboxError(deliveryErr)
		if entry.Attempts >= o.attemptLimit() {
			entry.DeadLetter = true
			entry.DeadLetterAt = entry.UpdatedAt
			entry.NextAttempt = entry.UpdatedAt
		} else {
			entry.NextAttempt = entry.UpdatedAt.Add(terminalOutboxBackoff(entry.Attempts))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if clone == nil {
		return deliveryErr
	}
	if clone.DeadLetter {
		o.recordTrace(clone, "terminal.delivery.dead_letter", "failed", deliveryErr.Error())
	} else {
		o.recordTrace(clone, "terminal.delivery.retry", "failed", deliveryErr.Error())
	}
	return deliveryErr
}

func terminalOutboxBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return terminalOutboxRetryMin
	}
	delay := terminalOutboxRetryMin
	for index := 1; index < attempt && delay < terminalOutboxRetryMax; index++ {
		delay *= 2
	}
	if delay > terminalOutboxRetryMax {
		return terminalOutboxRetryMax
	}
	return delay
}

func truncateTerminalOutboxError(err error) string {
	if err == nil {
		return ""
	}
	value := []rune(strings.TrimSpace(err.Error()))
	if len(value) <= terminalOutboxErrorMaxRunes {
		return string(value)
	}
	return string(value[:terminalOutboxErrorMaxRunes]) + "…"
}

func (o *terminalOutbox) removeDelivered(id string) error {
	o.mu.Lock()
	for index, entry := range o.entries {
		if entry.ID != id {
			continue
		}
		clone := cloneTerminalOutboxEntry(entry)
		if len(entry.PendingSupersedes) > 0 {
			_, err := o.mutateEntryLocked(entry, terminalOutboxTxOptions{}, func(target *terminalOutboxEntry) error {
				target.UpdatedAt = o.now()
				target.LastError = ""
				target.DeadLetter = false
				target.DeadLetterAt = time.Time{}
				return nil
			})
			o.mu.Unlock()
			if err != nil {
				return err
			}
			o.recordTrace(clone, "terminal.delivery.completed", "completed", "terminal delivery committed; supersede pending")
			return nil
		}
		err := o.removeEntryLocked(index)
		o.mu.Unlock()
		if err == nil {
			o.recordTrace(clone, "terminal.delivery.completed", "completed", "terminal delivery committed")
		}
		return err
	}
	o.mu.Unlock()
	return nil
}

func (o *terminalOutbox) recordTrace(entry *terminalOutboxEntry, stage string, state string, summary string) {
	if o == nil || o.trace == nil || entry == nil || entry.Trace == nil {
		return
	}
	event := observability.EventFor(*entry.Trace, stage, state)
	event.Source = "terminal_outbox"
	event.EventID = entry.ID
	event.Summary = summary
	_ = o.trace.Record(event)
}
