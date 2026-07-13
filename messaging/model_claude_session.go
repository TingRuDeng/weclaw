package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type modelSettingStatusOverride struct {
	modelSettingController
	status modelSettingStatus
}

// Status 返回当前 Claude session 的配置，而不是新会话默认值。
func (c modelSettingStatusOverride) Status() modelSettingStatus {
	return c.status
}

type claudeSessionSettingTarget struct {
	agent          agent.ClaudeSessionConfigAgent
	conversationID string
	config         agent.ClaudeSessionConfig
}

type claudeModelSettingRequest struct {
	ctx    context.Context
	route  modelAgentRoute
	name   string
	agent  agent.Agent
	model  string
	effort string
}

type modelSettingControllerRequest struct {
	route      modelAgentRoute
	name       string
	agent      agent.Agent
	controller modelSettingController
}

// withCurrentClaudeSessionStatus 为模型卡片和文本查询覆盖当前 session 状态。
func (h *Handler) withCurrentClaudeSessionStatus(req modelSettingControllerRequest) modelSettingController {
	target, ok := h.currentClaudeSessionSettingTarget(req.route, req.name, req.agent)
	if !ok {
		return req.controller
	}
	return modelSettingStatusOverride{
		modelSettingController: req.controller,
		status:                 modelSettingStatus{Model: target.config.Model, Effort: target.config.Effort},
	}
}

// setCurrentClaudeSessionSetting 把模型或推理强度写入当前已绑定的 Claude session。
func (h *Handler) setCurrentClaudeSessionSetting(req claudeModelSettingRequest) (string, bool) {
	if strings.TrimSpace(req.route.routeUserID) == "" || !isClaudeAgent(req.name, req.agent.Info()) {
		return "", false
	}
	if _, ok := req.agent.(agent.ClaudeSessionConfigAgent); !ok {
		return "", false
	}
	bindingKey := claudeBindingKey(req.route.routeUserID, req.name)
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(bindingKey))
	defer unlock()
	target, ok := h.currentClaudeSessionSettingTarget(req.route, req.name, req.agent)
	if !ok {
		return "当前窗口没有有效 Claude session，请先发送 /cc ls 选择或 /cc new 新建。", true
	}
	update := agent.ClaudeSessionConfigUpdate{
		ConversationID: target.conversationID, Model: req.model, Effort: req.effort,
	}
	if err := target.agent.SetClaudeSessionConfig(req.ctx, update); err != nil {
		return fmt.Sprintf("切换当前 Claude session 配置失败: %v", err), true
	}
	return renderCurrentClaudeSessionSetting(req.model, req.effort), true
}

// currentClaudeSessionSettingTarget 解析当前 route 的 ready 绑定与运行时配置。
func (h *Handler) currentClaudeSessionSettingTarget(route modelAgentRoute, name string, ag agent.Agent) (claudeSessionSettingTarget, bool) {
	configAgent, ok := ag.(agent.ClaudeSessionConfigAgent)
	if !ok || strings.TrimSpace(route.routeUserID) == "" {
		return claudeSessionSettingTarget{}, false
	}
	binding := h.ensureClaudeSessions().binding(claudeBindingKey(route.routeUserID, name))
	if binding.Status != claudeBindingReady || binding.SessionID == "" || binding.WorkspaceRoot == "" {
		return claudeSessionSettingTarget{}, false
	}
	conversationID := buildClaudeConversationID(route.routeUserID, name, binding.WorkspaceRoot)
	config, _ := configAgent.ClaudeSessionConfig(conversationID)
	return claudeSessionSettingTarget{
		agent: configAgent, conversationID: conversationID, config: config,
	}, true
}

// renderCurrentClaudeSessionSetting 返回当前 session 已生效的明确反馈。
func renderCurrentClaudeSessionSetting(model string, effort string) string {
	if strings.TrimSpace(model) != "" {
		return wechatCommandText("已将当前 Claude session 模型切换为: " + model)
	}
	return wechatCommandText("已将当前 Claude session 推理强度切换为: " + effort)
}
