package messaging

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func (h *Handler) claudeSessionModelStatus(sessionID string) sessionModelStatus {
	h.mu.RLock()
	dir := h.claudeLocalSessionDir
	h.mu.RUnlock()
	for _, view := range discoverLocalClaudeSessions(dir) {
		if strings.TrimSpace(view.ThreadID) != strings.TrimSpace(sessionID) {
			continue
		}
		path := filepath.Join(dir, "projects", encodeClaudeProjectPath(view.WorkspaceRoot), view.ThreadID+".jsonl")
		return readClaudeSessionModelStatus(path)
	}
	return sessionModelStatus{}
}

// readClaudeSessionModelStatus 仅采信 assistant 事件明确记录的模型和推理强度。
func readClaudeSessionModelStatus(path string) sessionModelStatus {
	status := sessionModelStatus{}
	readSessionJSONLines(path, func(line []byte) {
		if current, ok := parseClaudeAssistantModel(line); ok {
			status = current
		}
	})
	return status
}

func parseClaudeAssistantModel(line []byte) (sessionModelStatus, bool) {
	var record struct {
		Type            string `json:"type"`
		Effort          string `json:"effort"`
		EffortLevel     string `json:"effortLevel"`
		ReasoningEffort string `json:"reasoning_effort"`
		Message         struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &record); err != nil || record.Type != "assistant" {
		return sessionModelStatus{}, false
	}
	return sessionModelStatus{
		Model: strings.TrimSpace(record.Message.Model),
		Effort: firstNonBlank(
			record.Message.Effort,
			record.Effort,
			record.EffortLevel,
			record.ReasoningEffort,
		),
	}, true
}
