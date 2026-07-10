package agent

import (
	"encoding/json"
	"log"
	"strings"
)

const (
	codexWarningMaxRunes       = 160
	codexHTTPSFallbackProgress = "进展：Codex 正在切换到 HTTPS 传输。"
)

type codexWarningParams struct {
	ThreadID string `json:"threadId"`
	Message  string `json:"message"`
}

// handleCodexWarning 展示 app-server 非致命警告，但不改变 turn 终态。
func (a *ACPAgent) handleCodexWarning(params json.RawMessage) {
	var warning codexWarningParams
	if err := json.Unmarshal(params, &warning); err != nil {
		log.Printf("[acp] failed to parse codex warning: %v", err)
		return
	}
	message := strings.TrimSpace(warning.Message)
	if message == "" {
		return
	}
	log.Printf("[acp] codex warning (thread=%s): %.200s", warning.ThreadID, message)
	if isCodexHTTPSFallbackWarning(message) {
		a.dispatchProgressToThread(warning.ThreadID, codexHTTPSFallbackProgress)
		return
	}
	a.dispatchProgressToThread(warning.ThreadID, "进展：Codex 警告："+trimRunes(message, codexWarningMaxRunes))
}

func isCodexHTTPSFallbackWarning(message string) bool {
	return strings.Contains(strings.ToLower(message), "falling back from websockets to https transport")
}
