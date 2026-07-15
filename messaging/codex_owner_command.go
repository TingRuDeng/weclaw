package messaging

import (
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

const codexOwnerCommandUsage = "用法: /cx owner [remote|desktop]"

type codexOwnerHandoffValidation struct {
	runtime  codexSessionCommandRuntime
	threadID string
	current  codexControlIntent
	target   codexControlOwner
}

// handleCodexOwnerCommand 查询或显式移交当前 thread 的控制权。
func (h *Handler) handleCodexOwnerCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	threadID, err := h.selectedCodexOwnerThread(runtime)
	if err != nil {
		return cardNavigationResult(err.Error())
	}
	if len(runtime.fields) == 2 {
		return h.renderCodexOwnerStatus(runtime, threadID)
	}
	if len(runtime.fields) != 3 {
		return cardNavigationResult(codexOwnerCommandUsage)
	}
	switch strings.ToLower(runtime.fields[2]) {
	case "remote":
		return h.handoffCodexOwner(runtime, threadID, codexControlRemote)
	case "desktop":
		return h.handoffCodexOwner(runtime, threadID, codexControlDesktop)
	default:
		return cardNavigationResult(codexOwnerCommandUsage)
	}
}

// selectedCodexOwnerThread 要求当前工作空间已经明确选择一个会话。
func (h *Handler) selectedCodexOwnerThread(runtime codexSessionCommandRuntime) (string, error) {
	threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
	if pending || strings.TrimSpace(threadID) == "" {
		return "", fmt.Errorf("当前窗口没有有效的 Codex 会话，请发送 /cx ls 选择或 /cx new 新建")
	}
	return strings.TrimSpace(threadID), nil
}

// renderCodexOwnerStatus 重新探测实际 runtime，避免展示历史缓存状态。
func (h *Handler) renderCodexOwnerStatus(runtime codexSessionCommandRuntime, threadID string) navigationCommandResult {
	if _, ok := runtime.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return cardNavigationResult("当前 Codex Agent 不支持显式控制权状态。")
	}
	unlock, err := h.lockCodexSessionThread(runtime.ctx, threadID, "owner")
	if err != nil {
		return cardNavigationResult("前一项会话操作仍在处理，控制权查询未执行。")
	}
	defer unlock()
	started := time.Now()
	resolution, err := h.resolveCodexRuntimeLocked(runtime.ctx, codexRuntimeResolveOptions{
		route: runtime.codexRoute(threadID), threadID: threadID, ag: runtime.agent,
	})
	logCodexSessionControlTimeout("owner", "runtime-inspect", threadID, started, err)
	if err != nil {
		if isCodexSessionControlTimeout(err) {
			return cardNavigationResult("Codex 控制权查询超时，当前运行位置未确认；请重试。")
		}
		if isCodexThreadStoreReadError(err) {
			return cardNavigationResult("查询 Codex 控制权失败: 该会话暂时无法读取。")
		}
		return cardNavigationResult(fmt.Sprintf("查询 Codex 控制权失败: %v", err))
	}
	return cardNavigationResult(renderCodexOwnerStatusText(runtime, resolution))
}

// handoffCodexOwner 先完成实际移交，再用 revision 提交持久化控制意图。
func (h *Handler) handoffCodexOwner(runtime codexSessionCommandRuntime, threadID string, owner codexControlOwner) navigationCommandResult {
	unlock, err := h.lockCodexSessionThread(runtime.ctx, threadID, "owner")
	if err != nil {
		return cardNavigationResult("前一项会话操作仍在处理，Codex 控制权移交未执行。")
	}
	defer unlock()
	current := h.ensureCodexSessions().controlIntent(threadID)
	if err := h.validateCodexOwnerHandoff(codexOwnerHandoffValidation{
		runtime: runtime, threadID: threadID, current: current, target: owner,
	}); err != nil {
		return cardNavigationResult(err.Error())
	}
	proposed := proposedCodexControlIntent(runtime, current, owner)
	request, _, err := h.buildCodexRuntimeRequest(runtime.codexRoute(threadID), threadID)
	if err != nil {
		return cardNavigationResult(fmt.Sprintf("Codex 控制权移交失败: %v", err))
	}
	request.Intent = agentControlIntent(proposed)
	liveAgent, ok := runtime.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return cardNavigationResult("当前 Codex Agent 不支持显式控制权移交。")
	}
	started := time.Now()
	binding, err := liveAgent.HandoffCodexRuntime(runtime.ctx, request)
	logCodexSessionControlTimeout("owner", "runtime-handoff", threadID, started, err)
	if err != nil {
		if isCodexSessionControlTimeout(err) {
			return cardNavigationResult("Codex 控制权移交结果未确认，控制意图未提交；请重新查询 /cx owner 后重试。")
		}
		return cardNavigationResult(fmt.Sprintf("Codex 控制权移交失败: %v", err))
	}
	committed, err := h.commitCodexControlIntent(threadID, current, proposed)
	if err != nil {
		resyncErr := h.resyncCodexControlIntent(runtime, threadID, liveAgent)
		message := fmt.Sprintf("Codex 控制权提交失败: %v", err)
		if resyncErr != nil {
			message += fmt.Sprintf("；运行时回滚失败: %v", resyncErr)
		}
		return cardNavigationResult(message)
	}
	request.Intent = agentControlIntent(committed)
	binding.Control = request.Intent
	return cardNavigationResult(renderCodexHandoffResult(runtime, request, binding))
}

// validateCodexOwnerHandoff 防止非持有窗口归还控制权或在任务中途移交。
func (h *Handler) validateCodexOwnerHandoff(validation codexOwnerHandoffValidation) error {
	runtime, current, target := validation.runtime, validation.current, validation.target
	targetConversationID := runtime.codexRoute("").conversationID
	if activeConversationID, active := h.activeCodexTaskConversation(validation.threadID); active && activeConversationID != targetConversationID {
		return fmt.Errorf("当前 Codex 会话正在另一个消息窗口执行或观察，不能接管")
	}
	if current.Owner == codexControlRemote && target == codexControlDesktop {
		if current.RouteBindingKey != runtime.bindingKey || current.ConversationID != targetConversationID {
			return fmt.Errorf("当前 Codex 会话由另一个消息窗口远程控制，只能在原窗口归还控制权")
		}
	}
	if current.Owner != codexControlRemote || current.ConversationID == "" {
		return nil
	}
	if _, active := h.activeTask(current.ConversationID); active {
		return fmt.Errorf("请等待当前远程任务结束，或先发送 /stop，再归还控制权")
	}
	return nil
}

// activeCodexTaskConversation 返回同一 thread 当前登记的任务窗口。
func (h *Handler) activeCodexTaskConversation(threadID string) (string, bool) {
	threadID = strings.TrimSpace(threadID)
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	for conversationID, task := range h.activeTasks {
		task.mu.Lock()
		matched := task.codexThreadID == threadID && task.phase != codexTaskTerminal
		task.mu.Unlock()
		if matched {
			return conversationID, true
		}
	}
	return "", false
}

// proposedCodexControlIntent 构造交给 Agent 校验的下一版意图，但尚不写入磁盘。
func proposedCodexControlIntent(runtime codexSessionCommandRuntime, current codexControlIntent, owner codexControlOwner) codexControlIntent {
	intent := codexControlIntent{Owner: owner, Revision: current.Revision + 1}
	if owner == codexControlRemote {
		intent.RouteBindingKey = runtime.bindingKey
		intent.ConversationID = runtime.codexRoute("").conversationID
	}
	return intent
}

// commitCodexControlIntent 使用旧 revision 做 CAS，避免两个窗口同时认领成功。
func (h *Handler) commitCodexControlIntent(threadID string, current codexControlIntent, proposed codexControlIntent) (codexControlIntent, error) {
	return h.ensureCodexSessions().updateControlIntent(codexControlIntentUpdate{
		ThreadID: threadID, Owner: proposed.Owner,
		RouteBindingKey: proposed.RouteBindingKey, ConversationID: proposed.ConversationID,
		ExpectedRevision: current.Revision,
	})
}

// resyncCodexControlIntent 在 CAS 失败时把 Agent 恢复到磁盘中的获胜意图。
func (h *Handler) resyncCodexControlIntent(runtime codexSessionCommandRuntime, threadID string, liveAgent agent.CodexLiveRuntimeAgent) error {
	request, _, err := h.buildCodexRuntimeRequest(runtime.codexRoute(threadID), threadID)
	if err != nil {
		return err
	}
	_, err = liveAgent.InspectCodexRuntime(runtime.ctx, request)
	return err
}

// renderCodexOwnerStatusText 统一状态命令和卡片中的所有权信息。
func renderCodexOwnerStatusText(runtime codexSessionCommandRuntime, resolution codexRuntimeResolution) string {
	lines := []string{
		"Codex 会话控制", "会话: " + resolution.Request.Ref.ThreadID,
		"控制方: " + renderCodexControlOwnerForRoute(resolution.Request.Intent, runtime.codexRoute("")),
		"运行位置: " + renderCodexRuntimeHolder(resolution.Binding.Runtime),
	}
	if resolution.Binding.State.Active || resolution.Rollout.Active {
		lines = append(lines, "任务: 正在执行")
	} else {
		lines = append(lines, "任务: 空闲")
	}
	if reason := strings.TrimSpace(resolution.Binding.ConflictReason); reason != "" {
		lines = append(lines, "冲突: "+reason)
	}
	if resolution.ProbeErr != nil {
		lines = append(lines, "探测: "+resolution.ProbeErr.Error())
	}
	return wechatCommandText(lines...)
}

// renderCodexControlOwnerForRoute 区分当前远程窗口和其他远程窗口。
func renderCodexControlOwnerForRoute(intent agent.CodexControlIntent, route codexConversationRoute) string {
	if intent.Owner == agent.CodexControlRemote &&
		intent.RouteKey == route.bindingKey && intent.ConversationID == route.conversationID {
		return "当前远程窗口"
	}
	if intent.Owner == agent.CodexControlRemote {
		return "其他远程窗口"
	}
	return renderCodexControlOwner(intent.Owner)
}

// renderCodexHandoffResult 返回后端确认后的最终状态，不提前宣告成功。
func renderCodexHandoffResult(runtime codexSessionCommandRuntime, request agent.CodexRuntimeRequest, binding agent.CodexThreadBinding) string {
	action := "已归还给 Codex Desktop"
	if request.Intent.Owner == agent.CodexControlRemote {
		action = "已移交给当前远程窗口"
	}
	resolution := codexRuntimeResolution{Request: request, Binding: binding, Live: true}
	return wechatCommandText(action+"。", renderCodexOwnerStatusText(runtime, resolution))
}

// codexRoute 构造当前命令窗口绑定的会话路由。
func (runtime codexSessionCommandRuntime) codexRoute(threadID string) codexConversationRoute {
	return codexConversationRoute{
		bindingKey: runtime.bindingKey, workspaceRoot: runtime.workspaceRoot,
		conversationID: buildCodexConversationID(runtime.routeUserID, runtime.agentName, runtime.workspaceRoot),
		threadID:       strings.TrimSpace(threadID),
	}
}
