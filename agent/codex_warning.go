package agent

import (
	"encoding/json"
	"log"
	"strings"
)

const codexHTTPSFallbackProgress = "进展：Codex 正在切换到 HTTPS 传输。"

type codexWarningParams struct {
	ThreadID string `json:"threadId"`
	Message  string `json:"message"`
}

// handleCodexWarning 展示 app-server 非致命警告，但不改变 turn 终态。
func (a *ACPAgent) handleCodexWarningAt(params json.RawMessage, sequence uint64) {
	var warning codexWarningParams
	if err := json.Unmarshal(params, &warning); err != nil {
		log.Printf("[acp] failed to parse codex warning: %v", err)
		return
	}
	message := strings.TrimSpace(warning.Message)
	if message == "" {
		return
	}
	log.Printf("[acp] codex warning (thread=%s)", warning.ThreadID)
	status := "进展：Codex 正在处理连接异常。"
	if isCodexHTTPSFallbackWarning(message) {
		status = codexHTTPSFallbackProgress
	}
	a.dispatchProgressEventToThread(warning.ThreadID, status, &codexProgressEvent{
		ID: "warning", Kind: "status", Action: strings.TrimPrefix(status, codexProgressPrefix), Status: "running",
	}, sequence)
}

func isCodexHTTPSFallbackWarning(message string) bool {
	return strings.Contains(strings.ToLower(message), "falling back from websockets to https transport")
}
