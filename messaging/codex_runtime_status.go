package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
)

// renderCodexStatus 合并窗口 binding 与共享 app-server 运行态；没有有效会话时仍返回基础状态。
func (h *Handler) renderCodexStatus(runtime codexSessionCommandRuntime) navigationCommandResult {
	base := h.renderCodexStatusForRoute(runtime.actorUserID, runtime.routeUserID, runtime.agentName, runtime.agent)
	accountLine := renderCodexStatusAccountLine(runtime)
	threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
	threadID = strings.TrimSpace(threadID)
	if pending || threadID == "" {
		runtimeLine := "运行: 未绑定会话"
		if pending {
			runtimeLine = "运行: 等待首条消息"
		}
		return compactCodexStatusResult(base, "任务: 空闲", accountLine, runtimeLine)
	}
	if _, ok := runtime.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return compactCodexStatusResult(base, "任务: 未确认", accountLine, "运行: 兼容模式")
	}

	unlock, err := h.lockCodexSessionThread(runtime.ctx, threadID, "status")
	if err != nil {
		return compactCodexStatusResult(base, "任务: 未确认", accountLine, "运行: 查询繁忙，请稍后重试")
	}
	defer unlock()
	resolution, err := h.resolveCodexRuntimeLocked(runtime.ctx, codexRuntimeResolveOptions{
		route: runtime.codexRoute(threadID), threadID: threadID, ag: runtime.agent,
	})
	if err != nil {
		return compactCodexStatusResult(base, "任务: 未确认", accountLine, "运行: 暂不可用，请稍后重试")
	}
	taskLine, runtimeLine := compactCodexRuntimeStatusLines(resolution)
	return compactCodexStatusResult(base, taskLine, accountLine, runtimeLine)
}

func renderCodexStatusAccountLine(runtime codexSessionCommandRuntime) string {
	accountAgent, ok := runtime.agent.(agent.CodexAccountAgent)
	if !ok {
		return ""
	}
	status, err := accountAgent.CurrentCodexAccount(runtime.ctx, false)
	if err != nil {
		code := codexauth.ErrorCode(err)
		if code == "" {
			code = codexauth.CodeRuntimeUnavailable
		}
		return "账号: 暂不可用（" + code + "）"
	}
	if status.Store.Current == nil {
		return "账号: 未保存"
	}
	label := strings.TrimSpace(status.Store.Current.Label)
	if label == "" {
		label = "已保存"
	}
	return "账号: " + label
}

func compactCodexRuntimeStatusLines(resolution codexRuntimeResolution) (string, string) {
	taskLine := "任务: 空闲"
	if resolution.Binding.State.Active || resolution.Rollout.Active {
		taskLine = "任务: 正在执行"
	}
	runtimeLine := "运行: 未确认"
	switch resolution.Binding.Runtime {
	case agent.CodexRuntimeWeClaw:
		runtimeLine = "运行: 正常"
	case agent.CodexRuntimeConflict:
		runtimeLine = "运行: 异常（写入冲突）"
	case agent.CodexRuntimeDesktop:
		runtimeLine = "运行: 异常（旧版 Codex Desktop bridge）"
	}
	return taskLine, runtimeLine
}

func compactCodexStatusResult(base string, taskLine string, accountLine string, runtimeLine string) navigationCommandResult {
	return textNavigationResult(wechatCommandText(base, taskLine, accountLine, runtimeLine))
}

func (runtime codexSessionCommandRuntime) codexRoute(threadID string) codexConversationRoute {
	return codexConversationRoute{
		bindingKey: runtime.bindingKey, workspaceRoot: runtime.workspaceRoot,
		conversationID: buildCodexConversationID(runtime.routeUserID, runtime.agentName, runtime.workspaceRoot),
		threadID:       strings.TrimSpace(threadID),
	}
}
