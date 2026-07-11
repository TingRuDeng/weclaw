package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestHandleClaudeCliOpensCurrentSession(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	h.claudeSessions.setSession(claudeBindingKey("user-1", "claude"), workspace, "session-current")
	var opened []recordedClaudeCLIResume
	h.SetClaudeCLIResumeOpener(func(_ context.Context, command string, workspaceRoot string, sessionID string) error {
		opened = append(opened, recordedClaudeCLIResume{command: command, workspace: workspaceRoot, sessionID: sessionID})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(303, "/cc cli"))

	if len(opened) != 1 || opened[0].workspace != workspace || opened[0].sessionID != "session-current" {
		t.Fatalf("opened=%#v, want current session in workspace %s", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Claude CLI") {
		t.Fatalf("reply should mention opened cli, messages=%#v", calls.texts())
	}
}

func writeLocalClaudeSession(t *testing.T, claudeDir string, sessionID string, workspace string, title string, updatedAt string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create local claude workspace: %v", err)
	}
	writeLocalClaudeProjectConfig(t, claudeDir, workspace)
	writeLocalClaudeTranscript(t, claudeDir, workspace, sessionID, title, updatedAt)
}

func writeLocalClaudeProjectConfig(t *testing.T, claudeDir string, workspace string) {
	t.Helper()
	configPath := filepath.Join(claudeDir, "claude.json")
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read claude config: %v", err)
	}
	var cfg struct {
		Projects map[string]map[string]interface{} `json:"projects"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse claude config: %v", err)
		}
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]map[string]interface{}{}
	}
	cfg.Projects[workspace] = map[string]interface{}{"hasTrustDialogAccepted": true}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal claude config: %v", err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatalf("write claude config: %v", err)
	}
}

func writeLocalClaudeTranscript(t *testing.T, claudeDir string, workspace string, sessionID string, title string, updatedAt string) {
	t.Helper()
	projectDir := filepath.Join(claudeDir, "projects", encodeClaudeProjectPath(workspace))
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("create claude project dir: %v", err)
	}
	sessionPath := filepath.Join(projectDir, sessionID+".jsonl")
	line := fmt.Sprintf(`{"type":"summary","summary":%q,"timestamp":%q}`+"\n", title, updatedAt)
	if err := os.WriteFile(sessionPath, []byte(line), 0o600); err != nil {
		t.Fatalf("write claude transcript: %v", err)
	}
	when, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		t.Fatalf("parse updatedAt: %v", err)
	}
	if err := os.Chtimes(sessionPath, when, when); err != nil {
		t.Fatalf("chtime claude transcript: %v", err)
	}
}

func appendLocalClaudeAssistantModel(t *testing.T, claudeDir string, workspace string, sessionID string, model string) {
	t.Helper()
	path := filepath.Join(claudeDir, "projects", encodeClaudeProjectPath(workspace), sessionID+".jsonl")
	record := fmt.Sprintf(`{"type":"assistant","message":{"model":%q}}`+"\n", model)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open claude transcript: %v", err)
	}
	if _, err := file.WriteString(record); err != nil {
		_ = file.Close()
		t.Fatalf("append claude assistant model: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close claude transcript: %v", err)
	}
}
