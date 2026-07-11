package messaging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentSessionStorePersistsIsolatedSelections(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "agent-sessions.json")
	first := newAgentSessionStore()
	if err := first.SetFilePath(stateFile); err != nil {
		t.Fatalf("设置状态文件失败：%v", err)
	}
	if err := first.Set("session-a", "claude"); err != nil {
		t.Fatalf("保存 session-a 失败：%v", err)
	}
	if err := first.Set("session-b", "codex"); err != nil {
		t.Fatalf("保存 session-b 失败：%v", err)
	}

	restored := newAgentSessionStore()
	if err := restored.SetFilePath(stateFile); err != nil {
		t.Fatalf("恢复状态文件失败：%v", err)
	}
	if got, ok := restored.Get("session-a"); !ok || got != "claude" {
		t.Fatalf("session-a=(%q,%v)，期望 (claude,true)", got, ok)
	}
	if got, ok := restored.Get("session-b"); !ok || got != "codex" {
		t.Fatalf("session-b=(%q,%v)，期望 (codex,true)", got, ok)
	}
}

func TestAgentSessionStoreRejectsCorruptedState(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "agent-sessions.json")
	if err := os.WriteFile(stateFile, []byte("{"), 0o600); err != nil {
		t.Fatalf("写入损坏状态失败：%v", err)
	}
	store := newAgentSessionStore()
	if err := store.SetFilePath(stateFile); err == nil {
		t.Fatal("损坏状态文件必须返回错误")
	}
	if err := store.Set("session-a", "claude"); err != nil {
		t.Fatalf("显式报错后应允许下一次切换修复状态文件：%v", err)
	}
	restored := newAgentSessionStore()
	if err := restored.SetFilePath(stateFile); err != nil {
		t.Fatalf("修复后的状态文件应能加载：%v", err)
	}
	if got, ok := restored.Get("session-a"); !ok || got != "claude" {
		t.Fatalf("修复后的状态=(%q,%v)，期望 (claude,true)", got, ok)
	}
}

func TestAgentSessionStoreKeepsSelectionWhenSaveFails(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "agent-sessions.json")
	store := newAgentSessionStore()
	if err := store.SetFilePath(stateFile); err != nil {
		t.Fatalf("设置状态文件失败：%v", err)
	}
	if err := store.Set("session-a", "claude"); err != nil {
		t.Fatalf("保存初始状态失败：%v", err)
	}
	store.mu.Lock()
	store.filePath = filepath.Join(stateFile, "child.json")
	store.mu.Unlock()
	if err := store.Set("session-a", "codex"); err == nil {
		t.Fatal("不可写路径必须返回错误")
	}
	if got, ok := store.Get("session-a"); !ok || got != "claude" {
		t.Fatalf("写盘失败后的状态=(%q,%v)，期望 (claude,true)", got, ok)
	}
}
