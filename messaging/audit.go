package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
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
	Log(entry auditEntry) error
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

func (l *fileAuditLogger) Log(entry auditEntry) error {
	if l == nil {
		return nil
	}
	if entry.Time == "" {
		entry.Time = l.now().UTC().Format(time.RFC3339)
	}
	entry.Summary = auditSanitizeSummary(entry.Summary)
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal audit entry: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := securefile.OpenAppend(l.path)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat audit log: %w", err)
	}
	if l.maxBytes > 0 && info.Size()+int64(len(data)+1) > l.maxBytes {
		if err := f.Close(); err != nil {
			return fmt.Errorf("close audit log before rotation: %w", err)
		}
		if err := l.rotateLocked(); err != nil {
			return err
		}
		f, err = securefile.OpenAppend(l.path)
		if err != nil {
			return fmt.Errorf("open rotated audit log: %w", err)
		}
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write audit log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close audit log: %w", err)
	}
	return nil
}

// rotateLocked 按 path→path.1→path.2… 轮转，调用方必须持有 l.mu。
func (l *fileAuditLogger) rotateLocked() error {
	// 从最旧往新挪：.(n-1)→.n，最终 path→.1
	oldest := fmt.Sprintf("%s.%d", l.path, l.backups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove oldest audit backup: %w", err)
	}
	for i := l.backups - 1; i >= 1; i-- {
		if err := os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate audit backup: %w", err)
		}
	}
	if l.backups >= 1 {
		if err := os.Rename(l.path, l.path+".1"); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate audit log: %w", err)
		}
	} else {
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove rotated audit log: %w", err)
		}
	}
	return nil
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
