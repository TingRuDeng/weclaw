package agent

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

func defaultACPStateFile(command string, args []string, cwd string, protocol string) string {
	home, err := config.DataDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[acp] failed to create state dir: %v", err)
		return ""
	}
	key := strings.Join([]string{
		strings.ToLower(command),
		strings.Join(args, "\x00"),
		cwd,
		protocol,
	}, "\x00")
	sum := sha1.Sum([]byte(key))
	return filepath.Join(dir, "acp-"+hex.EncodeToString(sum[:])+".json")
}

func (a *ACPAgent) loadState() {
	if a.stateFile == "" {
		return
	}

	data, err := os.ReadFile(a.stateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[acp] failed to read state file %s: %v", a.stateFile, err)
		}
		return
	}

	var state acpPersistedState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[acp] failed to parse state file %s: %v", a.stateFile, err)
		return
	}

	loadedSessions := 0
	loadedThreads := 0
	loadedHistory := 0

	a.mu.Lock()
	for conversationID, sessionID := range state.Sessions {
		if conversationID == "" || sessionID == "" {
			continue
		}
		a.sessions[conversationID] = sessionID
		loadedSessions++
	}
	for conversationID, threadID := range state.Threads {
		if conversationID == "" || threadID == "" {
			continue
		}
		a.threads[conversationID] = threadID
		a.resumeOnFirstUse[conversationID] = true
		loadedThreads++
	}
	for conversationID, messages := range state.History {
		if conversationID == "" || len(messages) == 0 {
			continue
		}
		normalized := make([]acpHistoryMessage, 0, len(messages))
		for _, msg := range messages {
			role := strings.TrimSpace(strings.ToLower(msg.Role))
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}
			if role != "user" && role != "assistant" {
				continue
			}
			normalized = append(normalized, acpHistoryMessage{
				Role: role,
				Text: text,
			})
		}
		if len(normalized) == 0 {
			continue
		}
		if len(normalized) > acpMaxHistoryMessages {
			normalized = normalized[len(normalized)-acpMaxHistoryMessages:]
		}
		a.history[conversationID] = normalized
		loadedHistory++
	}
	a.mu.Unlock()

	if loadedSessions > 0 || loadedThreads > 0 || loadedHistory > 0 {
		log.Printf("[acp] restored state (sessions=%d, threads=%d, history=%d, file=%s)", loadedSessions, loadedThreads, loadedHistory, a.stateFile)
	}
}

func (a *ACPAgent) persistState() {
	a.stateSaveMu.Lock()
	defer a.stateSaveMu.Unlock()

	state, stateFile, ok := a.snapshotPersistedState()
	if !ok {
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[acp] failed to marshal state: %v", err)
		return
	}
	if err := writeACPStateAtomically(stateFile, data); err != nil {
		log.Printf("[acp] failed to persist state file %s: %v", stateFile, err)
	}
}

func (a *ACPAgent) snapshotPersistedState() (acpPersistedState, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stateFile == "" {
		return acpPersistedState{}, "", false
	}
	state := acpPersistedState{
		Version:  acpPersistedStateVersion,
		Protocol: a.protocol,
		Sessions: make(map[string]string, len(a.sessions)),
		Threads:  make(map[string]string, len(a.threads)),
		History:  make(map[string][]acpHistoryMessage, len(a.history)),
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range a.sessions {
		state.Sessions[k] = v
	}
	for k, v := range a.threads {
		state.Threads[k] = v
	}
	for conversationID, messages := range a.history {
		if len(messages) == 0 {
			continue
		}
		copied := make([]acpHistoryMessage, len(messages))
		copy(copied, messages)
		state.History[conversationID] = copied
	}
	return state, a.stateFile, true
}

func writeACPStateAtomically(stateFile string, data []byte) error {
	dir := filepath.Dir(stateFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(stateFile)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create state tmp file: %w", err)
	}
	tmpFile := tmp.Name()
	defer os.Remove(tmpFile)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod state tmp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write state tmp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state tmp file: %w", err)
	}
	if err := os.Rename(tmpFile, stateFile); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
