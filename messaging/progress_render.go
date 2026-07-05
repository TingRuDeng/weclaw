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
	delta = strings.TrimSpace(delta)
	if strings.HasPrefix(delta, "进展：") {
		return delta
	}
	if cfg.Mode == progressModeStream {
		preview := truncateTailRunes(delta, cfg.PreviewRunes)
		return "实时片段，仅供预览：\n" + preview
	}
	return "处理中，请耐心等待....."
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
