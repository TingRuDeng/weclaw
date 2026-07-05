package messaging

import "strings"

// friendlyAgentError 将常见 Agent 底层错误转换成微信侧可操作提示。
func friendlyAgentError(err error) string {
	raw := sanitizeAgentError(err.Error())
	lower := strings.ToLower(raw)
	switch {
	case isTurnTimeoutError(lower):
		return "本轮执行超时已被中止（可能卡在长命令或测试上）。我已强制回收子进程，你可以直接继续对话续接上一会话，或发送 /new 开启新会话。"
	case isCodexUpstreamError(lower):
		return "Codex 上游服务暂时不可用，当前请求没有完成。这通常不是微信或 WeClaw 配置错误，可以稍后重试；如果同一个旧会话反复触发 compact 失败，请发送 /new 创建新会话。"
	case isCodexWebSocketForbidden(lower):
		return "Codex 实时通道连接被服务端拒绝（403 Forbidden）。这是 Codex 网关的 WebSocket 权限或代理配置问题；Codex 通常会尝试 HTTPS 通道重试，如果仍失败，请检查当前 Codex 网关的 responses WebSocket 访问权限。"
	case isACPSessionNotFound(lower):
		return "Agent 会话已失效，可能是 ACP 子进程重启或切换账号后，本地恢复了旧 sessionId。请发送 /new 创建新会话后再试。"
	default:
		return raw
	}
}

// sanitizeAgentError 清理终端控制字符，避免 ANSI 颜色码透出到微信消息。
func sanitizeAgentError(text string) string {
	text = ansiEscapePattern.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, text)
	return strings.TrimSpace(text)
}

func isCodexUpstreamError(lower string) bool {
	hasCodexSignal := strings.Contains(lower, "turn error") ||
		strings.Contains(lower, "remote compact") ||
		strings.Contains(lower, "/responses/compact")
	hasUpstreamSignal := strings.Contains(lower, "upstream") ||
		strings.Contains(lower, "bad gateway") ||
		strings.Contains(lower, "502")
	return hasCodexSignal && hasUpstreamSignal
}

func isCodexWebSocketForbidden(lower string) bool {
	hasCodexSignal := strings.Contains(lower, "responses_websocket") ||
		strings.Contains(lower, "/v1/responses") ||
		strings.Contains(lower, "ws://")
	hasForbiddenSignal := strings.Contains(lower, "websocket") &&
		strings.Contains(lower, "403 forbidden")
	return hasCodexSignal && hasForbiddenSignal
}

func isACPSessionNotFound(lower string) bool {
	hasPromptSignal := strings.Contains(lower, "prompt error") ||
		strings.Contains(lower, "session/prompt") ||
		strings.Contains(lower, "agent error")
	return hasPromptSignal && strings.Contains(lower, "session not found")
}

// isTurnTimeoutError 识别单轮超时被取消/强杀的错误，给出可续接的友好提示。
func isTurnTimeoutError(lower string) bool {
	return strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "signal: killed") ||
		strings.Contains(lower, "signal: interrupt") ||
		strings.Contains(lower, "context canceled")
}
