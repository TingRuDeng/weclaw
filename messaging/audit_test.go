package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
