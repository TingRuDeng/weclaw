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
	msg := req.Message
	routeUserID := strings.TrimSpace(req.RouteUserID)
	if routeUserID == "" {
		routeUserID = msg.UserID
	}
	trimmed := req.Trimmed
	sendText := func(text string) {
		sendPlatformText(ctx, req.Reply, msg.UserID, text)
	}
	switch {
	case isServiceAdminCommand(trimmed):
		h.handleServiceAdminCommand(ctx, msg, trimmed, req.Reply)
	case isFeishuIdentityCommand(trimmed):
		sendText(h.handleFeishuIdentityCommand(msg, trimmed))
	case trimmed == "/status":
		sendText(h.buildStatusForRoute(msg.UserID, routeUserID, msg.Platform, msg.AccountID))
	case trimmed == "/help":
		if h.handleFeishuHelpCommand(ctx, msg, req.Reply, routeUserID) {
			return true
		}
		sendText(buildHelpTextForAdmin(h.isAdminMessage(msg)))
	case trimmed == "/new":
		sendText(h.resetDefaultSessionForMessage(ctx, defaultSessionResetRequest{
			actorUserID: msg.UserID,
			routeUserID: routeUserID,
			platform:    msg.Platform,
			accountID:   msg.AccountID,
		}))
	case isProgressCommand(trimmed):
		sendText(h.handleProgressCommandForAccount(trimmed, msg.Platform, msg.AccountID))
	case isClaudeSessionCommand(trimmed):
		return h.handleClaudeSessionPlatformCommand(ctx, req, routeUserID)
	case isCodexSessionCommand(trimmed):
		return h.handleCodexSessionPlatformCommand(ctx, req, routeUserID)
	case trimmed == "/guide":
		h.handleGuideCommand(ctx, msg.Platform, msg.AccountID, msg.UserID, routeUserID, req.Reply, req.ClientID)
	case trimmed == "/cancel":
		sendText(h.handleCancelPendingGuide(ctx, msg.UserID, routeUserID))
	case trimmed == "/stop":
		sendText(h.handleStopActiveTask(ctx, msg.UserID, routeUserID))
	case trimmed == "/ps":
		sendText(h.handleListActiveTasks(msg.UserID))
	case trimmed == "/mode" || strings.HasPrefix(trimmed, "/mode "):
		sendText(h.handleModeCommand(msg.UserID, trimmed))
	case isModelSettingCommand(trimmed):
		return h.handleModelSettingPlatformCommand(ctx, req)
	case strings.HasPrefix(trimmed, "/cwd"):
		sendText(h.handleCwdForMessage(trimmed, msg))
	default:
		return false
	}
	return true
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
	if h.handleFeishuCodexSessionCommand(ctx, msg, routeUserID, req.Reply, req.Trimmed) {
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
