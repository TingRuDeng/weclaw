package messaging

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
)

func (h *Handler) codexSessionModelStatus(threadID string) sessionModelStatus {
	h.mu.RLock()
	dir := h.codexLocalSessionDir
	h.mu.RUnlock()
	path, ok := findLocalCodexSessionPath(filepath.Join(dir, "sessions"), threadID)
	if !ok {
		return sessionModelStatus{}
	}
	return readCodexSessionModelStatus(path)
}

// findLocalCodexSessionPath uses the rollout filename to narrow the lookup to
// one thread. Opening and parsing every historical transcript made a session
// switch latency grow with the total Codex history and kept the binding lock
// held while unrelated files were read.
func findLocalCodexSessionPath(root string, threadID string) (string, bool) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || filepath.Base(threadID) != threadID ||
		strings.Contains(threadID, `\`) || threadID == "." || threadID == ".." {
		return "", false
	}

	var matched string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 ||
			!localCodexSessionFilenameMatches(entry.Name(), threadID) {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		meta, ok := readLocalCodexSessionMeta(path)
		if !ok || meta.ID != threadID {
			return nil
		}
		matched = path
		return fs.SkipAll
	})
	return matched, matched != ""
}

func localCodexSessionFilenameMatches(name string, threadID string) bool {
	if filepath.Ext(name) != ".jsonl" {
		return false
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return base == threadID || strings.HasSuffix(base, "-"+threadID)
}

// readCodexSessionModelStatus 使用最后一条 turn_context，保证展示本会话最近一次实际配置。
func readCodexSessionModelStatus(path string) sessionModelStatus {
	status := sessionModelStatus{}
	readSessionJSONLines(path, func(line []byte) {
		if current, ok := parseCodexTurnContext(line); ok {
			status = current
		}
	})
	return status
}

func parseCodexTurnContext(line []byte) (sessionModelStatus, bool) {
	var record struct {
		Type    string `json:"type"`
		Payload struct {
			Model           string `json:"model"`
			Effort          string `json:"effort"`
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &record); err != nil || record.Type != "turn_context" {
		return sessionModelStatus{}, false
	}
	return sessionModelStatus{
		Model:  strings.TrimSpace(record.Payload.Model),
		Effort: firstNonBlank(record.Payload.Effort, record.Payload.ReasoningEffort),
	}, true
}
