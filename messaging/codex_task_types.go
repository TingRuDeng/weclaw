package messaging

import (
	"context"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

// codexAgentTaskOptions 保存 Codex 后台任务需要的上下文，避免长参数列表掩盖调用意图。
type codexAgentTaskOptions struct {
	ctx         context.Context
	platform    platform.PlatformName
	userID      string
	routeUserID string
	reply       platform.Replier
	agentName   string
	message     string
	clientID    string
	replyPrefix string
	agent       agent.Agent
	progressCfg config.ProgressConfig
	route       codexConversationRoute
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
