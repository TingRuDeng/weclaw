package messaging

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func (h *Handler) codexSessionModelStatus(threadID string) sessionModelStatus {
	h.mu.RLock()
	dir := h.codexLocalSessionDir
	h.mu.RUnlock()
	metas := readLocalCodexSessionMetas(filepath.Join(dir, "sessions"))
	meta, ok := metas[strings.TrimSpace(threadID)]
	if !ok {
		return sessionModelStatus{}
	}
	return readCodexSessionModelStatus(meta.Path)
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
