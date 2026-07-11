package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileAuditLoggerRotatesBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l := newFileAuditLogger(path)
	l.maxBytes = 200 // 极小阈值便于触发轮转
	l.backups = 2

	for i := 0; i < 50; i++ {
		l.Log(auditEntry{User: "u1", Action: "agent_message", Summary: strings.Repeat("x", 40)})
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active log should exist: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated backup .1: %v", err)
	}
	// 不应超过 backups 数量
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Fatal("backups beyond configured count must be discarded")
	}
	// 活动文件应小于阈值的合理范围（单条 + 一点冗余）
	info, _ := os.Stat(path)
	if info.Size() > l.maxBytes*2 {
		t.Fatalf("active log not rotated, size=%d", info.Size())
	}
}

func TestFileAuditLoggerWritesJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l := newFileAuditLogger(path)
	l.Log(auditEntry{Platform: "wechat", User: "u1", Agent: "codex", Action: "agent_message", Summary: "  重构   登录\n模块  "})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	line := strings.TrimSpace(string(data))
	var entry auditEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("audit line not valid json: %v (%q)", err, line)
	}
	if entry.User != "u1" || entry.Action != "agent_message" || entry.Platform != "wechat" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.Summary != "重构 登录 模块" {
		t.Fatalf("summary not normalized: %q", entry.Summary)
	}
	if entry.Time == "" {
		t.Fatal("time should be auto-filled")
	}
}

func TestAuditSummaryTruncated(t *testing.T) {
	long := strings.Repeat("字", auditSummaryRunes+50)
	got := auditSanitizeSummary(long)
	if r := []rune(got); len(r) != auditSummaryRunes+1 { // +1 for the ellipsis
		t.Fatalf("expected truncation to %d+ellipsis runes, got %d", auditSummaryRunes, len(r))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("truncated summary should end with ellipsis")
	}
}

func TestAgentMessageAuditSummaryContainsOnlyMetadata(t *testing.T) {
	got := auditMessageSummary("top-secret-message")
	if strings.Contains(got, "top-secret-message") {
		t.Fatalf("audit summary contains message body: %q", got)
	}
	if got != "text_runes=18" {
		t.Fatalf("audit summary=%q, want rune count metadata", got)
	}
}
