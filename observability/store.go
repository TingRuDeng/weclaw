package observability

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/google/uuid"
)

const (
	traceFileName           = "trace.jsonl"
	defaultTraceMaxBytes    = 10 << 20
	defaultTraceBackups     = 3
	defaultTraceQueryLimit  = 100
	maxTraceQueryLimit      = 1000
	maxTraceLineBytes       = 256 << 10
	maxProtocolPayloadRunes = 32 << 10
	maxProtocolCorrelations = 4096
)

// Recorder 是运行时各层唯一依赖的 best-effort Trace 写入接口。
type Recorder interface {
	Record(Event) error
}

// QueryProvider 供本机 API 和 CLI 查询已落盘的诊断事件。
type QueryProvider interface {
	Query(context.Context, Query) (Page, error)
	Status() Status
}

type Query struct {
	TraceID   string    `json:"trace_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	ThreadID  string    `json:"thread_id,omitempty"`
	TurnID    string    `json:"turn_id,omitempty"`
	Stage     string    `json:"stage,omitempty"`
	Since     time.Time `json:"since,omitempty"`
	Limit     int       `json:"limit,omitempty"`
}

type Page struct {
	Events    []Event `json:"events"`
	Truncated bool    `json:"truncated,omitempty"`
}

type Status struct {
	Enabled                bool   `json:"enabled"`
	Writable               bool   `json:"writable"`
	ProtocolPayloadEnabled bool   `json:"protocol_payload_enabled"`
	LastError              string `json:"last_error,omitempty"`
}

type StoreOptions struct {
	Path                   string
	MaxBytes               int64
	Backups                int
	IncludeProtocolPayload bool
	Now                    func() time.Time
}

// Store 以单进程互斥的 JSONL 记录 Trace；在线 CLI 必须通过本机 API 查询。
type Store struct {
	mu                     sync.Mutex
	path                   string
	maxBytes               int64
	backups                int
	includeProtocolPayload bool
	now                    func() time.Time
	lastErr                error
	requestTraces          map[string]TraceContext
	requestMethods         map[string]string
	requestOrder           []string
	threadTraces           map[string]TraceContext
	turnTraces             map[string]TraceContext
	conversationOrder      []protocolCorrelationRef
}

// DefaultPath 返回主机级 Trace 文件，不与终态 outbox 混用 schema 或生命周期。
func DefaultPath() string {
	dataDir, err := config.DataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dataDir, "state", traceFileName)
}

func NewStore(options StoreOptions) (*Store, error) {
	path := strings.TrimSpace(options.Path)
	if path == "" {
		return nil, fmt.Errorf("trace path is empty")
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = defaultTraceMaxBytes
	}
	if options.Backups < 0 {
		return nil, fmt.Errorf("trace backups must not be negative")
	}
	if options.Backups == 0 {
		options.Backups = defaultTraceBackups
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if err := prepareTracePath(path); err != nil {
		return nil, err
	}
	return &Store{
		path: path, maxBytes: options.MaxBytes, backups: options.Backups,
		includeProtocolPayload: options.IncludeProtocolPayload, now: options.Now,
		requestTraces: make(map[string]TraceContext), requestMethods: make(map[string]string),
		threadTraces: make(map[string]TraceContext), turnTraces: make(map[string]TraceContext),
	}, nil
}

func prepareTracePath(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create trace directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect trace directory: %w", err)
	}
	if err := validateTraceDirectoryInfo(dirInfo); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect trace file: %w", err)
	}
	return validateTraceFileInfo(info)
}

func validateTraceFileInfo(info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("trace file must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("trace file permissions must be 0600: %o", info.Mode().Perm())
	}
	return validateTraceOwner(info)
}

func validateTraceDirectoryInfo(info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("trace directory must be a real directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("trace directory permissions must be 0700: %o", info.Mode().Perm())
	}
	return validateTraceOwner(info)
}

// Record 持久化一条固定字段事件；失败由调用方降级为日志，不影响业务终态。
func (store *Store) Record(event Event) error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if strings.TrimSpace(event.Stage) == "" {
		return fmt.Errorf("trace stage is empty")
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = store.now().UTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	event.Summary = SanitizeText(event.Summary)
	event.Payload = truncateProtocolPayload(event.Payload)
	data, err := json.Marshal(event)
	if err != nil {
		store.lastErr = err
		return err
	}
	if len(data) > maxTraceLineBytes {
		err := fmt.Errorf("trace event exceeds %d bytes", maxTraceLineBytes)
		store.lastErr = err
		return err
	}
	if err := appendTraceLineNoFollow(store.path, append(data, '\n'), store.maxBytes, store.backups); err != nil {
		store.lastErr = err
		return err
	}
	store.lastErr = nil
	return nil
}

func truncateProtocolPayload(payload string) string {
	runes := []rune(strings.TrimSpace(payload))
	if len(runes) <= maxProtocolPayloadRunes {
		return string(runes)
	}
	return string(runes[:maxProtocolPayloadRunes]) + "…"
}

func (store *Store) Query(ctx context.Context, query Query) (Page, error) {
	if store == nil {
		return Page{}, nil
	}
	store.mu.Lock()
	path, backups := store.path, store.backups
	store.mu.Unlock()
	return QueryFiles(ctx, path, backups, query)
}

// QueryFiles 以只读方式扫描轮转文件，供服务停止时的 CLI 使用。
func QueryFiles(ctx context.Context, path string, backups int, query Query) (Page, error) {
	query = normalizeQuery(query)
	paths := make([]string, 0, backups+1)
	for index := backups; index >= 1; index-- {
		paths = append(paths, fmt.Sprintf("%s.%d", path, index))
	}
	paths = append(paths, path)
	events := make([]Event, 0, query.Limit)
	truncated := false
	for _, candidate := range paths {
		if err := scanTraceFile(ctx, candidate, query, func(event Event) {
			if len(events) == query.Limit {
				copy(events, events[1:])
				events[len(events)-1] = event
				truncated = true
				return
			}
			events = append(events, event)
		}); err != nil {
			return Page{}, err
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].CreatedAt.Before(events[j].CreatedAt) })
	return Page{Events: events, Truncated: truncated}, nil
}

// QueryPath 使用默认轮转数量只读查询指定 Trace 主文件。
func QueryPath(ctx context.Context, path string, query Query) (Page, error) {
	return QueryFiles(ctx, path, defaultTraceBackups, query)
}

func normalizeQuery(query Query) Query {
	query.TraceID = strings.TrimSpace(query.TraceID)
	query.MessageID = strings.TrimSpace(query.MessageID)
	query.TaskID = strings.TrimSpace(query.TaskID)
	query.ThreadID = strings.TrimSpace(query.ThreadID)
	query.TurnID = strings.TrimSpace(query.TurnID)
	query.Stage = strings.TrimSpace(query.Stage)
	if query.Limit <= 0 {
		query.Limit = defaultTraceQueryLimit
	}
	if query.Limit > maxTraceQueryLimit {
		query.Limit = maxTraceQueryLimit
	}
	return query
}

func scanTraceFile(ctx context.Context, path string, query Query, consume func(Event)) error {
	file, err := openTraceFileNoFollow(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxTraceLineBytes)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode trace %s: %w", path, err)
		}
		if traceEventMatches(event, query) {
			consume(event)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func traceEventMatches(event Event, query Query) bool {
	if query.TraceID != "" && event.TraceID != query.TraceID ||
		query.MessageID != "" && event.MessageID != query.MessageID ||
		query.TaskID != "" && event.TaskID != query.TaskID ||
		query.ThreadID != "" && event.ThreadID != query.ThreadID ||
		query.TurnID != "" && event.TurnID != query.TurnID ||
		query.Stage != "" && event.Stage != query.Stage {
		return false
	}
	return query.Since.IsZero() || !event.CreatedAt.Before(query.Since)
}

func (store *Store) Status() Status {
	if store == nil {
		return Status{}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	status := Status{Enabled: true, Writable: store.lastErr == nil, ProtocolPayloadEnabled: store.includeProtocolPayload}
	if store.lastErr != nil {
		status.LastError = SanitizeText(store.lastErr.Error())
	}
	return status
}
