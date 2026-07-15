package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

const codexOwnerCommandUsage = "用法: /cx owner [remote|desktop]"

type codexOwnerReleaseValidation struct {
	runtime  codexSessionCommandRuntime
	threadID string
	current  codexControlIntent
}

type codexOwnerReleaseCompensation struct {
	ctx       context.Context
	liveAgent agent.CodexLiveRuntimeAgent
	change    codexRuntimeIntentChange
	cause     error
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
		result, err := h.acquireCodexSessionWithBindingLocked(runtime.acquireRequest(threadID))
		if err != nil {
			return textNavigationResult(renderCodexSessionAcquireFailure(err))
		}
		return textNavigationResult(h.renderCodexSessionAcquireSuccess(result))
	case "desktop":
		return h.releaseCodexOwnerToDesktop(runtime, threadID)
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
		log.Printf("[codex-owner] 控制权查询失败 thread=%q: %v", threadID, err)
		return cardNavigationResult("查询 Codex 控制权失败，请重试。")
	}
	return cardNavigationResult(renderCodexOwnerStatusText(runtime, resolution))
}

// releaseCodexOwnerToDesktop 只释放当前 thread，保留窗口的会话选择。
func (h *Handler) releaseCodexOwnerToDesktop(runtime codexSessionCommandRuntime, threadID string) navigationCommandResult {
	unlock, err := h.lockCodexSessionThread(runtime.ctx, threadID, "owner")
	if err != nil {
		return textNavigationResult("前一项会话操作仍在处理，Codex 控制权移交未执行。")
	}
	defer unlock()
	current := h.ensureCodexSessions().controlIntent(threadID)
	if err := h.validateCodexOwnerRelease(codexOwnerReleaseValidation{
		runtime: runtime, threadID: threadID, current: current,
	}); err != nil {
		return textNavigationResult(err.Error())
	}
	liveAgent, ok := runtime.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return textNavigationResult("当前 Codex Agent 不支持显式控制权移交。")
	}
	change := codexRuntimeIntentChange{
		threadID: threadID, route: runtime.codexRoute(threadID), before: current,
		after: codexControlIntent{Owner: codexControlDesktop, Revision: current.Revision + 1},
	}
	resolution, err := h.handoffCodexRuntimeIntent(codexRuntimeHandoffRequest{
		ctx: runtime.ctx, liveAgent: liveAgent, change: change, resyncIntent: current,
	})
	if err != nil {
		return textNavigationResult(renderCodexOwnerReleaseFailure(err))
	}
	committed, err := h.commitCodexControlIntent(threadID, current, change.after)
	if err != nil {
		return textNavigationResult(h.compensateCodexOwnerRelease(codexOwnerReleaseCompensation{
			ctx: runtime.ctx, liveAgent: liveAgent, change: change, cause: err,
		}))
	}
	resolution.Request.Intent = agentControlIntent(committed)
	resolution.Binding.Control = resolution.Request.Intent
	return textNavigationResult(renderCodexHandoffResult(runtime, resolution.Request, resolution.Binding))
}

// validateCodexOwnerRelease 防止非持有窗口归还控制权或在任务中途释放。
func (h *Handler) validateCodexOwnerRelease(validation codexOwnerReleaseValidation) error {
	runtime, current := validation.runtime, validation.current
	targetConversationID := runtime.codexRoute("").conversationID
	if current.Owner != codexControlRemote || current.RouteBindingKey != runtime.bindingKey ||
		current.ConversationID != targetConversationID {
		return fmt.Errorf("当前窗口未控制该 Codex 会话，不能归还控制权")
	}
	if _, active := h.activeCodexTaskConversation(validation.threadID); active {
		return fmt.Errorf("请等待当前远程任务结束，或先发送 /stop，再归还控制权")
	}
	if _, active := h.activeTask(current.ConversationID); active {
		return fmt.Errorf("请等待当前远程任务结束，或先发送 /stop，再归还控制权")
	}
	return nil
}

// compensateCodexOwnerRelease 在持久化失败后用独立预算恢复原运行时意图。
func (h *Handler) compensateCodexOwnerRelease(compensation codexOwnerReleaseCompensation) string {
	cleanupCtx, cancel := newCodexSessionAcquireCleanupContext(compensation.ctx)
	defer cancel()
	changes := []codexRuntimeIntentChange{compensation.change}
	if err := h.compensateCodexRuntimeChanges(cleanupCtx, compensation.liveAgent, changes); err != nil {
		return renderCodexOwnerReleaseFailure(errors.Join(errCodexSessionAcquireUncertain, compensation.cause, err))
	}
	log.Printf("[codex-owner] 控制权提交失败 thread=%q: %v", compensation.change.threadID, compensation.cause)
	return "Codex 控制权提交失败，本次释放未生效。"
}

// renderCodexOwnerReleaseFailure 区分已校准失败与无法确认的 fail-closed 状态。
func renderCodexOwnerReleaseFailure(err error) string {
	if errors.Is(err, errCodexSessionAcquireUncertain) {
		return "Codex 控制权移交结果未确认，当前禁止继续写入。"
	}
	if isCodexSessionControlTimeout(err) {
		return "Codex 控制权移交超时，已按当前控制意图重新校准；请重试。"
	}
	log.Printf("[codex-owner] 控制权移交失败: %v", err)
	return "Codex 控制权移交失败，请重试。"
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

// commitCodexControlIntent 使用旧 revision 做 CAS，避免两个窗口同时认领成功。
func (h *Handler) commitCodexControlIntent(threadID string, current codexControlIntent, proposed codexControlIntent) (codexControlIntent, error) {
	return h.ensureCodexSessions().updateControlIntent(codexControlIntentUpdate{
		ThreadID: threadID, Owner: proposed.Owner,
		RouteBindingKey: proposed.RouteBindingKey, ConversationID: proposed.ConversationID,
		ExpectedRevision: current.Revision,
	})
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
		log.Printf("[codex-owner] 运行时冲突 thread=%q: %s", resolution.Request.Ref.ThreadID, reason)
		lines = append(lines, "冲突: 运行时写入冲突")
	}
	if resolution.ProbeErr != nil {
		lines = append(lines, "探测: "+renderCodexOwnerProbeFailure(resolution.ProbeErr))
	}
	return wechatCommandText(lines...)
}

// renderCodexOwnerProbeFailure 只展示稳定分类，底层错误仅写内部日志。
func renderCodexOwnerProbeFailure(err error) string {
	log.Printf("[codex-owner] 运行位置探测异常: %v", err)
	switch {
	case errors.Is(err, agent.ErrCodexDesktopOwnershipUnknown):
		return "Desktop 控制权未确认"
	case errors.Is(err, agent.ErrCodexRuntimeConflict):
		return "运行时写入冲突"
	default:
		return "运行位置探测异常"
	}
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
