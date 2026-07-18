package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// renderCodexStatus 合并窗口 binding 与共享 app-server 运行态；没有有效会话时仍返回基础状态。
func (h *Handler) renderCodexStatus(runtime codexSessionCommandRuntime) navigationCommandResult {
	base := h.renderCodexStatusForRoute(runtime.actorUserID, runtime.routeUserID, runtime.agentName, runtime.agent)
	threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
	threadID = strings.TrimSpace(threadID)
	if pending || threadID == "" {
		return textNavigationResult(base)
	}
	if _, ok := runtime.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return textNavigationResult(base)
	}

	unlock, err := h.lockCodexSessionThread(runtime.ctx, threadID, "status")
	if err != nil {
		return textNavigationResult(wechatCommandText(base, "共享服务状态: 查询繁忙，请稍后重试。"))
	}
	defer unlock()
	resolution, err := h.resolveCodexRuntimeLocked(runtime.ctx, codexRuntimeResolveOptions{
		route: runtime.codexRoute(threadID), threadID: threadID, ag: runtime.agent,
	})
	if err != nil {
		return textNavigationResult(wechatCommandText(base, "共享服务状态: 暂不可用，请稍后重试。"))
	}
	return textNavigationResult(wechatCommandText(base, renderCodexRuntimeStatusText(resolution)))
}

func renderCodexRuntimeStatusText(resolution codexRuntimeResolution) string {
	lines := []string{
		"窗口绑定: 已绑定",
		"写入服务: " + renderCodexRuntimeHolder(resolution.Binding.Runtime),
	}
	if resolution.Binding.State.Active || resolution.Rollout.Active {
		lines = append(lines, "任务: 正在执行")
	} else {
		lines = append(lines, "任务: 空闲")
	}
	lines = append(lines, "说明: 多个前端可绑定同一会话；app-server 统一串行化 turn。")
	return wechatCommandText(lines...)
}

func (runtime codexSessionCommandRuntime) codexRoute(threadID string) codexConversationRoute {
	return codexConversationRoute{
		bindingKey: runtime.bindingKey, workspaceRoot: runtime.workspaceRoot,
		conversationID: buildCodexConversationID(runtime.routeUserID, runtime.agentName, runtime.workspaceRoot),
		threadID:       strings.TrimSpace(threadID),
	}
}
