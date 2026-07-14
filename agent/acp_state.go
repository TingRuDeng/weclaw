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

type acpStateFileIdentity struct {
	command  string
	args     []string
	cwd      string
	protocol string
}

// defaultACPStateFile 根据运行时身份生成隔离的持久化状态路径。
func defaultACPStateFile(identity acpStateFileIdentity) string {
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
		strings.ToLower(identity.command),
		strings.Join(identity.args, "\x00"),
		identity.cwd,
		identity.protocol,
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
	loadedBindings := 0

	a.mu.Lock()
	for conversationID, sessionID := range state.Sessions {
		if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(sessionID) == "" {
			continue
		}
		a.pendingPersistedSessions[conversationID] = sessionID
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
	a.mu.Unlock()
	if a.codexOwners != nil && state.Version < acpPersistedStateVersion {
		loadedBindings = a.codexOwners.restoreBindings(state.LiveBindings)
	}

	if loadedSessions > 0 || loadedThreads > 0 || loadedBindings > 0 {
		log.Printf("[acp] loaded state (pending_sessions=%d, threads=%d, bindings=%d, file=%s)", loadedSessions, loadedThreads, loadedBindings, a.stateFile)
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
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
	if !a.isClaudeACPIdentityLocked() {
		for k, v := range a.pendingPersistedSessions {
			state.Sessions[k] = v
		}
		for k, v := range a.sessions {
			state.Sessions[k] = v
		}
	}
	for k, v := range a.threads {
		state.Threads[k] = v
	}
	return state, a.stateFile, true
}

// isClaudeACPIdentityLocked 在持有 a.mu 时按显式身份判断，避免状态路径递归加锁。
func (a *ACPAgent) isClaudeACPIdentityLocked() bool {
	if a.protocol != protocolLegacyACP {
		return false
	}
	return requiresClaudeSessionCapabilities(a.configuredName, a.capabilities.AgentInfo)
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
