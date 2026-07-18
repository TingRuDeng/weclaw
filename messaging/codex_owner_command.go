package messaging

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

const codexOwnerCommandUsage = "用法: /cx owner"

// handleCodexOwnerCommand 保留旧命令作为只读兼容入口。v4 不再把 writer
// 所有权分配给窗口；所有 frontend 都通过同一个 app-server 使用各自 binding。
func (h *Handler) handleCodexOwnerCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	threadID, err := h.selectedCodexOwnerThread(runtime)
	if err != nil {
		return textNavigationResult(err.Error())
	}
	if len(runtime.fields) == 3 {
		switch strings.ToLower(runtime.fields[2]) {
		case "remote":
			// Old clients may still send this button. Treat it as an idempotent
			// readiness check, never as a global ownership mutation.
		case "desktop":
			return textNavigationResult(wechatCommandText(
				"无需释放 Codex 控制权。",
				"当前版本已使用单一共享 app-server；窗口只保存会话绑定，不再互相移交 writer。",
			))
		default:
			return textNavigationResult(codexOwnerCommandUsage)
		}
	} else if len(runtime.fields) != 2 {
		return textNavigationResult(codexOwnerCommandUsage)
	}
	return h.renderCodexOwnerStatus(runtime, threadID)
}

func (h *Handler) selectedCodexOwnerThread(runtime codexSessionCommandRuntime) (string, error) {
	threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
	if pending || strings.TrimSpace(threadID) == "" {
		return "", fmt.Errorf("当前窗口没有有效的 Codex 会话，请发送 /cx ls 选择或 /cx new 新建")
	}
	return strings.TrimSpace(threadID), nil
}

func (h *Handler) renderCodexOwnerStatus(runtime codexSessionCommandRuntime, threadID string) navigationCommandResult {
	if _, ok := runtime.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return textNavigationResult("当前 Codex Agent 不支持共享 app-server 状态。")
	}
	unlock, err := h.lockCodexSessionThread(runtime.ctx, threadID, "owner")
	if err != nil {
		return textNavigationResult("前一项会话操作仍在处理，状态查询未执行。")
	}
	defer unlock()
	resolution, err := h.resolveCodexRuntimeLocked(runtime.ctx, codexRuntimeResolveOptions{
		route: runtime.codexRoute(threadID), threadID: threadID, ag: runtime.agent,
	})
	if err != nil {
		return textNavigationResult("共享 Codex app-server 暂不可用，请稍后重试。")
	}
	return textNavigationResult(renderCodexOwnerStatusText(resolution))
}

func renderCodexOwnerStatusText(resolution codexRuntimeResolution) string {
	lines := []string{
		"Codex 共享会话",
		"会话: " + resolution.Request.Ref.ThreadID,
		"窗口绑定: 已绑定",
		"写入服务: " + renderCodexRuntimeHolder(resolution.Binding.Runtime),
	}
	if resolution.Binding.State.Active || resolution.Rollout.Active {
		lines = append(lines, "任务: 正在执行")
	} else {
		lines = append(lines, "任务: 空闲")
	}
	lines = append(lines, "说明: 多个飞书、微信或本地客户端可绑定同一会话；app-server 统一串行化 turn。")
	return wechatCommandText(lines...)
}

func (runtime codexSessionCommandRuntime) codexRoute(threadID string) codexConversationRoute {
	return codexConversationRoute{
		bindingKey: runtime.bindingKey, workspaceRoot: runtime.workspaceRoot,
		conversationID: buildCodexConversationID(runtime.routeUserID, runtime.agentName, runtime.workspaceRoot),
		threadID:       strings.TrimSpace(threadID),
	}
}
