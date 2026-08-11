package messaging

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type recordingAuditLogger struct {
	mu      sync.Mutex
	entries []auditEntry
}

func (l *recordingAuditLogger) Log(entry auditEntry) error {
	l.mu.Lock()
	l.entries = append(l.entries, entry)
	l.mu.Unlock()
	return nil
}

func (l *recordingAuditLogger) snapshot() []auditEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]auditEntry(nil), l.entries...)
}

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

func TestAuditRecordLogsPersistenceFailureWithoutPropagating(t *testing.T) {
	dir := t.TempDir()
	blockedParent := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockedParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(nil, nil)
	h.SetAuditLogger(newFileAuditLogger(filepath.Join(blockedParent, "audit.log")))
	var logs strings.Builder
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	h.auditRecord(auditEntry{User: "u1", Action: "agent_message"})

	if !strings.Contains(logs.String(), "[audit] record failed") {
		t.Fatalf("logs=%q, want observable audit failure", logs.String())
	}
}

func TestServiceAdminCommandAuditsAcceptedAndResult(t *testing.T) {
	h := NewHandler(nil, nil)
	recorder := &recordingAuditLogger{}
	h.SetAuditLogger(recorder)
	h.SetServiceAdminCommandExecutor(func(context.Context, string, []string) (string, error) {
		return "Already up to date", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), authorizedAdminCommandMessage(t, platform.IncomingMessage{
		Platform: platform.PlatformWeChat, AccountID: "wx-a", UserID: "admin", Text: "/update",
	}), reply)
	reply.waitTexts(t, 2)

	entries := recorder.snapshot()
	if !auditEntriesContain(entries, "admin_command_accepted", "command=update") ||
		!auditEntriesContain(entries, "admin_command_succeeded", "command=update") {
		t.Fatalf("audit entries=%#v, want accepted and succeeded", entries)
	}
}

func TestApprovalAndStopActionsEmitAuditRecords(t *testing.T) {
	h := NewHandler(nil, nil)
	recorder := &recordingAuditLogger{}
	h.SetAuditLogger(recorder)
	options := []agent.ApprovalOption{
		{ID: "allow_once", Kind: "allow"}, {ID: "deny_once", Kind: "deny"},
	}
	card, err := h.registerPendingApprovalForRoute(
		"ou_user", "route-1", "card-key", options, "allow_once", platform.ChoiceInteractionApproval,
	)
	if err != nil {
		t.Fatal(err)
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user",
		Route: platform.SessionRoute{Key: "route-1"}, MessageID: "card-audit-1",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{
			"choice": "allow_once", "approval_key": "card-key",
			platform.ChoiceMetadataInteractionKind: platform.ChoiceInteractionApproval,
		}},
	}, reply)
	if got := waitAuditApprovalChoice(t, card); got != "allow_once" {
		t.Fatalf("card choice=%q", got)
	}
	h.clearPendingApproval("ou_user", card)

	text, err := h.registerPendingApprovalForRoute(
		"ou_user", "route-1", "text-key", options, "allow_once", platform.ChoiceInteractionApproval,
	)
	if err != nil {
		t.Fatal(err)
	}
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user",
		Route: platform.SessionRoute{Key: "route-1"}, MessageID: "text-audit-1", Text: "deny_once",
	}, reply)
	if got := waitAuditApprovalChoice(t, text); got != "deny_once" {
		t.Fatalf("text choice=%q", got)
	}

	h.defaultName = "codex"
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "cli", Command: "codex"}}}
	h.agents["codex"] = ag
	key := h.agentExecutionKey("route-1", "codex", ag)
	task, taskCtx, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{
		owner: "ou_user", routeUserID: "route-1", agentName: "codex", message: "run",
	})
	if !started {
		t.Fatal("active task not started")
	}
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user",
		Route: platform.SessionRoute{Key: "route-1"}, MessageID: "stop-audit-1",
		RawCommand: &platform.CardAction{Action: "stop"},
	}, reply)
	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel task")
	}
	h.finishActiveTask(key, task)

	entries := recorder.snapshot()
	if !auditEntriesContain(entries, "approval_card_decision", "decision=allow") ||
		!auditEntriesContain(entries, "approval_text_decision", "decision=deny") ||
		!auditEntriesContain(entries, "task_stop", "outcome=accepted") {
		t.Fatalf("audit entries=%#v", entries)
	}
}

func TestApprovalDefaultDenyOnContextCancellationEmitsAuditRecord(t *testing.T) {
	h := NewHandler(nil, nil)
	recorder := &recordingAuditLogger{}
	h.SetAuditLogger(recorder)
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := h.approvalHandlerForRoute(agentInteractionContextOptions{
			actorUserID: "ou_user", routeUserID: "feishu:tenant:dm:chat:ou_user", agentName: "codex",
			reply: platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true}),
		})(ctx, agent.ApprovalRequest{Options: []agent.ApprovalOption{
			{ID: "allow_once", Kind: "allow"}, {ID: "deny_once", Kind: "deny"},
		}})
		resultCh <- err
	}()

	deadline := time.Now().Add(time.Second)
	for !hasPendingApprovalForTest(h, "ou_user") {
		if time.Now().After(deadline) {
			t.Fatal("approval was not registered")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-resultCh:
		if err == nil {
			t.Fatal("context cancellation should be returned to the caller")
		}
	case <-time.After(time.Second):
		t.Fatal("approval handler did not return after cancellation")
	}
	if !auditEntriesContain(recorder.snapshot(), "approval_default_deny", "reason=context_cancelled") {
		t.Fatalf("audit entries=%#v, want default-deny cancellation record", recorder.snapshot())
	}
}

func TestFeishuIdentityMutationsAuditTargetWithoutAuthorizationCode(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	h := newFeishuIdentityCommandHandler(t)
	recorder := &recordingAuditLogger{}
	h.SetAuditLogger(recorder)
	h.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	record, ok := h.ensureFeishuIdentities().IssueAuthCode("on_same_person", time.Now().UTC())
	if !ok {
		t.Fatal("IssueAuthCode ok=false")
	}
	reply := newAdminCommandTestReplier()
	h.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve-code "+record.AuthCode), reply)
	reply.waitTexts(t, 1)
	h.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users revoke on_same_person"), reply)
	reply.waitTexts(t, 2)

	entries := recorder.snapshot()
	if !auditEntriesContain(entries, "feishu_identity_approve", "target=on_same_person") ||
		!auditEntriesContain(entries, "feishu_identity_revoke", "target=on_same_person") {
		t.Fatalf("audit entries=%#v", entries)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Summary, record.AuthCode) {
			t.Fatalf("audit leaked authorization code: %#v", entry)
		}
	}
}

func auditEntriesContain(entries []auditEntry, action string, summaryPart string) bool {
	for _, entry := range entries {
		if entry.Action == action && strings.Contains(entry.Summary, summaryPart) {
			return true
		}
	}
	return false
}

func waitAuditApprovalChoice(t *testing.T, pending *pendingApproval) string {
	t.Helper()
	select {
	case choice := <-pending.choices:
		return choice
	case <-time.After(time.Second):
		t.Fatal("approval choice was not delivered")
		return ""
	}
}
