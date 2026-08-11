package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const (
	terminalOutboxVersion          = 1
	terminalOutboxFileName         = "terminal-outbox.json"
	terminalOutboxMaxEntries       = 10000
	terminalOutboxDeliveryTimeout  = 10 * time.Second
	terminalOutboxRetryMin         = 2 * time.Second
	terminalOutboxRetryMax         = time.Minute
	terminalOutboxMaxAttempts      = 12
	terminalOutboxErrorMaxRunes    = 500
	terminalOutboxStatusMaxEntries = 100
	activeStreamRestartText        = "任务已中断。WeClaw 服务在任务执行期间发生重启。"
)

var (
	ErrTerminalOutboxUnavailable = errors.New("terminal outbox unavailable")
	ErrTerminalOutboxNotFound    = errors.New("terminal outbox entry not found")
)

// TerminalOutboxEntryStatus 是仅供本机运维使用的脱敏投递状态，不包含平台路由和消息正文。
type TerminalOutboxEntryStatus struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind,omitempty"`
	ParentID     string    `json:"parent_id,omitempty"`
	AgentName    string    `json:"agent_name,omitempty"`
	Attempts     int       `json:"attempts"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	NextAttempt  time.Time `json:"next_attempt"`
	LastError    string    `json:"last_error,omitempty"`
	Preparing    bool      `json:"preparing,omitempty"`
	Processing   bool      `json:"processing,omitempty"`
	DeadLetter   bool      `json:"dead_letter,omitempty"`
	DeadLetterAt time.Time `json:"dead_letter_at,omitempty"`
}

// TerminalOutboxStatus 汇总终态投递积压与待人工 redrive 的死信；
// Entries 最多返回最早的 100 条。
type TerminalOutboxStatus struct {
	Pending         int                         `json:"pending"`
	DeadLetter      int                         `json:"dead_letter"`
	Preparing       int                         `json:"preparing"`
	Processing      int                         `json:"processing"`
	OldestCreatedAt time.Time                   `json:"oldest_created_at,omitempty"`
	NextAttempt     time.Time                   `json:"next_attempt,omitempty"`
	RecentError     string                      `json:"recent_error,omitempty"`
	AtCapacity      bool                        `json:"at_capacity"`
	Truncated       bool                        `json:"truncated,omitempty"`
	Entries         []TerminalOutboxEntryStatus `json:"entries,omitempty"`
}

type TerminalOutboxRedriveResult struct {
	Requested int                  `json:"requested"`
	Status    TerminalOutboxStatus `json:"status"`
}

type terminalOutboxState struct {
	Version int                    `json:"version"`
	Entries []*terminalOutboxEntry `json:"entries"`
}

type pendingStreamSupersede struct {
	ID           string                       `json:"id"`
	Route        platform.DeliveryRoute       `json:"route"`
	Checkpoint   platform.SupersedeCheckpoint `json:"checkpoint"`
	Attempts     int                          `json:"attempts,omitempty"`
	NextAttempt  time.Time                    `json:"next_attempt"`
	LastError    string                       `json:"last_error,omitempty"`
	DeadLetter   bool                         `json:"dead_letter,omitempty"`
	DeadLetterAt time.Time                    `json:"dead_letter_at,omitempty"`
}

type terminalOutboxEntry struct {
	ID                   string                           `json:"id"`
	Route                platform.DeliveryRoute           `json:"route"`
	AgentName            string                           `json:"agent_name,omitempty"`
	Failed               bool                             `json:"failed,omitempty"`
	Stopped              bool                             `json:"stopped,omitempty"`
	Stream               *platform.DurableStreamReference `json:"stream,omitempty"`
	Checkpoint           *platform.TerminalCheckpoint     `json:"checkpoint,omitempty"`
	ResultTitle          string                           `json:"result_title,omitempty"`
	RichResult           bool                             `json:"rich_result,omitempty"`
	Text                 string                           `json:"text,omitempty"`
	Notification         string                           `json:"notification,omitempty"`
	Trace                *observability.TraceContext      `json:"trace,omitempty"`
	PendingSupersedes    []pendingStreamSupersede         `json:"pending_supersedes,omitempty"`
	ActiveStreamRecovery bool                             `json:"active_stream_recovery,omitempty"`

	CheckpointDelivered   bool `json:"checkpoint_delivered,omitempty"`
	TextDelivered         bool `json:"text_delivered,omitempty"`
	NotificationDelivered bool `json:"notification_delivered,omitempty"`

	Attempts     int       `json:"attempts,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	NextAttempt  time.Time `json:"next_attempt"`
	LastError    string    `json:"last_error,omitempty"`
	DeadLetter   bool      `json:"dead_letter,omitempty"`
	DeadLetterAt time.Time `json:"dead_letter_at,omitempty"`
}

type terminalOutboxDraft struct {
	Route                platform.DeliveryRoute
	AgentName            string
	Failed               bool
	Stopped              bool
	Stream               *platform.DurableStreamReference
	Checkpoint           *platform.TerminalCheckpoint
	ResultTitle          string
	RichResult           bool
	Text                 string
	Notification         string
	Trace                observability.TraceContext
	ActiveStreamRecovery bool
}

type terminalOutbox struct {
	mu           sync.Mutex
	path         string
	registry     *platform.Registry
	entries      []*terminalOutboxEntry
	preparing    map[string]bool
	followerHeld map[string]bool
	releaseHeld  map[string]bool
	releaseBusy  map[string]bool
	processing   map[string]bool
	wake         chan struct{}
	now          func() time.Time
	trace        observability.Recorder
	maxEntries   int
	maxAttempts  int
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
	followers, releases := h.ensureCodexSessions().followerRecoverySnapshots()
	outbox.holdCodexFollowerRecoveries(followers)
	h.recoverReleasedCodexFollowerStreamsForTargets(outbox, committedCodexReleaseTargets(releases))
	go outbox.run(ctx)
	return nil
}

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

func (h *Handler) currentTerminalOutbox() *terminalOutbox {
	h.terminalOutboxMu.RLock()
	defer h.terminalOutboxMu.RUnlock()
	return h.terminalOutbox
}

// TerminalOutboxStatus 返回运行中 outbox 的脱敏状态。
func (h *Handler) TerminalOutboxStatus(context.Context) (TerminalOutboxStatus, error) {
	outbox := h.currentTerminalOutbox()
	if outbox == nil {
		return TerminalOutboxStatus{}, ErrTerminalOutboxUnavailable
	}
	return outbox.status(), nil
}

// RedriveTerminalOutbox 将指定或全部待投递项提前到当前时间，并唤醒 worker。
func (h *Handler) RedriveTerminalOutbox(_ context.Context, id string) (TerminalOutboxRedriveResult, error) {
	outbox := h.currentTerminalOutbox()
	if outbox == nil {
		return TerminalOutboxRedriveResult{}, ErrTerminalOutboxUnavailable
	}
	return outbox.redrive(id)
}

// InspectTerminalOutbox 只读检查磁盘状态，供服务停止时的 CLI 和 doctor 使用。
func InspectTerminalOutbox(path string) (TerminalOutboxStatus, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return TerminalOutboxStatus{}, fmt.Errorf("terminal outbox path is empty")
	}
	entries, err := loadTerminalOutbox(path)
	if err != nil {
		return TerminalOutboxStatus{}, err
	}
	return terminalOutboxStatus(entries, nil, nil), nil
}

func newTerminalOutbox(path string, registry *platform.Registry, traceRecorders ...observability.Recorder) (*terminalOutbox, error) {
	outbox := &terminalOutbox{
		path: path, registry: registry,
		preparing: make(map[string]bool), followerHeld: make(map[string]bool),
		releaseHeld: make(map[string]bool), releaseBusy: make(map[string]bool), processing: make(map[string]bool),
		wake: make(chan struct{}, 1), now: time.Now,
		maxEntries: terminalOutboxMaxEntries, maxAttempts: terminalOutboxMaxAttempts,
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
		log.Printf("[terminal-outbox] delivery pending id=%s platform=%s: %s",
			entry.ID, entry.Route.Platform, observability.SanitizeText(err.Error()))
	}
	o.signal()
	return nil
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
	entry := &terminalOutboxEntry{
		ID: uuid.NewString(), Route: draft.Route,
		AgentName: strings.TrimSpace(draft.AgentName), Failed: draft.Failed, Stopped: draft.Stopped,
		Stream: cloneDurableStreamReference(draft.Stream), Checkpoint: draft.Checkpoint,
		ResultTitle: strings.TrimSpace(draft.ResultTitle), RichResult: draft.RichResult,
		Text: draft.Text, Notification: draft.Notification,
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
	o.mu.Lock()
	defer o.mu.Unlock()
	entry := o.entryLocked(id)
	if entry == nil {
		return ErrTerminalOutboxNotFound
	}
	before := cloneTerminalOutboxEntry(entry)
	entry.Route = draft.Route
	entry.AgentName = strings.TrimSpace(draft.AgentName)
	entry.Failed = draft.Failed
	entry.Stopped = draft.Stopped
	entry.ResultTitle = strings.TrimSpace(draft.ResultTitle)
	entry.RichResult = draft.RichResult
	entry.Text = draft.Text
	entry.Notification = draft.Notification
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
	if err := validateTerminalOutboxEntry(entry); err != nil {
		*entry = *before
		return err
	}
	if err := o.persistLocked(); err != nil {
		*entry = *before
		return fmt.Errorf("persist staged terminal result: %w", err)
	}
	return nil
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
	before := cloneTerminalOutboxEntry(entry)
	entry.Route = draft.Route
	entry.AgentName = strings.TrimSpace(draft.AgentName)
	entry.Failed = draft.Failed
	entry.Stopped = draft.Stopped
	nextStream := cloneDurableStreamReference(draft.Stream)
	if nextStream == nil && draft.Checkpoint == nil {
		nextStream = cloneDurableStreamReference(entry.Stream)
	}
	entry.Stream = nextStream
	entry.Checkpoint = cloneTerminalCheckpoint(draft.Checkpoint)
	entry.ResultTitle = strings.TrimSpace(draft.ResultTitle)
	entry.RichResult = draft.RichResult
	entry.Text = draft.Text
	entry.Notification = draft.Notification
	entry.ActiveStreamRecovery = false
	entry.UpdatedAt = o.now()
	entry.NextAttempt = entry.UpdatedAt
	entry.LastError = ""
	entry.DeadLetter = false
	entry.DeadLetterAt = time.Time{}
	entry.CheckpointDelivered = false
	entry.TextDelivered = false
	entry.NotificationDelivered = false
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

func (o *terminalOutbox) discardReservation(id string) error {
	o.mu.Lock()
	for index, entry := range o.entries {
		if entry.ID != id {
			continue
		}
		if len(entry.PendingSupersedes) > 0 {
			before := cloneTerminalOutboxEntry(entry)
			wasPreparing := o.preparing[id]
			entry.Stream = nil
			entry.Checkpoint = nil
			entry.Text = ""
			entry.Notification = ""
			entry.ActiveStreamRecovery = false
			entry.CheckpointDelivered = true
			entry.TextDelivered = true
			entry.NotificationDelivered = true
			entry.UpdatedAt = o.now()
			entry.LastError = ""
			entry.DeadLetter = false
			entry.DeadLetterAt = time.Time{}
			delete(o.preparing, id)
			if err := validateTerminalOutboxEntry(entry); err != nil {
				*entry = *before
				if wasPreparing {
					o.preparing[id] = true
				}
				o.mu.Unlock()
				return err
			}
			if err := o.persistLocked(); err != nil {
				*entry = *before
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
		previousEntries := o.entries
		wasPreparing := o.preparing[id]
		remaining := make([]*terminalOutboxEntry, 0, len(o.entries)-1)
		remaining = append(remaining, o.entries[:index]...)
		remaining = append(remaining, o.entries[index+1:]...)
		o.entries = remaining
		delete(o.preparing, id)
		if err := o.persistLocked(); err != nil {
			o.entries = previousEntries
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
	o.mu.Lock()
	entry := o.entryLocked(id)
	if entry == nil {
		o.mu.Unlock()
		return ErrTerminalOutboxNotFound
	}
	before := cloneTerminalOutboxEntry(entry)
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
	if err := validateTerminalOutboxEntry(entry); err != nil {
		*entry = *before
		o.mu.Unlock()
		return err
	}
	if err := o.persistLocked(); err != nil {
		*entry = *before
		o.mu.Unlock()
		return fmt.Errorf("persist active stream recovery: %w", err)
	}
	o.mu.Unlock()
	return nil
}

func (o *terminalOutbox) refreshStreamReservationTrace(id string, trace observability.TraceContext) error {
	if strings.TrimSpace(trace.TraceID) == "" {
		return nil
	}
	trace.RouteKey = ""
	o.mu.Lock()
	defer o.mu.Unlock()
	entry := o.entryLocked(id)
	if entry == nil {
		return ErrTerminalOutboxNotFound
	}
	before := cloneTerminalOutboxEntry(entry)
	entry.Trace = &trace
	entry.UpdatedAt = o.now()
	if err := validateTerminalOutboxEntry(entry); err != nil {
		*entry = *before
		return err
	}
	if err := o.persistLocked(); err != nil {
		*entry = *before
		return fmt.Errorf("persist active stream recovery trace: %w", err)
	}
	return nil
}

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
	before := cloneTerminalOutboxEntry(entry)
	entry.Route = newRoute
	entry.Stream = cloneDurableStreamReference(&newReference)
	entry.ActiveStreamRecovery = true
	entry.Checkpoint = nil
	entry.CheckpointDelivered = false
	entry.TextDelivered = false
	entry.NotificationDelivered = false
	entry.UpdatedAt = o.now()
	entry.NextAttempt = entry.UpdatedAt
	entry.Attempts = 0
	entry.LastError = ""
	entry.DeadLetter = false
	entry.DeadLetterAt = time.Time{}
	if !duplicate {
		entry.PendingSupersedes = append(entry.PendingSupersedes, clonePendingStreamSupersede(pending))
	}
	if err := validateTerminalOutboxEntry(entry); err != nil {
		*entry = *before
		o.mu.Unlock()
		return err
	}
	if err := o.persistLocked(); err != nil {
		*entry = *before
		o.mu.Unlock()
		return fmt.Errorf("persist reanchored stream recovery: %w", err)
	}
	committed := cloneTerminalOutboxEntry(entry)
	o.mu.Unlock()
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
	before := cloneTerminalOutboxEntry(entry)
	wasPreparing := o.preparing[id]
	wasFollowerHeld := o.followerHeld[id]
	wasReleaseHeld := o.releaseHeld[id]
	wasReleaseBusy := o.releaseBusy[id]
	entry.Stream = nil
	entry.Checkpoint = nil
	entry.Text = ""
	entry.Notification = ""
	entry.ActiveStreamRecovery = false
	entry.CheckpointDelivered = true
	entry.TextDelivered = true
	entry.NotificationDelivered = true
	entry.Attempts = 0
	entry.UpdatedAt = o.now()
	entry.NextAttempt = entry.UpdatedAt
	entry.LastError = ""
	entry.DeadLetter = false
	entry.DeadLetterAt = time.Time{}
	entry.PendingSupersedes = append(entry.PendingSupersedes, clonePendingStreamSupersede(pending))
	delete(o.preparing, id)
	delete(o.followerHeld, id)
	delete(o.releaseHeld, id)
	delete(o.releaseBusy, id)
	if err := validateTerminalOutboxEntry(entry); err != nil {
		*entry = *before
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
	if err := o.persistLocked(); err != nil {
		*entry = *before
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
		return fmt.Errorf("persist detached stream recovery: %w", err)
	}
	committed := cloneTerminalOutboxEntry(entry)
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
		if o.processing[entry.ID] {
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

func (o *terminalOutbox) nextAttemptDelay() (time.Duration, bool) {
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	var next time.Time
	for _, entry := range o.entries {
		if !o.processing[entry.ID] {
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
	if o.processing[entryID] {
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
	before := cloneTerminalOutboxEntry(entry)
	pending := &entry.PendingSupersedes[index]
	pending.Attempts++
	entry.UpdatedAt = o.now()
	pending.LastError = truncateTerminalOutboxError(deliveryErr)
	if pending.Attempts >= o.attemptLimit() {
		pending.DeadLetter = true
		pending.DeadLetterAt = entry.UpdatedAt
		pending.NextAttempt = entry.UpdatedAt
	} else {
		pending.NextAttempt = entry.UpdatedAt.Add(terminalOutboxBackoff(pending.Attempts))
	}
	if err := validateTerminalOutboxEntry(entry); err != nil {
		*entry = *before
		o.mu.Unlock()
		return fmt.Errorf("supersede delivery failed: %v; validate retry state: %w", deliveryErr, err)
	}
	if err := o.persistLocked(); err != nil {
		*entry = *before
		o.mu.Unlock()
		return fmt.Errorf("supersede delivery failed: %v; persist retry state: %w", deliveryErr, err)
	}
	clone := cloneTerminalOutboxEntry(entry)
	deadLetter := pending.DeadLetter
	o.mu.Unlock()
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
	entry, ok := o.beginAttempt(id)
	if !ok {
		return nil
	}
	defer o.endAttempt(id)
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
				err = o.markDelivered(id, stage)
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
		resultKey := id + ":text"
		if entry.RichResult {
			resultKey = id + ":result"
		}
		startStage(terminalOutboxTextStage, func(ctx context.Context, reply platform.Replier) error {
			return sendOutboxResult(ctx, reply, entry, text, resultKey)
		})
	}
	if strings.TrimSpace(entry.Notification) != "" && !entry.NotificationDelivered {
		notification := entry.Notification
		startStage(terminalOutboxNotificationStage, func(ctx context.Context, reply platform.Replier) error {
			return sendOutboxText(ctx, reply, notification, id+":notification")
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

func (o *terminalOutbox) prepareStreamRecovery(id string, entry *terminalOutboxEntry, reply platform.Replier) (*terminalOutboxEntry, error) {
	if entry == nil || entry.Stream == nil {
		return entry, nil
	}
	content := firstNonBlank(entry.Text, "任务已中断。WeClaw 服务在任务执行期间发生重启。")
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

	o.mu.Lock()
	current := o.entryLocked(id)
	if current == nil {
		o.mu.Unlock()
		return entry, ErrTerminalOutboxNotFound
	}
	before := cloneTerminalOutboxEntry(current)
	current.Stream = nil
	current.Checkpoint = cloneTerminalCheckpoint(&checkpoint)
	current.UpdatedAt = o.now()
	current.NextAttempt = current.UpdatedAt
	if err := validateTerminalOutboxEntry(current); err != nil {
		*current = *before
		o.mu.Unlock()
		return entry, err
	}
	if err := o.persistLocked(); err != nil {
		*current = *before
		o.mu.Unlock()
		return entry, fmt.Errorf("persist recovered terminal checkpoint: %w", err)
	}
	prepared := cloneTerminalOutboxEntry(current)
	o.mu.Unlock()
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
	o.mu.Lock()
	defer o.mu.Unlock()
	entry := o.entryLocked(id)
	if entry == nil {
		return nil
	}
	before := cloneTerminalOutboxEntry(entry)
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
	if err := o.persistLocked(); err != nil {
		*entry = *before
		return err
	}
	return nil
}

func (o *terminalOutbox) recordFailure(id string, deliveryErr error) error {
	o.mu.Lock()
	entry := o.entryLocked(id)
	if entry == nil {
		o.mu.Unlock()
		return deliveryErr
	}
	before := cloneTerminalOutboxEntry(entry)
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
	if err := o.persistLocked(); err != nil {
		*entry = *before
		o.mu.Unlock()
		return fmt.Errorf("delivery failed: %v; persist retry state: %w", deliveryErr, err)
	}
	clone := cloneTerminalOutboxEntry(entry)
	o.mu.Unlock()
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
			before := cloneTerminalOutboxEntry(entry)
			entry.UpdatedAt = o.now()
			entry.LastError = ""
			entry.DeadLetter = false
			entry.DeadLetterAt = time.Time{}
			if err := o.persistLocked(); err != nil {
				*entry = *before
				o.mu.Unlock()
				return err
			}
			o.mu.Unlock()
			o.recordTrace(clone, "terminal.delivery.completed", "completed", "terminal delivery committed; supersede pending")
			return nil
		}
		previous := o.entries
		remaining := make([]*terminalOutboxEntry, 0, len(o.entries)-1)
		remaining = append(remaining, o.entries[:index]...)
		remaining = append(remaining, o.entries[index+1:]...)
		o.entries = remaining
		err := o.persistLocked()
		if err != nil {
			o.entries = previous
		}
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

var syncTerminalOutboxDirectory = func(dir string) error {
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(dirFile.Sync(), dirFile.Close())
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
	// Rename is the commit point. Keep in-memory state aligned with the file
	// already visible at path even when the directory metadata cannot be synced.
	if err := syncTerminalOutboxDirectory(dir); err != nil {
		log.Printf("[terminal-outbox] state file replaced but parent directory sync failed: %v", err)
	}
	return nil
}
