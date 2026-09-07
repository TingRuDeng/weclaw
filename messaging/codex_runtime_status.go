package messaging

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/platform"
)

// renderCodexStatus 合并窗口 binding 与共享 app-server 运行态；没有有效会话时仍返回基础状态。
func (h *Handler) renderCodexStatus(runtime codexSessionCommandRuntime) navigationCommandResult {
	base := h.renderCodexStatusForRoute(runtime.actorUserID, runtime.routeUserID, runtime.agentName, runtime.agent)
	accountLine := renderCodexStatusAccountLine(runtime)
	threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
	threadID = strings.TrimSpace(threadID)
	speedLine := codexFastStatusLine(runtime.ctx, runtime.agent, threadID)
	if pending || threadID == "" {
		bindingLine := "绑定: 未绑定"
		runtimeLine := "运行通道: 不可用（未绑定会话）"
		routeLines := codexStatusRouteDetails(h, runtime, threadID, "(none)", agent.CodexRuntimeUnknown, false)
		if pending {
			bindingLine = "绑定: 已绑定"
			runtimeLine = "运行通道: 可用（等待首条消息）"
		}
		return compactCodexStatusResult(base, bindingLine, "任务: 空闲", accountLine, runtimeLine,
			append([]string{"进度同步: 正常"}, routeLines...)...)
	}
	bindingLine := "绑定: 已绑定"
	syncLine := h.codexProgressSyncStatusLine(runtime.bindingKey, threadID)
	if _, ok := runtime.agent.(agent.CodexLiveRuntimeAgent); !ok {
		extra := append([]string{syncLine, speedLine}, codexStatusCompatibilityDetails(runtime, threadID)...)
		extra = append(extra, codexStatusRouteDetails(h, runtime, threadID, "(none)", agent.CodexRuntimeUnknown, false)...)
		return compactCodexStatusResult(base, bindingLine, "任务: 未确认", accountLine, "运行通道: 可用（兼容模式）", extra...)
	}

	unlock, err := h.lockCodexSessionThread(runtime.ctx, threadID, "status")
	if err != nil {
		extra := append([]string{syncLine, speedLine}, codexStatusUnavailableDetails(runtime, threadID)...)
		extra = append(extra, codexStatusRouteDetails(h, runtime, threadID, "(none)", agent.CodexRuntimeUnknown, false)...)
		return compactCodexStatusResult(base, bindingLine, "任务: 未确认", accountLine, "运行通道: 不可用（查询繁忙）", extra...)
	}
	defer unlock()
	resolution, err := h.resolveCodexRuntimeLocked(runtime.ctx, codexRuntimeResolveOptions{
		route: runtime.codexRoute(threadID), threadID: threadID, ag: runtime.agent,
	})
	if err != nil {
		extra := append([]string{syncLine, speedLine}, codexStatusUnavailableDetails(runtime, threadID)...)
		extra = append(extra, codexStatusRouteDetails(h, runtime, threadID, "(none)", agent.CodexRuntimeUnknown, false)...)
		return compactCodexStatusResult(base, bindingLine, "任务: 未确认", accountLine, "运行通道: 不可用", extra...)
	}
	taskLine, runtimeLine := compactCodexRuntimeStatusLines(resolution)
	extra := append([]string{syncLine, speedLine}, codexRuntimeStatusDetails(h, runtime, threadID, resolution)...)
	return compactCodexStatusResult(base, bindingLine, taskLine, accountLine, runtimeLine, extra...)
}

// codexRuntimeStatusDetails 把窗口选择与真正的 Host 写入路径并列展示。
// binding 只表示当前窗口选择，不能从它推断 writer；turn 与 Host 必须来自本次权威探测。
func codexRuntimeStatusDetails(h *Handler, runtime codexSessionCommandRuntime, threadID string, resolution codexRuntimeResolution) []string {
	threadID = firstNonBlank(strings.TrimSpace(threadID), strings.TrimSpace(resolution.Binding.Ref.ThreadID))
	state := resolution.Binding.State
	turnID := strings.TrimSpace(state.ActiveTurnID)
	if turnID == "" && state.Active {
		turnID = strings.TrimSpace(resolution.Rollout.TurnID)
	}
	if turnID == "" {
		turnID = "(none)"
	}
	taskInfo := codexStatusTaskIdentity(h, runtime, threadID)
	taskID, origin := taskInfo.taskID, taskInfo.origin
	host, control := codexStatusHostAndControl(resolution.Binding.Runtime, origin)
	wait := codexStatusWaitState(state)
	details := []string{
		"任务 ID: " + taskID,
		"thread: " + firstNonBlank(threadID, "(none)"),
		"turn: " + turnID,
		"Host: " + host,
		"原始入口: " + origin,
		"当前窗口 binding: 已选择该 thread",
		codexStatusTurnWriterLine(resolution.Binding.Runtime, taskInfo, state.Active),
		"控制通道: " + control,
		codexStatusControlIntentLine(resolution.Request.Intent),
		codexStatusControlEntryLine(h, runtime, threadID, resolution.Binding.Runtime, state.Active),
		"等待: " + wait,
	}
	details = append(details, codexStatusControllableRouteLines(
		h, runtime, threadID, turnID, resolution.Binding.Runtime, state.Active, taskInfo,
	)...)
	return append(details, codexStatusRouteDetails(
		h, runtime, threadID, turnID, resolution.Binding.Runtime, state.Active,
	)...)
}

// codexStatusTurnWriterLine 明确指出当前 active turn 的 writer 类别。
// 消息 route 只能证明入口或观察位置；只有本地任务生命周期才能证明 WeClaw 是 writer。
type codexStatusTaskInfo struct {
	taskID              string
	origin              string
	inProcess           bool
	writerPlatform      platform.PlatformName
	writerAccountID     string
	writerDeliveryRoute platform.DeliveryRoute
}

func codexStatusTurnWriterLine(runtime agent.CodexRuntimeHolder, taskInfo codexStatusTaskInfo, active bool) string {
	if !active {
		return "Turn writer: 无（当前没有 active turn）"
	}
	switch runtime {
	case agent.CodexRuntimeDesktop:
		return "Turn writer: Codex App Desktop Host（具体 client 未由 Desktop IPC 暴露）"
	case agent.CodexRuntimeWeClaw:
		if taskInfo.inProcess {
			return "Turn writer: WeClaw 当前进程；入口 " + codexStatusTaskWriterIdentity(taskInfo)
		}
		switch strings.TrimSpace(taskInfo.origin) {
		case "WeClaw 消息/CLI":
			return "Turn writer: WeClaw 当前进程；入口 " + codexStatusTaskWriterIdentity(taskInfo)
		case "Codex App 或其他前端":
			return "Turn writer: 外部前端（共享 daemon；具体 client 未暴露）"
		default:
			return "Turn writer: 未登记的外部前端（共享 daemon；具体 client 未暴露）"
		}
	case agent.CodexRuntimeConflict:
		return "Turn writer: 不可确认（Host 冲突，已阻止写入）"
	default:
		return "Turn writer: 未确认（只确认 turn active，writer 身份不可读）"
	}
}

func codexStatusTaskWriterIdentity(taskInfo codexStatusTaskInfo) string {
	route := taskInfo.writerDeliveryRoute
	if route.Platform == "" {
		route.Platform = taskInfo.writerPlatform
	}
	if strings.TrimSpace(route.AccountID) == "" {
		route.AccountID = taskInfo.writerAccountID
	}
	if route.Valid() {
		return codexStatusDeliveryRouteIdentity(route)
	}
	platformName := codexStatusRouteField(route.Platform)
	accountID := codexStatusRouteField(route.AccountID)
	if platformName == "(none)" && accountID == "(none)" {
		return "未记录（platform/account/chat/reply_to 不可追踪）"
	}
	return fmt.Sprintf("platform=%s account=%s；chat/reply_to 不可追踪", platformName, accountID)
}

// codexStatusControlIntentLine 展示当前窗口提交给 runtime registry 的控制意图。
// RouteKey 只代表 binding 选择，不能被误读为 app-server 当前 writer client。
func codexStatusControlIntentLine(intent agent.CodexControlIntent) string {
	owner := strings.TrimSpace(string(intent.Owner))
	if owner == "" {
		owner = "(none)"
	}
	routeKey := strings.ReplaceAll(strings.TrimSpace(intent.RouteKey), "\x00", "/")
	if routeKey == "" {
		routeKey = "(none)"
	}
	return fmt.Sprintf("控制意图（仅表示窗口 binding）: owner=%s route=%s revision=%d；不等于 active turn writer",
		codexStatusRouteField(owner), codexStatusRouteField(routeKey),
		intent.Revision)
}

// codexStatusControlEntryLine 把当前命令真正可以发送控制请求的入口单独列出。
// 这解决了 writer client 不可读时的可操作性问题，同时保留“谁启动了 turn”与“谁能 steer”两种身份的边界。
func codexStatusControlEntryLine(
	h *Handler,
	runtime codexSessionCommandRuntime,
	threadID string,
	holder agent.CodexRuntimeHolder,
	active bool,
) string {
	const prefix = "控制入口（本次命令）: "
	route, ok := codexStatusCommandRoute(runtime)
	identity := "未知（回复器未暴露 DeliveryRoute）"
	if ok {
		identity = codexStatusDeliveryRouteIdentity(route)
	} else if strings.TrimSpace(string(runtime.req.Platform)) != "" {
		identity = "platform=" + codexStatusRouteField(runtime.req.Platform) + "；chat/reply_to 不可追踪"
	}
	if !active {
		return prefix + identity + "；当前没有 active turn"
	}
	external, controllable, denied := codexStatusCommandControlState(h, runtime, threadID)
	if denied {
		return prefix + identity + "；未授权，只能观察当前 turn"
	}
	switch holder {
	case agent.CodexRuntimeWeClaw:
		if !external || !controllable {
			return prefix + identity + "；未确认控制授权，只能观察当前 turn"
		}
		return prefix + identity + "；已授权，可发送普通输入、/guide、/stop"
	case agent.CodexRuntimeDesktop:
		if !external || !controllable {
			return prefix + identity + "；未确认控制授权，只能观察当前 turn"
		}
		return prefix + identity + "；已授权，可发送普通输入、/guide；/stop 需要在 Codex App 中执行"
	case agent.CodexRuntimeConflict:
		return prefix + identity + "；Host 冲突，控制请求已阻止"
	default:
		return prefix + identity + "；Host 未确认，暂不可控制"
	}
}

func codexStatusCommandControlState(
	h *Handler,
	runtime codexSessionCommandRuntime,
	threadID string,
) (external bool, controllable bool, denied bool) {
	if h == nil || strings.TrimSpace(threadID) == "" {
		return false, false, false
	}
	return h.externalCodexControlState(runtime.codexRoute(threadID).conversationID, runtime.actorUserID)
}

// codexStatusRouteDetails 把消息路由与 Codex writer 分开显示。
// DeliveryRoute 只能定位消息入口/投递端点，不能证明它持有 app-server writer。
func codexStatusRouteDetails(
	h *Handler,
	runtime codexSessionCommandRuntime,
	threadID string,
	turnID string,
	holder agent.CodexRuntimeHolder,
	active bool,
) []string {
	lines := []string{codexStatusCommandRouteLine(runtime)}
	lines = append(lines, codexStatusFollowerRouteLines(h, runtime, threadID, turnID, holder, active)...)
	return lines
}

// codexStatusControllableRouteLines 把已经登记且仍可控制当前 turn 的消息入口列出来。
// follower route 不是 writer，但可以代表其授权操作者向已确认 Host 提交相应控制请求。
func codexStatusControllableRouteLines(
	h *Handler,
	runtime codexSessionCommandRuntime,
	threadID string,
	turnID string,
	holder agent.CodexRuntimeHolder,
	active bool,
	taskInfo codexStatusTaskInfo,
) []string {
	if !active || strings.TrimSpace(turnID) == "" || turnID == "(none)" {
		return []string{"可控制通道: 当前没有 active turn"}
	}
	if holder != agent.CodexRuntimeWeClaw && holder != agent.CodexRuntimeDesktop {
		return []string{"可控制通道: 当前 Host 不可用或未确认，暂不能控制"}
	}
	if h == nil {
		return []string{"可控制通道: 当前没有已确认可控制的消息通道"}
	}
	workspaceRoot := normalizeCodexWorkspaceRoot(runtime.workspaceRoot)
	threadID = strings.TrimSpace(threadID)
	snapshots := h.ensureCodexSessions().followerSnapshots()
	type controllableRoute struct {
		route  platform.DeliveryRoute
		source string
	}
	routes := make([]controllableRoute, 0, len(snapshots)+2)
	routeIndexes := make(map[string]int, len(snapshots)+2)
	addRoute := func(route platform.DeliveryRoute, source string) {
		route.AccountID = strings.TrimSpace(route.AccountID)
		route.ChatID = strings.TrimSpace(route.ChatID)
		route.ReplyToID = strings.TrimSpace(route.ReplyToID)
		if !route.Valid() {
			return
		}
		key := strings.Join([]string{
			strings.TrimSpace(string(route.Platform)), route.AccountID, route.ChatID,
		}, "\x00")
		if index, exists := routeIndexes[key]; exists {
			if routes[index].route.ReplyToID == "" && route.ReplyToID != "" {
				routes[index].route.ReplyToID = route.ReplyToID
			}
			if source != "" && !strings.Contains(routes[index].source, source) {
				routes[index].source = strings.Trim(routes[index].source+"、"+source, "、")
			}
			return
		}
		routeIndexes[key] = len(routes)
		routes = append(routes, controllableRoute{route: route, source: source})
	}
	if holder == agent.CodexRuntimeWeClaw && taskInfo.inProcess {
		addRoute(taskInfo.writerDeliveryRoute, "WeClaw writer")
	}
	if external, controllable, denied := codexStatusCommandControlState(h, runtime, threadID); !denied && external && controllable {
		if currentRoute, ok := codexStatusCommandRoute(runtime); ok {
			addRoute(currentRoute, "当前命令")
		}
	}
	for _, snapshot := range snapshots {
		if runtime.agentName != "" && snapshot.AgentName != runtime.agentName {
			continue
		}
		if snapshot.Target.DeliveryRoute.Platform != platform.PlatformFeishu ||
			normalizeCodexWorkspaceRoot(snapshot.Target.WorkspaceRoot) != workspaceRoot ||
			strings.TrimSpace(snapshot.Target.ThreadID) != threadID {
			continue
		}
		if snapshot.AttachPhase != codexFollowerAttachReady ||
			!h.codexFollowerIdentityAuthorized(
				snapshot.Target.DeliveryRoute.Platform,
				snapshot.Target.DeliveryRoute.AccountID,
				snapshot.Target.AuthorizedIdentity,
			) {
			continue
		}
		trackedTurnID := firstNonBlank(
			strings.TrimSpace(snapshot.FollowTurnID),
			strings.TrimSpace(snapshot.AttachTurnID),
		)
		if trackedTurnID != "" && trackedTurnID != turnID {
			continue
		}
		external, controllable, denied := h.externalCodexControlState(
			snapshot.ConversationID, snapshot.Target.ActorUserID,
		)
		if denied || !external || !controllable {
			continue
		}
		addRoute(snapshot.Target.DeliveryRoute, "")
	}
	if len(routes) == 0 {
		return []string{"可控制通道: 当前没有已确认可控制的消息通道"}
	}
	lines := make([]string, 0, len(routes)+1)
	for _, candidate := range routes {
		capabilities := codexStatusControlCapabilities(holder)
		if candidate.source != "" {
			capabilities = candidate.source + "，" + capabilities
		}
		lines = append(lines, fmt.Sprintf(
			"- %s；%s",
			codexStatusDeliveryRouteIdentity(candidate.route),
			capabilities,
		))
	}
	return append([]string{"可控制通道:"}, lines...)
}

func codexStatusControlCapabilities(holder agent.CodexRuntimeHolder) string {
	switch holder {
	case agent.CodexRuntimeWeClaw:
		return "可发送普通输入、/guide、/stop"
	case agent.CodexRuntimeDesktop:
		return "可发送普通输入、/guide；/stop 需要在 Codex App 中执行"
	default:
		return "当前 Host 不可用，暂不能控制"
	}
}

func codexStatusCommandRouteLine(runtime codexSessionCommandRuntime) string {
	const prefix = "消息通道（本次命令）: "
	route, ok := codexStatusCommandRoute(runtime)
	if !ok {
		platformName := strings.TrimSpace(string(runtime.req.Platform))
		if platformName == "" {
			return prefix + "未知（回复器未暴露 DeliveryRoute）"
		}
		return prefix + "platform=" + platformName + "；route 未暴露，chat/reply_to 不可追踪"
	}
	return prefix + codexStatusDeliveryRouteIdentity(route)
}

func codexStatusCommandRoute(runtime codexSessionCommandRuntime) (platform.DeliveryRoute, bool) {
	reporter, ok := optionalDeliveryRouteReporter(progressReplier(runtime.req.Reply))
	if !ok {
		return platform.DeliveryRoute{}, false
	}
	route := reporter.DeliveryRoute()
	if route.Platform == "" {
		route.Platform = runtime.req.Platform
	}
	if strings.TrimSpace(route.AccountID) == "" {
		route.AccountID = strings.TrimSpace(runtime.req.AccountID)
	}
	return route, route.Valid()
}

func codexStatusFollowerRouteLines(
	h *Handler,
	runtime codexSessionCommandRuntime,
	threadID string,
	turnID string,
	holder agent.CodexRuntimeHolder,
	active bool,
) []string {
	threadID = strings.TrimSpace(threadID)
	if h == nil || threadID == "" || threadID == "(none)" {
		return []string{"Turn 观察通道: 当前没有已登记的飞书观察通道"}
	}
	workspaceRoot := normalizeCodexWorkspaceRoot(runtime.workspaceRoot)
	snapshots := h.ensureCodexSessions().followerSnapshots()
	lines := make([]string, 0, len(snapshots)+1)
	for _, snapshot := range snapshots {
		if runtime.agentName != "" && snapshot.AgentName != runtime.agentName {
			continue
		}
		if snapshot.Target.DeliveryRoute.Platform != platform.PlatformFeishu ||
			normalizeCodexWorkspaceRoot(snapshot.Target.WorkspaceRoot) != workspaceRoot ||
			strings.TrimSpace(snapshot.Target.ThreadID) != threadID {
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"- %s；%s；%s",
			codexStatusDeliveryRouteIdentity(snapshot.Target.DeliveryRoute),
			codexStatusFollowerTurnLabel(snapshot, turnID),
			codexStatusFollowerControlRole(h, snapshot, turnID, holder, active),
		))
	}
	if len(lines) == 0 {
		return []string{"Turn 观察通道: 当前没有已登记的飞书观察通道"}
	}
	return append([]string{"Turn 观察通道:"}, lines...)
}

func codexStatusFollowerControlRole(
	h *Handler,
	snapshot codexFollowerSnapshot,
	turnID string,
	holder agent.CodexRuntimeHolder,
	active bool,
) string {
	if h == nil || !active ||
		(holder != agent.CodexRuntimeWeClaw && holder != agent.CodexRuntimeDesktop) ||
		snapshot.AttachPhase != codexFollowerAttachReady ||
		!h.codexFollowerIdentityAuthorized(
			snapshot.Target.DeliveryRoute.Platform,
			snapshot.Target.DeliveryRoute.AccountID,
			snapshot.Target.AuthorizedIdentity,
		) {
		return "仅观察"
	}
	trackedTurnID := firstNonBlank(
		strings.TrimSpace(snapshot.FollowTurnID),
		strings.TrimSpace(snapshot.AttachTurnID),
	)
	turnID = strings.TrimSpace(turnID)
	if trackedTurnID != "" && trackedTurnID != turnID {
		return "仅观察"
	}
	external, controllable, denied := h.externalCodexControlState(
		snapshot.ConversationID, snapshot.Target.ActorUserID,
	)
	if denied || !external || !controllable {
		return "仅观察"
	}
	return "同时可控制"
}

func codexStatusDeliveryRouteIdentity(route platform.DeliveryRoute) string {
	return fmt.Sprintf("platform=%s account=%s chat=%s reply_to=%s",
		codexStatusRouteField(route.Platform), codexStatusRouteField(route.AccountID),
		codexStatusRouteField(route.ChatID), codexStatusRouteField(route.ReplyToID))
}

func codexStatusRouteField(value interface{}) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		return "(none)"
	}
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
}

func codexStatusFollowerTurnLabel(snapshot codexFollowerSnapshot, turnID string) string {
	turnID = strings.TrimSpace(turnID)
	trackedTurnID := firstNonBlank(strings.TrimSpace(snapshot.FollowTurnID), strings.TrimSpace(snapshot.AttachTurnID))
	if turnID == "" || turnID == "(none)" {
		if trackedTurnID == "" {
			return "当前 turn 未确认"
		}
		return "最近跟踪 turn=" + codexStatusRouteField(trackedTurnID)
	}
	if trackedTurnID == turnID {
		return "跟踪当前 turn"
	}
	if trackedTurnID == "" {
		return "已登记，当前 turn 游标未确认"
	}
	return "最近跟踪 turn=" + codexStatusRouteField(trackedTurnID) + "（当前 turn 游标未确认）"
}

func codexStatusCompatibilityDetails(runtime codexSessionCommandRuntime, threadID string) []string {
	return []string{
		"任务 ID: 未登记（兼容 Agent）",
		"thread: " + firstNonBlank(strings.TrimSpace(threadID), "(none)"),
		"turn: 未确认",
		"Host: 兼容 Agent（类型未提供）",
		"原始入口: 未记录",
		"当前窗口 binding: 已选择该 thread",
		"控制通道: 兼容 Agent（由当前窗口提供）",
		"等待: 未确认",
	}
}

func codexStatusUnavailableDetails(runtime codexSessionCommandRuntime, threadID string) []string {
	return []string{
		"任务 ID: 未确认",
		"thread: " + firstNonBlank(strings.TrimSpace(threadID), "(none)"),
		"turn: 未确认",
		"Host: 未确认",
		"原始入口: 未确认",
		"当前窗口 binding: 已选择该 thread",
		"控制通道: 不可用（尚未确认 Host）",
		"等待: 未确认",
	}
}

func codexStatusTaskIdentity(h *Handler, runtime codexSessionCommandRuntime, threadID string) codexStatusTaskInfo {
	conversationID := runtime.codexRoute(threadID).conversationID
	if task, ok := h.activeTask(conversationID); ok && task != nil {
		task.mu.Lock()
		info := codexStatusTaskInfo{
			taskID:              firstNonBlank(task.taskID, "未登记"),
			writerPlatform:      task.writerPlatform,
			writerAccountID:     task.writerAccountID,
			writerDeliveryRoute: task.writerDeliveryRoute,
			inProcess:           task.inProcessCodexLifecycle,
		}
		info.origin = "WeClaw 消息/CLI"
		if !task.inProcessCodexLifecycle {
			info.origin = "Codex App 或其他前端"
		}
		task.mu.Unlock()
		return info
	}
	return codexStatusTaskInfo{
		taskID: "未登记（可按 thread/turn 恢复）",
		origin: "未记录（权威 thread 未提供来源）",
	}
}

func codexStatusHostAndControl(runtime agent.CodexRuntimeHolder, origin string) (string, string) {
	switch runtime {
	case agent.CodexRuntimeWeClaw:
		switch strings.TrimSpace(origin) {
		case "WeClaw 消息/CLI":
			return "WeClaw 官方 daemon（共享）", "WeClaw 当前进程，可 steer/interrupt"
		case "Codex App 或其他前端":
			return "WeClaw 官方 daemon（共享）", "共享 daemon；外部前端 writer，具体客户端未暴露；WeClaw 可 steer/interrupt"
		default:
			return "WeClaw 官方 daemon（共享）", "共享 daemon；writer 未记录，具体客户端未暴露；WeClaw 可 steer/interrupt"
		}
	case agent.CodexRuntimeDesktop:
		return "Codex App Host", "Codex App Desktop IPC；WeClaw 可 steer，/stop 需要在 Codex App 中执行"
	case agent.CodexRuntimeConflict:
		return "冲突（双 Host）", "不可用（已阻止写入）"
	default:
		return "未确认", "不可用（尚未确认 Host）"
	}
}

func codexStatusWaitState(state agent.CodexThreadState) string {
	if !state.Active {
		return "无"
	}
	switch {
	case state.WaitingOnApproval && state.WaitingOnUserInput:
		return "审批/补充输入"
	case state.WaitingOnApproval:
		return "审批"
	case state.WaitingOnUserInput:
		return "补充输入"
	default:
		return "无"
	}
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
	return "账号: " + compactCodexStatusAccountIdentity(status)
}

// compactCodexStatusAccountIdentity 只保留 /cx status 做运行判断所需的标签。
// 账号管理命令仍可展示脱敏邮箱，但日常状态卡不应重复暴露账号详情。
func compactCodexStatusAccountIdentity(status agent.CodexAccountStatus) string {
	current := status.Store.Current
	auth := status.Sync.AuthProfile
	switch status.Sync.State {
	case agent.CodexAccountSyncPending:
		if auth == nil {
			return "等待自动同步"
		}
		if current == nil || current.ID == auth.ID {
			return auth.Label + "（待自动同步）"
		}
		return current.Label + " → " + auth.Label + "（待自动同步）"
	case agent.CodexAccountSyncUnsaved:
		return "本地账号未保存"
	case agent.CodexAccountSyncRuntimeMismatch:
		if auth != nil {
			return auth.Label + "（运行账号不一致）"
		}
		return "运行账号不一致"
	case agent.CodexAccountSyncRuntimeUnavailable:
		if auth != nil {
			return auth.Label + "（运行账号未确认）"
		}
		return "运行账号未确认"
	case agent.CodexAccountSyncSynced:
		if auth != nil {
			return auth.Label
		}
	}
	if current == nil {
		return "未保存"
	}
	return current.Label
}

func compactCodexRuntimeStatusLines(resolution codexRuntimeResolution) (string, string) {
	taskLine := "任务: 空闲"
	if resolution.Binding.State.Active || resolution.Rollout.Active {
		taskLine = "任务: 正在执行"
	}
	runtimeLine := "运行通道: 不可用（未确认）"
	switch resolution.Binding.Runtime {
	case agent.CodexRuntimeWeClaw:
		runtimeLine = "运行通道: 可用"
	case agent.CodexRuntimeConflict:
		runtimeLine = "运行通道: 不可用（Host 冲突）"
	case agent.CodexRuntimeDesktop:
		runtimeLine = "运行通道: 可用（Codex App）"
	}
	return taskLine, runtimeLine
}

func compactCodexStatusResult(base string, bindingLine string, taskLine string, accountLine string, runtimeLine string, extraLines ...string) navigationCommandResult {
	lines := []string{base, bindingLine, taskLine, accountLine, runtimeLine}
	lines = append(lines, extraLines...)
	return textNavigationResult(wechatCommandText(lines...))
}

func (h *Handler) codexProgressSyncStatusLine(bindingKey string, threadID string) string {
	snapshot, ok := h.ensureCodexSessions().followerSnapshot(bindingKey)
	if !ok || strings.TrimSpace(snapshot.Target.ThreadID) != strings.TrimSpace(threadID) {
		return "进度同步: 正常"
	}
	h.codexFollowerMu.Lock()
	service := h.codexFollower
	h.codexFollowerMu.Unlock()
	if service != nil && service.synchronizationDegraded(bindingKey, threadID) {
		return "进度同步: 已降级"
	}
	if snapshot.AttachPhase != codexFollowerAttachReady {
		return "进度同步: 同步中"
	}
	return "进度同步: 正常"
}

func (runtime codexSessionCommandRuntime) codexRoute(threadID string) codexConversationRoute {
	return codexConversationRoute{
		bindingKey: runtime.bindingKey, workspaceRoot: runtime.workspaceRoot,
		conversationID: buildCodexConversationID(runtime.routeUserID, runtime.agentName, runtime.workspaceRoot),
		threadID:       strings.TrimSpace(threadID),
	}
}
