package messaging

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *codexSessionStore) load() {
	s.mu.Lock()
	filePath := s.filePath
	s.mu.Unlock()
	if filePath == "" {
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[codex-session] failed to read %s: %v", filePath, err)
		}
		return
	}

	var state codexSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[codex-session] failed to parse %s: %v", filePath, err)
		return
	}

	bindings := make(map[string]codexSessionBinding, len(state.Bindings))
	controls := make(map[string]codexControlIntent, len(state.Controls))
	changed := state.Version != codexSessionStateVersion
	for key, binding := range state.Bindings {
		if strings.TrimSpace(key) == "" {
			continue
		}
		migratedKey, migrated := migrateLegacyBindingKey(key)
		if migrated {
			changed = true
		}
		normalized := codexSessionBinding{
			ActiveWorkspace: normalizeCodexWorkspaceRoot(binding.ActiveWorkspace),
			Workspaces:      make(map[string]codexWorkspaceSession),
		}
		for workspaceRoot, session := range binding.Workspaces {
			workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
			if workspaceRoot == "" {
				continue
			}
			normalized.Workspaces[workspaceRoot] = session
		}
		bindings[migratedKey] = mergeCodexSessionBinding(bindings[migratedKey], normalized)
	}
	for threadID, intent := range state.Controls {
		threadID = strings.TrimSpace(threadID)
		normalized, err := normalizeCodexControlIntent(codexControlIntentUpdate{
			ThreadID: threadID, Owner: intent.Owner,
			RouteBindingKey: intent.RouteBindingKey, ConversationID: intent.ConversationID,
		})
		if err != nil || intent.Revision == 0 {
			changed = true
			continue
		}
		normalized.Revision = intent.Revision
		normalized.UpdatedAt = strings.TrimSpace(intent.UpdatedAt)
		controls[threadID] = normalized
	}

	s.mu.Lock()
	s.bindings = bindings
	s.controls = controls
	s.mu.Unlock()
	if changed {
		s.save()
	}
}

func mergeCodexSessionBinding(current codexSessionBinding, incoming codexSessionBinding) codexSessionBinding {
	if current.Workspaces == nil {
		current.Workspaces = make(map[string]codexWorkspaceSession)
	}
	if current.ActiveWorkspace == "" {
		current.ActiveWorkspace = incoming.ActiveWorkspace
	}
	for workspaceRoot, session := range incoming.Workspaces {
		current.Workspaces[workspaceRoot] = mergeCodexWorkspaceSession(current.Workspaces[workspaceRoot], session)
	}
	return current
}

func mergeCodexWorkspaceSession(current codexWorkspaceSession, incoming codexWorkspaceSession) codexWorkspaceSession {
	if current.UpdatedAt == "" || incoming.UpdatedAt > current.UpdatedAt {
		return incoming
	}
	return current
}

func (s *codexSessionStore) save() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := s.persistStateLocked(); err != nil {
		log.Printf("[codex-session] failed to save state: %v", err)
	}
}

// persistStateLocked 在持有 saveMu 时原子写入当前快照。
func (s *codexSessionStore) persistStateLocked() error {
	filePath, state := s.snapshotCodexSessionState()
	return s.persistCandidate(filePath, state)
}

// persistCandidate 将候选快照交给可注入 writer，供原子提交在替换内存前验证写盘结果。
func (s *codexSessionStore) persistCandidate(filePath string, state codexSessionState) error {
	if filePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return fmt.Errorf("创建状态目录: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("编码状态: %w", err)
	}
	writer := s.writeState
	if writer == nil {
		writer = writeCodexSessionStateFile
	}
	return writer(filePath, data)
}

func (s *codexSessionStore) snapshotCodexSessionState() (string, codexSessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filePath := s.filePath
	state := codexSessionState{
		Version:  codexSessionStateVersion,
		Bindings: make(map[string]codexSessionBinding, len(s.bindings)),
		Controls: make(map[string]codexControlIntent, len(s.controls)),
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
	for key, binding := range s.bindings {
		workspaces := make(map[string]codexWorkspaceSession, len(binding.Workspaces))
		for workspaceRoot, session := range binding.Workspaces {
			workspaces[workspaceRoot] = session
		}
		state.Bindings[key] = codexSessionBinding{ActiveWorkspace: binding.ActiveWorkspace, Workspaces: workspaces}
	}
	for threadID, intent := range s.controls {
		state.Controls[threadID] = intent
	}
	return filePath, state
}

func writeCodexSessionStateFile(filePath string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(filePath), filepath.Base(filePath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时状态文件: %w", err)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("设置临时状态文件权限: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("写入临时状态文件: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("关闭临时状态文件: %w", err)
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("替换状态文件: %w", err)
	}
	return nil
}
