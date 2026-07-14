package messaging

import (
	"path/filepath"
	"strings"
)

func codexBindingKey(userID string, agentName string) string {
	return normalizeConversationUserKey(userID) + "\x00" + strings.TrimSpace(agentName)
}

// codexBindingExecutionKey 统一 Codex 绑定写入和任务 route 快照使用的串行键。
func codexBindingExecutionKey(bindingKey string) string {
	return "codex-binding:" + bindingKey
}

func migrateLegacyBindingKey(bindingKey string) (string, bool) {
	parts := strings.SplitN(bindingKey, "\x00", 2)
	migratedUserKey := normalizeConversationUserKey(parts[0])
	if migratedUserKey == strings.TrimSpace(parts[0]) {
		return bindingKey, false
	}
	if len(parts) == 1 {
		return migratedUserKey, migratedUserKey != bindingKey
	}
	return migratedUserKey + "\x00" + parts[1], true
}

func normalizeConversationUserKey(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" || strings.Contains(userID, ":") {
		return userID
	}
	return legacyBindingDefaultPlatform + ":" + userID
}

func buildCodexConversationID(userID string, agentName string, workspaceRoot string) string {
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	return strings.Join([]string{"codex", normalizeConversationUserKey(userID), strings.TrimSpace(agentName), workspaceRoot}, "\x00")
}

func normalizeCodexWorkspaceRoot(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return workspaceRoot
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		return abs
	}
	return workspaceRoot
}
