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
		sendText(h.buildStatus(msg.UserID))
	case trimmed == "/help":
		if h.handleFeishuHelpCommand(ctx, msg, req.Reply, routeUserID) {
			return true
		}
		sendText(buildHelpTextForAdmin(h.isAdminMessage(msg)))
	case trimmed == "/new":
		sendText(h.resetDefaultSessionForRoute(ctx, msg.UserID, routeUserID))
	case isProgressCommand(trimmed):
		sendText(h.handleProgressCommandForAccount(trimmed, msg.Platform, msg.AccountID))
	case isClaudeSessionCommand(trimmed):
		sendText(h.handleClaudeSessionCommand(ctx, msg.UserID, trimmed))
	case isCodexSessionCommand(trimmed):
		if h.handleFeishuCodexSessionCommand(ctx, msg, routeUserID, req.Reply, trimmed) {
			return true
		}
		sendText(h.handleCodexSessionCommandForRoute(ctx, codexSessionCommandRequest{
			ActorUserID: msg.UserID,
			RouteUserID: routeUserID,
			Trimmed:     trimmed,
			Platform:    msg.Platform,
			AccountID:   msg.AccountID,
			Reply:       req.Reply,
		}))
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
	case trimmed == "/model" || strings.HasPrefix(trimmed, "/model "):
		sendText(h.handleModelCommandForAccount(ctx, msg.Platform, msg.AccountID, strings.TrimSpace(strings.TrimPrefix(trimmed, "/model"))))
	case trimmed == "/reasoning" || strings.HasPrefix(trimmed, "/reasoning "):
		sendText(h.handleReasoningCommandForAccount(ctx, msg.Platform, msg.AccountID, strings.TrimSpace(strings.TrimPrefix(trimmed, "/reasoning"))))
	case strings.HasPrefix(trimmed, "/cwd"):
		sendText(h.handleCwd(trimmed, msg.UserID))
	default:
		return false
	}
	return true
}
