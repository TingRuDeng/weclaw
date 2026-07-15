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
		h.handleServiceAdminCommand(ctx, msg, req.Trimmed, req.Reply)
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
		replyPlatformCommand(ctx, req, h.handleProgressCommandForAccount(req.Trimmed, msg.Platform, msg.AccountID))
	case isClaudeSessionCommand(req.Trimmed):
		return h.handleClaudeSessionPlatformCommand(ctx, req, routeUserID)
	case isCodexSessionCommand(req.Trimmed):
		return h.handleCodexSessionPlatformCommand(ctx, req, routeUserID)
	case isModelSettingCommand(req.Trimmed):
		return h.handleModelSettingPlatformCommand(ctx, req)
	case strings.HasPrefix(req.Trimmed, "/cwd"):
		replyPlatformCommand(ctx, req, h.handleCwdForMessage(req.Trimmed, msg))
	default:
		return false
	}
	return true
}

// routeTaskPlatformCommand 处理排队、停止、任务列表和权限模式命令。
func (h *Handler) routeTaskPlatformCommand(ctx context.Context, req platformCommandRequest, routeUserID string) bool {
	msg := req.Message
	switch {
	case req.Trimmed == "/guide":
		h.handleGuideCommand(newTaskCommandRequest(ctx, req, routeUserID))
	case req.Trimmed == "/cancel":
		replyPlatformCommand(ctx, req, h.handleCancelPendingGuide(newTaskCommandRequest(ctx, req, routeUserID)))
	case req.Trimmed == "/stop":
		replyPlatformCommand(ctx, req, h.handleStopActiveTask(newTaskCommandRequest(ctx, req, routeUserID)))
	case req.Trimmed == "/ps":
		replyPlatformCommand(ctx, req, h.handleListActiveTasks(msg.UserID))
	case req.Trimmed == "/mode" || strings.HasPrefix(req.Trimmed, "/mode "):
		replyPlatformCommand(ctx, req, h.handleModeCommandForActor(routeUserID, msg.UserID, req.Trimmed))
	default:
		return false
	}
	return true
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
	})
	sendPlatformText(ctx, req.Reply, msg.UserID, text)
	return true
}
