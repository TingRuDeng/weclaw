package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const auditSummaryRunes = 200

// 审计文件按大小轮转，避免长期运行无限增长。
const (
	auditMaxBytes = 10 << 20 // 单文件 10 MiB
	auditBackups  = 3        // 保留 .1/.2/.3 共 3 个历史文件
)

// auditEntry 是一条结构化审计记录，仅含追责所需的元信息，绝不含密钥。
type auditEntry struct {
	Time     string `json:"time"`
	Platform string `json:"platform,omitempty"`
	User     string `json:"user"`
	Agent    string `json:"agent,omitempty"`
	Action   string `json:"action"`
	Summary  string `json:"summary,omitempty"`
}

// auditLogger 记录敏感操作以供追责。
type auditLogger interface {
	Log(entry auditEntry)
}

// fileAuditLogger 以 JSON Lines 形式把审计写入本地文件。
type fileAuditLogger struct {
	mu       sync.Mutex
	path     string
	now      func() time.Time
	maxBytes int64
	backups  int
}

func newFileAuditLogger(path string) *fileAuditLogger {
	return &fileAuditLogger{path: path, now: time.Now, maxBytes: auditMaxBytes, backups: auditBackups}
}

func (l *fileAuditLogger) Log(entry auditEntry) {
	if l == nil {
		return
	}
	if entry.Time == "" {
		entry.Time = l.now().UTC().Format(time.RFC3339)
	}
	entry.Summary = auditSanitizeSummary(entry.Summary)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return
	}
	l.rotateIfNeeded(int64(len(data) + 1))
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// rotateIfNeeded 在写入将使文件超过上限时轮转：path→path.1→path.2…，超出 backups 的丢弃。
func (l *fileAuditLogger) rotateIfNeeded(incoming int64) {
	if l.maxBytes <= 0 {
		return
	}
	info, err := os.Stat(l.path)
	if err != nil || info.Size()+incoming <= l.maxBytes {
		return
	}
	// 从最旧往新挪：.(n-1)→.n，最终 path→.1
	oldest := fmt.Sprintf("%s.%d", l.path, l.backups)
	_ = os.Remove(oldest)
	for i := l.backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1))
	}
	if l.backups >= 1 {
		_ = os.Rename(l.path, l.path+".1")
	} else {
		_ = os.Remove(l.path)
	}
}

// auditSanitizeSummary 截断摘要并清理换行，避免单条日志过长或被注入。
func auditSanitizeSummary(summary string) string {
	summary = strings.Join(strings.Fields(summary), " ")
	runes := []rune(summary)
	if len(runes) > auditSummaryRunes {
		return string(runes[:auditSummaryRunes]) + "…"
	}
	return summary
}

// auditMessageSummary 只记录正文规模，不把用户输入写入审计文件。
func auditMessageSummary(text string) string {
	return fmt.Sprintf("text_runes=%d", len([]rune(text)))
}

// NewFileAuditLogger 创建写本地 JSON Lines 审计文件的记录器。
func NewFileAuditLogger(path string) auditLogger {
	return newFileAuditLogger(path)
}

// DefaultAuditLogPath 返回默认审计文件路径 ~/.weclaw/audit.log。
func DefaultAuditLogPath() string {
	return filepath.Join(defaultDataDir(), "audit.log")
}
