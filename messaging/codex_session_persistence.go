package messaging

import (
	"encoding/json"
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
	changed := false
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

	s.mu.Lock()
	s.bindings = bindings
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

	s.mu.Lock()
	filePath := s.filePath
	if filePath == "" {
		s.mu.Unlock()
		return
	}
	state := codexSessionState{
		Version:  1,
		Bindings: make(map[string]codexSessionBinding, len(s.bindings)),
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
	for key, binding := range s.bindings {
		workspaces := make(map[string]codexWorkspaceSession, len(binding.Workspaces))
		for workspaceRoot, session := range binding.Workspaces {
			workspaces[workspaceRoot] = session
		}
		state.Bindings[key] = codexSessionBinding{ActiveWorkspace: binding.ActiveWorkspace, Workspaces: workspaces}
	}
	s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		log.Printf("[codex-session] failed to create state dir: %v", err)
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[codex-session] failed to marshal state: %v", err)
		return
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(filePath), filepath.Base(filePath)+".*.tmp")
	if err != nil {
		log.Printf("[codex-session] failed to create temp state file: %v", err)
		return
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		log.Printf("[codex-session] failed to chmod %s: %v", tmpName, err)
		return
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		log.Printf("[codex-session] failed to write %s: %v", tmpName, err)
		return
	}
	if err := tmpFile.Close(); err != nil {
		log.Printf("[codex-session] failed to close %s: %v", tmpName, err)
		return
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		log.Printf("[codex-session] failed to move %s into place: %v", filePath, err)
	}
}
