package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
)

func renderAcceptance(taskTitle string) string {
	return "收到，开始处理....."
}

func renderInitialProgress() string {
	return "进展：任务仍在执行中，连接正常。\n\n我会继续等待 Agent 完成，并发送最终完整结果。"
}

func renderDeltaProgress(delta string, cfg config.ProgressConfig) string {
	status := lastNonEmptyProgressLine(delta)
	if strings.HasPrefix(status, "进展：") {
		return status
	}
	if cfg.Mode == progressModeStream {
		status = truncateTailRunes(status, cfg.PreviewRunes)
		if status == "" {
			return ""
		}
		return "实时状态：\n" + status
	}
	return "处理中，请耐心等待....."
}

// lastNonEmptyProgressLine 从累计输出中提取最后一条可读状态，避免把正文碎片刷进卡片。
func lastNonEmptyProgressLine(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func renderFinalSuccess(prefix string, reply string) string {
	reply = strings.TrimSpace(reply)
	return prefix + reply
}

func renderFinalFailure(prefix string, err error) string {
	reason := "未知错误"
	if err != nil {
		reason = friendlyAgentError(err)
	}
	return prefix + "本次未完成。\n\n原因：" + reason + "\n\n你可以调整需求后重试，或发送 /new 开启新会话。"
}
