package messaging

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
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
	ReplaySelectionID    string                           `json:"replay_selection_id,omitempty"`
	ReplayTurnID         string                           `json:"replay_turn_id,omitempty"`
	ID                   string                           `json:"id"`
	Route                platform.DeliveryRoute           `json:"route"`
	AgentName            string                           `json:"agent_name,omitempty"`
	AuthorizedIdentity   string                           `json:"authorized_identity,omitempty"`
	FollowerBindingKey   string                           `json:"follower_binding_key,omitempty"`
	FollowerRevision     uint64                           `json:"follower_revision,omitempty"`
	FollowerThreadID     string                           `json:"follower_thread_id,omitempty"`
	Failed               bool                             `json:"failed,omitempty"`
	Stopped              bool                             `json:"stopped,omitempty"`
	Stream               *platform.DurableStreamReference `json:"stream,omitempty"`
	Checkpoint           *platform.TerminalCheckpoint     `json:"checkpoint,omitempty"`
	ResultTitle          string                           `json:"result_title,omitempty"`
	RichResult           bool                             `json:"rich_result,omitempty"`
	Text                 string                           `json:"text,omitempty"`
	Notification         string                           `json:"notification,omitempty"`
	IdempotencyKey       string                           `json:"idempotency_key,omitempty"`
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
	ReplaySelectionID    string
	ReplayTurnID         string
	ID                   string
	Route                platform.DeliveryRoute
	AgentName            string
	AuthorizedIdentity   string
	FollowerBindingKey   string
	FollowerRevision     uint64
	FollowerThreadID     string
	Failed               bool
	Stopped              bool
	Stream               *platform.DurableStreamReference
	Checkpoint           *platform.TerminalCheckpoint
	ResultTitle          string
	RichResult           bool
	Text                 string
	Notification         string
	IdempotencyKey       string
	Trace                observability.TraceContext
	ActiveStreamRecovery bool
}

type terminalDeliveryGuard struct {
	AuthorizedIdentity string
	FollowerBindingKey string
	FollowerRevision   uint64
	FollowerThreadID   string
}

func (g terminalDeliveryGuard) empty() bool {
	return strings.TrimSpace(g.AuthorizedIdentity) == "" && strings.TrimSpace(g.FollowerBindingKey) == "" &&
		g.FollowerRevision == 0 && strings.TrimSpace(g.FollowerThreadID) == ""
}

func (g terminalDeliveryGuard) complete() bool {
	return strings.TrimSpace(g.AuthorizedIdentity) != "" && strings.TrimSpace(g.FollowerBindingKey) != "" &&
		g.FollowerRevision != 0 && strings.TrimSpace(g.FollowerThreadID) != ""
}

func terminalDeliveryGuardFromDraft(draft terminalOutboxDraft) terminalDeliveryGuard {
	return terminalDeliveryGuard{
		AuthorizedIdentity: strings.TrimSpace(draft.AuthorizedIdentity),
		FollowerBindingKey: strings.TrimSpace(draft.FollowerBindingKey),
		FollowerRevision:   draft.FollowerRevision,
		FollowerThreadID:   strings.TrimSpace(draft.FollowerThreadID),
	}
}

func terminalDeliveryGuardFromEntry(entry *terminalOutboxEntry) terminalDeliveryGuard {
	if entry == nil {
		return terminalDeliveryGuard{}
	}
	return terminalDeliveryGuard{
		AuthorizedIdentity: strings.TrimSpace(entry.AuthorizedIdentity),
		FollowerBindingKey: strings.TrimSpace(entry.FollowerBindingKey),
		FollowerRevision:   entry.FollowerRevision,
		FollowerThreadID:   strings.TrimSpace(entry.FollowerThreadID),
	}
}

func mergeTerminalDeliveryGuard(entry *terminalOutboxEntry, draft terminalOutboxDraft) error {
	guard := terminalDeliveryGuardFromDraft(draft)
	if guard.empty() {
		return nil
	}
	if !guard.complete() {
		return fmt.Errorf("incomplete terminal follower delivery guard")
	}
	entry.AuthorizedIdentity = guard.AuthorizedIdentity
	entry.FollowerBindingKey = guard.FollowerBindingKey
	entry.FollowerRevision = guard.FollowerRevision
	entry.FollowerThreadID = guard.FollowerThreadID
	return nil
}

func (g terminalDeliveryGuard) apply(draft *terminalOutboxDraft) {
	if draft == nil {
		return
	}
	draft.AuthorizedIdentity = strings.TrimSpace(g.AuthorizedIdentity)
	draft.FollowerBindingKey = strings.TrimSpace(g.FollowerBindingKey)
	draft.FollowerRevision = g.FollowerRevision
	draft.FollowerThreadID = strings.TrimSpace(g.FollowerThreadID)
}

type terminalOutbox struct {
	mu               sync.Mutex
	path             string
	registry         *platform.Registry
	entries          []*terminalOutboxEntry
	preparing        map[string]bool
	followerHeld     map[string]bool
	startupHeld      map[string]bool
	releaseHeld      map[string]bool
	releaseBusy      map[string]bool
	processing       map[string]bool
	wake             chan struct{}
	now              func() time.Time
	syncDirectory    func(string) error
	trace            observability.Recorder
	maxEntries       int
	maxAttempts      int
	deliveryDecision func(*terminalOutboxEntry) (bool, string)
	deliveryBarrier  *sync.RWMutex
	replayDelivered  func(*terminalOutboxEntry) error
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
	outbox.deliveryDecision = func(entry *terminalOutboxEntry) (bool, string) {
		return h.terminalOutboxDeliveryDecision(registry, entry)
	}
	outbox.deliveryBarrier = &h.codexFollowerDeliveryMu
	outbox.replayDelivered = h.commitCodexReplayDelivered
	h.terminalOutboxMu.Lock()
	if h.terminalOutbox != nil {
		h.terminalOutboxMu.Unlock()
		return fmt.Errorf("terminal outbox already started")
	}
	h.terminalOutbox = outbox
	h.terminalOutboxMu.Unlock()
	followers, releases := h.ensureCodexSessions().followerRecoverySnapshots()
	outbox.holdCodexFollowerTerminalDeliveries(followers)
	outbox.holdCodexFollowerRecoveries(followers)
	h.recoverReleasedCodexFollowerStreamsForTargets(outbox, committedCodexReleaseTargets(releases))
	go outbox.run(ctx)
	return nil
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
		preparing: make(map[string]bool), followerHeld: make(map[string]bool), startupHeld: make(map[string]bool),
		releaseHeld: make(map[string]bool), releaseBusy: make(map[string]bool), processing: make(map[string]bool),
		wake: make(chan struct{}, 1), now: time.Now, syncDirectory: syncTerminalOutboxDirectory,
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
