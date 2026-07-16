package messaging

import "strings"

// agentDisplayName 把内部 Agent 配置名归一为用户可识别的来源名称。
func agentDisplayName(name string) string {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "claude"):
		return "Claude"
	case strings.Contains(lower, "codex"):
		return "Codex"
	case trimmed != "":
		return trimmed
	default:
		return "Agent"
	}
}
