package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

type platformMessageRuntime struct {
	ctx         context.Context
	msg         platform.IncomingMessage
	reply       platform.Replier
	routeUserID string
	text        string
	clientID    string
	trace       observability.TraceContext
}

// sendText 向真实发送者回复文本，不使用会话路由键替代用户 ID。
func (runtime platformMessageRuntime) sendText(text string) {
	sendPlatformText(runtime.ctx, runtime.reply, runtime.msg.UserID, text)
}

// agentRequest 将平台消息上下文转换为统一 Agent 请求。
func (runtime platformMessageRuntime) agentRequest(message string) agentMessageRequest {
	return agentMessageRequest{
		ctx: runtime.ctx, platformName: runtime.msg.Platform, accountID: runtime.msg.AccountID,
		userID: runtime.msg.UserID, routeUserID: runtime.routeUserID, reply: runtime.reply,
		message: message, clientID: runtime.clientID, trace: runtime.trace,
	}
}

func platformMessageText(msg platform.IncomingMessage) string {
	if msg.RawCommand != nil && msg.RawCommand.Action == "choice" {
		return strings.TrimSpace(msg.RawCommand.Value["choice"])
	}
	return msg.Text
}

// platformMessageSessionKey 返回平台 adapter 明确传入的会话 key。
func platformMessageSessionKey(msg platform.IncomingMessage) string {
	if sessionKey := msg.SessionRouteKey(); sessionKey != "" {
		return sessionKey
	}
	if msg.Platform == platform.PlatformFeishu && msg.Metadata != nil {
		return strings.TrimSpace(msg.Metadata[feishuSessionMetadataKey])
	}
	return ""
}

// platformMessageRouteUserID 返回 agent 会话路由使用的用户维度，不改变真实发送者 ID。
func platformMessageRouteUserID(msg platform.IncomingMessage) string {
	if sessionKey := platformMessageSessionKey(msg); sessionKey != "" {
		return sessionKey
	}
	return msg.UserID
}

func platformMessageDedupKey(msg platform.IncomingMessage) string {
	return strings.TrimSpace(string(msg.Platform)) + "\x00" + strings.TrimSpace(msg.AccountID) + "\x00" + strings.TrimSpace(msg.MessageID)
}

// isDuplicatePlatformMessage 使用稳定消息 ID 拦截平台重复投递。
func (h *Handler) isDuplicatePlatformMessage(msg platform.IncomingMessage) bool {
	if msg.MessageID == "" || msg.MessageID == "0" {
		return false
	}
	if _, loaded := h.seenMsgs.LoadOrStore(platformMessageDedupKey(msg), time.Now()); loaded {
		return true
	}
	h.maybeCleanSeenMsgs(time.Now())
	return false
}

// handlePlatformRawCommand 优先处理停止按钮和审批卡片动作。
func (h *Handler) handlePlatformRawCommand(runtime platformMessageRuntime) bool {
	command := runtime.msg.RawCommand
	if command == nil {
		return false
	}
	if command.Action == "stop" {
		req := taskCommandRequest{
			ctx: runtime.ctx, platformName: runtime.msg.Platform, accountID: runtime.msg.AccountID,
			actorUserID: runtime.msg.UserID, routeUserID: runtime.routeUserID, reply: runtime.reply,
		}
		runtime.sendText(h.handleStopActiveTask(req))
		return true
	}
	if command.Action != "choice" {
		return false
	}
	if isPendingInteractionChoiceCommand(command) && h.consumePendingInteractionForKey(
		runtime.msg.UserID, runtime.routeUserID,
		command.Value[platform.ChoiceMetadataInteractionKind],
		command.Value["approval_key"], command.Value["choice"],
	) {
		reportCardActionResult(command, platform.CardActionResultConsumed)
		return true
	}
	if !isPendingInteractionChoiceCommand(command) {
		return false
	}
	if !reportCardActionResult(command, platform.CardActionResultExpired) {
		runtime.sendText(staleApprovalReply())
	}
	return true
}

// preparePlatformMessage 依次消费审批、附件和文本去重，并创建客户端请求 ID。
func (h *Handler) preparePlatformMessage(runtime platformMessageRuntime) (platformMessageRuntime, bool) {
	switch h.consumePendingApprovalText(runtime.msg.UserID, runtime.routeUserID, runtime.text) {
	case approvalTextConsumed:
		return runtime, false
	case approvalTextAmbiguous:
		runtime.sendText("当前有多个待审批操作，无法判断这条回复对应哪一个。请点击目标审批卡片中的按钮。")
		return runtime, false
	}
	prepared, ok := h.preparePlatformAttachments(runtime)
	if !ok || !h.acceptPreparedPlatformText(prepared) {
		return prepared, false
	}
	prepared.clientID = NewClientID()
	prepared.trace = prepared.trace.WithClientID(prepared.clientID)
	prepared.ctx = observability.ContextWithTrace(prepared.ctx, prepared.trace)
	if setter, ok := prepared.reply.(platform.ClientIDSetter); ok {
		setter.SetClientID(prepared.clientID)
	}
	log.Printf("[handler] received from %s: %q", prepared.msg.UserID, truncate(platformMessageLogText(prepared.text), 80))
	h.recordTraceStage(prepared.trace, "message.accepted", "accepted", traceSummaryForIncoming(prepared.msg, prepared.text))
	return prepared, true
}

// platformMessageLogText 隐去一次性确认能力，避免日志中的短期凭据被重放。
func platformMessageLogText(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 4 && strings.EqualFold(fields[0], "/cx") &&
		strings.EqualFold(fields[1], "account") && strings.EqualFold(fields[2], "confirm") {
		return "/cx account confirm <redacted>"
	}
	if len(fields) == 2 && (strings.EqualFold(fields[0], "/approve") || strings.EqualFold(fields[0], "/deny")) {
		return fields[0] + " <redacted>"
	}
	return text
}

// preparePlatformAttachments 将文件和带说明的图片转换为 Agent 可消费文本。
func (h *Handler) preparePlatformAttachments(runtime platformMessageRuntime) (platformMessageRuntime, bool) {
	if file, ok := firstAttachment(runtime.msg.Attachments, platform.AttachmentFile); ok {
		text, handled := h.handleFileAttachment(runtime.ctx, runtime.msg.UserID, runtime.reply, file, runtime.text)
		if !handled {
			return runtime, false
		}
		runtime.text = strings.TrimSpace(text)
	}
	if image, ok := firstAttachment(runtime.msg.Attachments, platform.AttachmentImage); ok && runtime.text != "" {
		text, handled := h.handleImageAttachment(runtime.ctx, runtime.msg.UserID, runtime.reply, image, runtime.text)
		if !handled {
			return runtime, false
		}
		runtime.text = strings.TrimSpace(text)
	}
	return runtime, true
}

// acceptPreparedPlatformText 处理纯图片保存、空消息和无消息 ID 的文本去重。
func (h *Handler) acceptPreparedPlatformText(runtime platformMessageRuntime) bool {
	if runtime.text == "" {
		if image, ok := firstAttachment(runtime.msg.Attachments, platform.AttachmentImage); ok && h.saveDirectory() != "" {
			h.handleImageAttachmentSave(runtime.ctx, runtime.msg.UserID, runtime.reply, image)
			return false
		}
		log.Printf("[handler] received non-text message from %s, skipping", runtime.msg.UserID)
		return false
	}
	if runtime.msg.MessageID != "" && runtime.msg.MessageID != "0" {
		return true
	}
	if !h.isDuplicateTextMessage(runtime.msg.UserID, runtime.msg.ContextToken, runtime.routeUserID, runtime.text) {
		return true
	}
	runtime.sendText(duplicateTaskReply())
	return false
}

// dispatchPlatformMessage 先处理内置能力，再执行普通 Agent 消息。
func (h *Handler) dispatchPlatformMessage(runtime platformMessageRuntime) {
	trimmed := strings.TrimSpace(runtime.text)
	if h.trySavePlatformURL(runtime, trimmed) {
		return
	}
	if h.handleBuiltInPlatformCommand(runtime.ctx, platformCommandRequest{
		Message: runtime.msg, RouteUserID: runtime.routeUserID, Reply: runtime.reply,
		Trimmed: trimmed, ClientID: runtime.clientID,
	}) {
		return
	}
	if !h.allowAgentInvocation(runtime.routeUserID) {
		log.Printf("[handler] rate limit exceeded for %s", runtime.routeUserID)
		runtime.sendText("请求过于频繁，请稍后再试。")
		return
	}
	h.auditRecord(auditEntry{
		Platform: string(runtime.msg.Platform), User: runtime.msg.UserID,
		Action: "agent_message", Summary: auditMessageSummary(runtime.text),
	})
	h.dispatchParsedAgentMessage(runtime)
}

// trySavePlatformURL 在配置保存目录时优先收录单独发送的链接。
func (h *Handler) trySavePlatformURL(runtime platformMessageRuntime, trimmed string) bool {
	saveDir := h.saveDirectory()
	if saveDir == "" || !IsURL(trimmed) {
		return false
	}
	rawURL := ExtractURL(trimmed)
	if rawURL == "" {
		return false
	}
	log.Printf("[handler] saving URL to linkhoard: %s", rawURL)
	title, err := SaveLinkToLinkhoard(runtime.ctx, saveDir, rawURL)
	if err != nil {
		log.Printf("[handler] link save failed: %v", err)
		runtime.sendText(fmt.Sprintf("保存失败: %v", err))
		return true
	}
	runtime.sendText(fmt.Sprintf("已保存: %s", title))
	return true
}

// dispatchParsedAgentMessage 解析 Agent 别名并选择默认、单播或广播路径。
func (h *Handler) dispatchParsedAgentMessage(runtime platformMessageRuntime) {
	agentNames, message := h.parseCommand(runtime.text)
	if len(agentNames) == 0 {
		h.sendToDefaultAgent(runtime.agentRequest(runtime.text))
		return
	}
	if message == "" {
		h.handleAgentSwitchMessage(runtime, agentNames)
		return
	}
	knownNames := h.knownAgentNames(agentNames)
	if len(knownNames) == 0 {
		h.sendToDefaultAgent(runtime.agentRequest(runtime.text))
		return
	}
	if len(knownNames) == 1 {
		req := runtime.agentRequest(message)
		req.name = knownNames[0]
		h.sendToNamedAgent(req)
		return
	}
	h.broadcastToAgents(newBroadcastAgentsRequest(runtime, knownNames, message))
}

// handleAgentSwitchMessage 处理仅包含 Agent 名称的窗口切换消息。
func (h *Handler) handleAgentSwitchMessage(runtime platformMessageRuntime, names []string) {
	if len(names) != 1 {
		runtime.sendText("Usage: specify one agent to switch, or add a message to broadcast")
		return
	}
	if !h.isKnownAgent(names[0]) {
		h.sendToDefaultAgent(runtime.agentRequest(runtime.text))
		return
	}
	runtime.sendText(h.switchDefault(runtime.ctx, runtime.routeUserID, names[0]))
}

// knownAgentNames 过滤未配置的 Agent 名称，保持原始顺序。
func (h *Handler) knownAgentNames(names []string) []string {
	known := make([]string, 0, len(names))
	for _, name := range names {
		if h.isKnownAgent(name) {
			known = append(known, name)
		}
	}
	return known
}

// newBroadcastAgentsRequest 组装多 Agent 广播请求。
func newBroadcastAgentsRequest(runtime platformMessageRuntime, names []string, message string) broadcastAgentsRequest {
	return broadcastAgentsRequest{
		ctx: runtime.ctx, platformName: runtime.msg.Platform, accountID: runtime.msg.AccountID,
		userID: runtime.msg.UserID, routeUserID: runtime.routeUserID, replyWriter: runtime.reply,
		names: names, message: message, clientID: runtime.clientID, trace: runtime.trace,
	}
}
