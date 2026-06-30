package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const auditSummaryRunes = 200

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
	mu   sync.Mutex
	path string
	now  func() time.Time
}

func newFileAuditLogger(path string) *fileAuditLogger {
	return &fileAuditLogger{path: path, now: time.Now}
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
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
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

// NewFileAuditLogger 创建写本地 JSON Lines 审计文件的记录器。
func NewFileAuditLogger(path string) auditLogger {
	return newFileAuditLogger(path)
}

// DefaultAuditLogPath 返回默认审计文件路径 ~/.weclaw/audit.log。
func DefaultAuditLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "weclaw-audit.log")
	}
	return filepath.Join(home, ".weclaw", "audit.log")
}
