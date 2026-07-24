package messaging

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

// platformCommandRequest 承载内置命令上下文，Message.UserID 是操作者，RouteUserID 是会话路由键。
type platformCommandRequest struct {
	Message     platform.IncomingMessage
	RouteUserID string
	Reply       platform.Replier
	Trimmed     string
	ClientID    string
}

// handleBuiltInPlatformCommand 处理平台内置命令，飞书使用 routeUserID 控制会话但保留真实发送者回复。
func (h *Handler) handleBuiltInPlatformCommand(ctx context.Context, req platformCommandRequest) bool {
	routeUserID := strings.TrimSpace(req.RouteUserID)
	if routeUserID == "" {
		routeUserID = req.Message.UserID
	}
	return h.routeBuiltInPlatformCommand(ctx, req, routeUserID)
}

// routeBuiltInPlatformCommand 按命令类型分发，保持平台入口只负责上下文装配。
func (h *Handler) routeBuiltInPlatformCommand(ctx context.Context, req platformCommandRequest, routeUserID string) bool {
	if h.routeServicePlatformCommand(ctx, req, routeUserID) {
		return true
	}
	if h.routeSessionPlatformCommand(ctx, req, routeUserID) {
		return true
	}
	return h.routeTaskPlatformCommand(ctx, req, routeUserID)
}

// routeServicePlatformCommand 处理服务管理、身份、状态、帮助和新会话命令。
func (h *Handler) routeServicePlatformCommand(ctx context.Context, req platformCommandRequest, routeUserID string) bool {
	msg := req.Message
	switch {
	case isServiceAdminCommand(req.Trimmed):
		h.handleServiceAdminCommand(ctx, msg, routeUserID, req.Trimmed, req.Reply)
	case isFeishuIdentityCommand(req.Trimmed):
		replyPlatformCommand(ctx, req, h.handleFeishuIdentityCommand(msg, req.Trimmed))
	case req.Trimmed == "/status":
		replyPlatformCommand(ctx, req, h.buildStatusForRoute(msg.UserID, routeUserID, msg.Platform, msg.AccountID))
	case req.Trimmed == "/help" || strings.HasPrefix(req.Trimmed, "/help "):
		if h.handleFeishuHelpCommand(ctx, msg, req.Reply, routeUserID, req.Trimmed) {
			return true
		}
		replyPlatformCommand(ctx, req, buildHelpTextForAdmin(h.isAdminMessage(msg)))
	case req.Trimmed == "/new":
		replyPlatformCommand(ctx, req, h.resetDefaultSessionForMessage(ctx, defaultSessionResetRequest{
			actorUserID: msg.UserID,
			routeUserID: routeUserID,
			platform:    msg.Platform,
			accountID:   msg.AccountID,
			reply:       req.Reply,
		}))
	default:
		return false
	}
	return true
}

// routeSessionPlatformCommand 处理进度、会话导航、模型和工作目录命令。
func (h *Handler) routeSessionPlatformCommand(ctx context.Context, req platformCommandRequest, routeUserID string) bool {
	msg := req.Message
	switch {
	case isProgressCommand(req.Trimmed):
		if len(strings.Fields(req.Trimmed)) > 1 && !h.isAdminMessage(msg) {
			replyPlatformCommand(ctx, req, "仅管理员可以修改当前机器人账号的进度模式。")
			return true
		}
		replyPlatformCommand(ctx, req, h.handleProgressCommandForAccount(req.Trimmed, msg.Platform, msg.AccountID))
	case isClaudeSessionCommand(req.Trimmed):
		return h.handleClaudeSessionPlatformCommand(ctx, req, routeUserID)
	case isCodexSessionCommand(req.Trimmed):
		return h.handleCodexSessionPlatformCommand(ctx, req, routeUserID)
	case isModelSettingCommand(req.Trimmed):
		return h.handleModelSettingPlatformCommand(ctx, req)
	case isCwdCommand(req.Trimmed):
		replyPlatformCommand(ctx, req, h.handleCwdForMessage(req.Trimmed, msg, routeUserID))
	default:
		return false
	}
	return true
}

// routeTaskPlatformCommand 处理排队、停止、任务列表和权限模式命令。
func (h *Handler) routeTaskPlatformCommand(ctx context.Context, req platformCommandRequest, routeUserID string) bool {
	msg := req.Message
	switch {
	case isApprovalFallbackCommand(req.Trimmed):
		replyPlatformCommand(ctx, req, h.handleApprovalFallbackCommand(msg.UserID, routeUserID, req.Trimmed))
	case req.Trimmed == "/guide":
		h.handleGuideCommand(newTaskCommandRequest(ctx, req, routeUserID))
	case req.Trimmed == "/cancel":
		replyPlatformCommand(ctx, req, h.handleCancelPendingGuide(newTaskCommandRequest(ctx, req, routeUserID)))
	case req.Trimmed == "/stop":
		replyPlatformCommand(ctx, req, h.handleStopActiveTask(newTaskCommandRequest(ctx, req, routeUserID)))
	case req.Trimmed == "/ps":
		replyPlatformCommand(ctx, req, h.handleListActiveTasks(msg.UserID))
	case req.Trimmed == "/mode" || strings.HasPrefix(req.Trimmed, "/mode "):
		return h.handleModePlatformCommand(ctx, req, routeUserID)
	default:
		return false
	}
	return true
}

func isApprovalFallbackCommand(trimmed string) bool {
	fields := strings.Fields(strings.TrimSpace(trimmed))
	if len(fields) == 0 {
		return false
	}
	return strings.EqualFold(fields[0], "/approve") || strings.EqualFold(fields[0], "/deny")
}

func (h *Handler) handleApprovalFallbackCommand(actorUserID string, routeUserID string, trimmed string) string {
	fields := strings.Fields(strings.TrimSpace(trimmed))
	if len(fields) != 2 {
		return "用法：/approve <审批短码> 允许操作；/deny <审批短码> 拒绝操作。"
	}
	approve := strings.EqualFold(fields[0], "/approve")
	result := h.consumePendingApprovalCode(actorUserID, routeUserID, fields[1], approve)
	switch result {
	case approvalCodeConsumed:
		action := "拒绝"
		if approve {
			action = "允许"
		}
		h.auditRecord(auditEntry{User: actorUserID, Action: "approval_text_fallback", Summary: action})
		return "已提交审批：" + action + "。"
	case approvalCodeAlreadyResolved:
		return "该审批已处理，无需重复操作。"
	case approvalCodeDecisionUnavailable:
		return "该审批不支持这个操作，请使用审批卡片中的可用选项。"
	default:
		return "审批短码无效、已过期，或不属于当前窗口。"
	}
}

// replyPlatformCommand 始终向真实发送者回复命令结果。
func replyPlatformCommand(ctx context.Context, req platformCommandRequest, text string) {
	sendPlatformText(ctx, req.Reply, req.Message.UserID, text)
}

// newTaskCommandRequest 从平台命令中提取任务控制所需上下文。
func newTaskCommandRequest(ctx context.Context, req platformCommandRequest, routeUserID string) taskCommandRequest {
	return taskCommandRequest{
		ctx: ctx, platformName: req.Message.Platform, accountID: req.Message.AccountID,
		actorUserID: req.Message.UserID, routeUserID: routeUserID,
		reply: req.Reply, clientID: req.ClientID,
	}
}

// handleClaudeSessionPlatformCommand 为飞书提供卡片导航，其他平台继续使用文本命令。
func (h *Handler) handleClaudeSessionPlatformCommand(ctx context.Context, req platformCommandRequest, routeUserID string) bool {
	msg := req.Message
	cardReq := claudeFeishuCommandRequest{Context: ctx, Message: msg, RouteUserID: routeUserID, Reply: req.Reply, Trimmed: req.Trimmed}
	if h.handleFeishuClaudeSessionCommand(cardReq) {
		return true
	}
	text := h.handleClaudeSessionCommandForRoute(ctx, msg.UserID, routeUserID, h.isAdminMessage(msg), req.Trimmed)
	sendPlatformText(ctx, req.Reply, msg.UserID, text)
	return true
}

// handleCodexSessionPlatformCommand 隔离较长的 Codex 会话参数装配。
func (h *Handler) handleCodexSessionPlatformCommand(ctx context.Context, req platformCommandRequest, routeUserID string) bool {
	msg := req.Message
	if h.handleFeishuCodexSessionCommand(feishuCodexSessionCommandRequest{
		ctx: ctx, message: msg, routeUserID: routeUserID,
		reply: req.Reply, trimmed: req.Trimmed,
	}) {
		return true
	}
	text := h.handleCodexSessionCommandForRoute(ctx, codexSessionCommandRequest{
		ActorUserID: msg.UserID,
		RouteUserID: routeUserID,
		Trimmed:     req.Trimmed,
		Platform:    msg.Platform,
		AccountID:   msg.AccountID,
		Reply:       req.Reply,
		Admin:       h.isAdminMessage(msg),
		Private:     isPrivateCodexCommandMessage(msg, routeUserID),
	})
	sendPlatformText(ctx, req.Reply, msg.UserID, text)
	return true
}
