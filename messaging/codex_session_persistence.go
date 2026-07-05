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
	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		log.Printf("[codex-session] failed to write %s: %v", tmpFile, err)
		return
	}
	if err := os.Rename(tmpFile, filePath); err != nil {
		log.Printf("[codex-session] failed to move %s into place: %v", filePath, err)
	}
}
