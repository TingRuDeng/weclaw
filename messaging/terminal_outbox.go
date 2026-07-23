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
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const (
	terminalOutboxVersion         = 1
	terminalOutboxFileName        = "terminal-outbox.json"
	terminalOutboxMaxEntries      = 10000
	terminalOutboxDeliveryTimeout = 10 * time.Second
	terminalOutboxRetryMin        = 2 * time.Second
	terminalOutboxRetryMax        = time.Minute
	terminalOutboxPollInterval    = time.Second
	terminalOutboxErrorMaxRunes   = 500
)

type terminalOutboxState struct {
	Version int                    `json:"version"`
	Entries []*terminalOutboxEntry `json:"entries"`
}

type terminalOutboxEntry struct {
	ID           string                       `json:"id"`
	Route        platform.DeliveryRoute       `json:"route"`
	AgentName    string                       `json:"agent_name,omitempty"`
	Failed       bool                         `json:"failed,omitempty"`
	Checkpoint   *platform.TerminalCheckpoint `json:"checkpoint,omitempty"`
	Text         string                       `json:"text,omitempty"`
	Notification string                       `json:"notification,omitempty"`
	Trace        *observability.TraceContext  `json:"trace,omitempty"`

	CheckpointDelivered   bool `json:"checkpoint_delivered,omitempty"`
	TextDelivered         bool `json:"text_delivered,omitempty"`
	NotificationDelivered bool `json:"notification_delivered,omitempty"`

	Attempts    int       `json:"attempts,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	NextAttempt time.Time `json:"next_attempt"`
	LastError   string    `json:"last_error,omitempty"`
}

type terminalOutboxDraft struct {
	Route        platform.DeliveryRoute
	AgentName    string
	Failed       bool
	Checkpoint   *platform.TerminalCheckpoint
	Text         string
	Notification string
	Trace        observability.TraceContext
}

type terminalOutbox struct {
	mu         sync.Mutex
	path       string
	registry   *platform.Registry
	entries    []*terminalOutboxEntry
	preparing  map[string]bool
	processing map[string]bool
	wake       chan struct{}
	now        func() time.Time
	trace      observability.Recorder
}

// DefaultTerminalOutboxFile 返回终态 outbox 的主机级状态文件。
func DefaultTerminalOutboxFile() string {
	dataDir, err := config.DataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dataDir, "state", terminalOutboxFileName)
}

// StartTerminalOutbox 在平台 registry 可用后装载并启动跨重启终态投递。
func (h *Handler) StartTerminalOutbox(ctx context.Context, registry *platform.Registry, path string) error {
	if registry == nil {
		return fmt.Errorf("terminal outbox requires platform registry")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("terminal outbox path is empty")
	}
	outbox, err := newTerminalOutbox(path, registry, h.traceRecorder)
	if err != nil {
		return err
	}
	h.terminalOutboxMu.Lock()
	if h.terminalOutbox != nil {
		h.terminalOutboxMu.Unlock()
		return fmt.Errorf("terminal outbox already started")
	}
	h.terminalOutbox = outbox
	h.terminalOutboxMu.Unlock()
	go outbox.run(ctx)
	return nil
}

func (h *Handler) currentTerminalOutbox() *terminalOutbox {
	h.terminalOutboxMu.RLock()
	defer h.terminalOutboxMu.RUnlock()
	return h.terminalOutbox
}

func newTerminalOutbox(path string, registry *platform.Registry, traceRecorders ...observability.Recorder) (*terminalOutbox, error) {
	outbox := &terminalOutbox{
		path: path, registry: registry, preparing: make(map[string]bool), processing: make(map[string]bool),
		wake: make(chan struct{}, 1), now: time.Now,
	}
	if len(traceRecorders) > 0 {
		outbox.trace = traceRecorders[0]
	}
	entries, err := loadTerminalOutbox(path)
	if err != nil {
		return nil, fmt.Errorf("load terminal outbox: %w", err)
	}
	outbox.entries = entries
	return outbox, nil
}

func (o *terminalOutbox) enqueueAndAttempt(ctx context.Context, draft terminalOutboxDraft, preferred platform.Replier) error {
	entry, err := o.enqueue(draft)
	if err != nil {
		return err
	}
	if err := o.attempt(ctx, entry.ID, preferred); err != nil {
		log.Printf("[terminal-outbox] delivery pending id=%s platform=%s account=%s: %v",
			entry.ID, entry.Route.Platform, entry.Route.AccountID, err)
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
	if draft.Checkpoint == nil && strings.TrimSpace(draft.Text) == "" && strings.TrimSpace(draft.Notification) == "" {
		return nil, fmt.Errorf("terminal delivery has no payload")
	}
	now := o.now()
	entry := &terminalOutboxEntry{
		ID: uuid.NewString(), Route: draft.Route,
		AgentName: strings.TrimSpace(draft.AgentName), Failed: draft.Failed,
		Checkpoint: draft.Checkpoint, Text: draft.Text, Notification: draft.Notification,
		CreatedAt: now, UpdatedAt: now, NextAttempt: now,
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
	if len(o.entries) >= terminalOutboxMaxEntries {
		o.mu.Unlock()
		return nil, fmt.Errorf("terminal outbox capacity exceeded")
	}
	o.entries = append(o.entries, entry)
	if preparing {
		o.preparing[entry.ID] = true
	}
	if err := o.persistLocked(); err != nil {
		o.entries = o.entries[:len(o.entries)-1]
		delete(o.preparing, entry.ID)
		o.mu.Unlock()
		return nil, fmt.Errorf("persist terminal delivery: %w", err)
	}
	clone := cloneTerminalOutboxEntry(entry)
	o.mu.Unlock()
	o.recordTrace(clone, "terminal.outbox.enqueued", "pending", "terminal delivery queued")
	return clone, nil
}

// commitReservation 用 checkpoint 终态替换恢复文本；持久化失败时保留原草稿并允许重试投递。
func (o *terminalOutbox) commitReservation(id string, draft terminalOutboxDraft) error {
	if !draft.Route.Valid() {
		o.releaseReservation(id)
		return fmt.Errorf("invalid terminal delivery route")
	}
	if draft.Checkpoint == nil && strings.TrimSpace(draft.Text) == "" && strings.TrimSpace(draft.Notification) == "" {
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
	before := cloneTerminalOutboxEntry(entry)
	entry.Route = draft.Route
	entry.AgentName = strings.TrimSpace(draft.AgentName)
	entry.Failed = draft.Failed
	entry.Checkpoint = cloneTerminalCheckpoint(draft.Checkpoint)
	entry.Text = draft.Text
	entry.Notification = draft.Notification
	entry.UpdatedAt = o.now()
	entry.NextAttempt = entry.UpdatedAt
	entry.LastError = ""
	if strings.TrimSpace(draft.Trace.TraceID) != "" {
		trace := draft.Trace
		trace.RouteKey = ""
		entry.Trace = &trace
	} else {
		entry.Trace = nil
	}
	if err := validateTerminalOutboxEntry(entry); err != nil {
		*entry = *before
		delete(o.preparing, id)
		o.mu.Unlock()
		o.signal()
		return err
	}
	if err := o.persistLocked(); err != nil {
		*entry = *before
		delete(o.preparing, id)
		o.mu.Unlock()
		o.signal()
		return fmt.Errorf("persist prepared terminal delivery: %w", err)
	}
	delete(o.preparing, id)
	o.mu.Unlock()
	o.signal()
	return nil
}

func (o *terminalOutbox) releaseReservation(id string) {
	o.mu.Lock()
	delete(o.preparing, id)
	o.mu.Unlock()
	o.signal()
}

func (o *terminalOutbox) run(ctx context.Context) {
	ticker := time.NewTicker(terminalOutboxPollInterval)
	defer ticker.Stop()
	o.signal()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-o.wake:
		}
		for _, id := range o.dueIDs() {
			if err := o.attempt(ctx, id, nil); err != nil && ctx.Err() == nil {
				log.Printf("[terminal-outbox] retry pending id=%s: %v", id, err)
			}
		}
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
	ids := make([]string, 0, len(o.entries))
	for _, entry := range o.entries {
		if !o.preparing[entry.ID] && !o.processing[entry.ID] && !entry.NextAttempt.After(now) {
			ids = append(ids, entry.ID)
		}
	}
	return ids
}

func (o *terminalOutbox) attempt(parent context.Context, id string, preferred platform.Replier) error {
	entry, ok := o.beginAttempt(id)
	if !ok {
		return nil
	}
	defer o.endAttempt(id)
	o.recordTrace(entry, "terminal.delivery.attempt", "running", fmt.Sprintf("attempt=%d", entry.Attempts+1))
	reply, err := o.resolveReplier(entry.Route, preferred)
	if err != nil {
		return o.recordFailure(id, err)
	}
	ctx, cancel := context.WithTimeout(parent, terminalOutboxDeliveryTimeout)
	defer cancel()
	if entry.Checkpoint != nil && !entry.CheckpointDelivered {
		durable, ok := reply.(platform.DurableTerminalReplier)
		if !ok {
			return o.recordFailure(id, platform.ErrUnsupported)
		}
		if err := durable.DeliverTerminal(ctx, *entry.Checkpoint); err != nil {
			return o.recordFailure(id, err)
		}
		if err := o.markDelivered(id, terminalOutboxCheckpointStage); err != nil {
			return err
		}
	}
	if strings.TrimSpace(entry.Text) != "" && !entry.TextDelivered {
		if err := sendOutboxText(ctx, reply, entry.Text, id+":text"); err != nil {
			return o.recordFailure(id, err)
		}
		if err := o.markDelivered(id, terminalOutboxTextStage); err != nil {
			return err
		}
	}
	if strings.TrimSpace(entry.Notification) != "" && !entry.NotificationDelivered {
		if err := sendOutboxText(ctx, reply, entry.Notification, id+":notification"); err != nil {
			return o.recordFailure(id, err)
		}
		if err := o.markDelivered(id, terminalOutboxNotificationStage); err != nil {
			return err
		}
	}
	return o.removeDelivered(id)
}

func (o *terminalOutbox) beginAttempt(id string) (*terminalOutboxEntry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.processing[id] {
		return nil, false
	}
	entry := o.entryLocked(id)
	if entry == nil {
		return nil, false
	}
	o.processing[id] = true
	return cloneTerminalOutboxEntry(entry), true
}

func (o *terminalOutbox) endAttempt(id string) {
	o.mu.Lock()
	delete(o.processing, id)
	o.mu.Unlock()
}

func (o *terminalOutbox) resolveReplier(route platform.DeliveryRoute, preferred platform.Replier) (platform.Replier, error) {
	if preferred != nil {
		if reporter, ok := preferred.(platform.DeliveryRouteReporter); ok && sameDeliveryRoute(reporter.DeliveryRoute(), route) {
			return preferred, nil
		}
	}
	if reply, ok := o.registry.ReplierForRoute(route); ok && reply != nil {
		return reply, nil
	}
	return nil, fmt.Errorf("no outbound replier for platform=%s account=%s", route.Platform, route.AccountID)
}

func sameDeliveryRoute(left platform.DeliveryRoute, right platform.DeliveryRoute) bool {
	return left.Platform == right.Platform && strings.TrimSpace(left.AccountID) == strings.TrimSpace(right.AccountID) &&
		strings.TrimSpace(left.ChatID) == strings.TrimSpace(right.ChatID) && strings.TrimSpace(left.ReplyToID) == strings.TrimSpace(right.ReplyToID)
}

func sendOutboxText(ctx context.Context, reply platform.Replier, text string, key string) error {
	idempotent, ok := reply.(platform.IdempotentTextReplier)
	if !ok {
		return platform.ErrUnsupported
	}
	return idempotent.SendTextIdempotent(ctx, text, key)
}

type terminalOutboxStage int

const (
	terminalOutboxCheckpointStage terminalOutboxStage = iota
	terminalOutboxTextStage
	terminalOutboxNotificationStage
)

func (o *terminalOutbox) markDelivered(id string, stage terminalOutboxStage) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry := o.entryLocked(id)
	if entry == nil {
		return nil
	}
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
	return o.persistLocked()
}

func (o *terminalOutbox) recordFailure(id string, deliveryErr error) error {
	o.mu.Lock()
	entry := o.entryLocked(id)
	if entry == nil {
		o.mu.Unlock()
		return deliveryErr
	}
	entry.Attempts++
	entry.UpdatedAt = o.now()
	entry.NextAttempt = entry.UpdatedAt.Add(terminalOutboxBackoff(entry.Attempts))
	entry.LastError = truncateTerminalOutboxError(deliveryErr)
	if err := o.persistLocked(); err != nil {
		o.mu.Unlock()
		return fmt.Errorf("delivery failed: %v; persist retry state: %w", deliveryErr, err)
	}
	clone := cloneTerminalOutboxEntry(entry)
	o.mu.Unlock()
	o.recordTrace(clone, "terminal.delivery.retry", "failed", deliveryErr.Error())
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
		o.entries = append(o.entries[:index], o.entries[index+1:]...)
		err := o.persistLocked()
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

func (o *terminalOutbox) entryLocked(id string) *terminalOutboxEntry {
	for _, entry := range o.entries {
		if entry.ID == id {
			return entry
		}
	}
	return nil
}

func (o *terminalOutbox) persistLocked() error {
	return writeTerminalOutbox(o.path, o.entries)
}

func cloneTerminalOutboxEntry(entry *terminalOutboxEntry) *terminalOutboxEntry {
	if entry == nil {
		return nil
	}
	clone := *entry
	clone.Checkpoint = cloneTerminalCheckpoint(entry.Checkpoint)
	if entry.Trace != nil {
		trace := *entry.Trace
		clone.Trace = &trace
	}
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
	for _, entry := range state.Entries {
		if err := validateTerminalOutboxEntry(entry); err != nil {
			return nil, err
		}
		if _, exists := seen[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate terminal outbox id %s", entry.ID)
		}
		seen[entry.ID] = struct{}{}
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
	if entry.Checkpoint == nil && strings.TrimSpace(entry.Text) == "" && strings.TrimSpace(entry.Notification) == "" {
		return fmt.Errorf("terminal outbox entry has no payload")
	}
	if entry.Checkpoint != nil {
		if strings.TrimSpace(entry.Checkpoint.Kind) == "" || len(entry.Checkpoint.Payload) == 0 || !json.Valid(entry.Checkpoint.Payload) {
			return fmt.Errorf("invalid terminal checkpoint")
		}
	}
	if entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() || entry.NextAttempt.IsZero() {
		return fmt.Errorf("terminal outbox timestamps are missing")
	}
	return nil
}

func writeTerminalOutbox(path string, entries []*terminalOutboxEntry) error {
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
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	return errors.Join(syncErr, closeErr)
}
