package messaging

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

// codexAgentTaskOptions 保存 Codex 后台任务需要的上下文，避免长参数列表掩盖调用意图。
type codexAgentTaskOptions struct {
	ctx         context.Context
	platform    platform.PlatformName
	accountID   string
	userID      string
	routeUserID string
	reply       platform.Replier
	agentName   string
	message     string
	clientID    string
	messageKey  string
	replyPrefix string
	agent       agent.Agent
	progressCfg config.ProgressConfig
	route       codexConversationRoute
	trace       observability.TraceContext
}

// codexAgentTaskRuntime 保存已经登记 active task 后的运行时资源。
type codexAgentTaskRuntime struct {
	opts              codexAgentTaskOptions
	agentCtx          context.Context
	cancelTaskTimeout context.CancelFunc
	executionKey      string
	route             codexConversationRoute
	task              *activeAgentTask
}

type codexConversationRoute struct {
	bindingKey     string
	workspaceRoot  string
	conversationID string
	threadID       string
}

// codexTaskWriterRoute captures the message endpoint that initiated a local
// Codex turn. It is separate from follower routes, which only observe a turn.
func codexTaskWriterRoute(platformName platform.PlatformName, accountID string, reply platform.Replier) platform.DeliveryRoute {
	route := platform.DeliveryRoute{Platform: platformName, AccountID: accountID}
	if reporter, ok := optionalDeliveryRouteReporter(progressReplier(reply)); ok {
		route = reporter.DeliveryRoute()
		if route.Platform == "" {
			route.Platform = platformName
		}
		if route.AccountID == "" {
			route.AccountID = accountID
		}
	}
	route.AccountID = strings.TrimSpace(route.AccountID)
	route.ChatID = strings.TrimSpace(route.ChatID)
	route.ReplyToID = strings.TrimSpace(route.ReplyToID)
	return route
}
