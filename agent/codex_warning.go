package agent

import (
	"encoding/json"
	"log"
	"strings"
)

type codexWarningParams struct {
	ThreadID string `json:"threadId"`
	Message  string `json:"message"`
}

// handleCodexWarning 只记录 app-server 非致命警告，不把内部传输状态写入用户进度。
func (a *ACPAgent) handleCodexWarning(params json.RawMessage) {
	var warning codexWarningParams
	if err := json.Unmarshal(params, &warning); err != nil {
		log.Printf("[acp] failed to parse codex warning: %v", err)
		return
	}
	if strings.TrimSpace(warning.Message) == "" {
		return
	}
	log.Printf("[acp] codex warning (thread=%s)", warning.ThreadID)
}
