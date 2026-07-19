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

// Status 返回当前已绑定 session/thread 的配置，而不是新会话默认值。
func (c modelSettingStatusOverride) Status() modelSettingStatus {
	return c.status
}

func (c modelSettingStatusOverride) SettingScope() string { return "当前会话" }

type claudeSessionSettingTarget struct {
	agent          agent.ClaudeSessionConfigAgent
	conversationID string
	config         agent.ClaudeSessionConfig
}

type claudeSessionSettingRef struct {
	conversationID string
	sessionID      string
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
	ctx        context.Context
	route      modelAgentRoute
	name       string
	agent      agent.Agent
	controller modelSettingController
}

// withCurrentSessionStatus 优先展示当前已绑定 Agent session/thread 的真实配置。
func (h *Handler) withCurrentSessionStatus(req modelSettingControllerRequest) modelSettingController {
	req.controller = h.withCurrentClaudeSessionStatus(req)
	return h.withCurrentCodexSessionStatus(req)
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
	binding, err := h.ensureClaudeSessions().requireWritableBinding(bindingKey)
	if err != nil {
		return renderClaudeBindingError(err), true
	}
	bindingSnapshot := claudeTaskBindingSnapshot{SessionID: binding.SessionID, Revision: binding.Revision}
	ref, bound := h.currentClaudeSessionSettingRef(req.route, req.name)
	if req.route.modelSettingCard && (!bound || req.route.modelSettingClaudeSessionID == "" || req.route.modelSettingClaudeSessionID != ref.sessionID) {
		return expiredModelSettingCardText(), true
	}
	unlockSession, lockErr := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: req.ctx, command: "model setting", sessionIDs: []string{binding.SessionID},
	})
	if lockErr != nil {
		return "当前 Claude session 正在切换或恢复，请稍后重试。", true
	}
	defer unlockSession()
	if err := h.ensureClaudeSessions().validateBindingSnapshot(bindingKey, bindingSnapshot); err != nil {
		return renderClaudeBindingError(err), true
	}
	taskKey := claudeSessionExecutionKey(binding.SessionID)
	task, taskCtx, started := h.beginActiveTask(req.ctx, taskKey, activeTaskMeta{
		owner: req.route.routeUserID, routeUserID: req.route.routeUserID,
		agentName: req.name, message: "更新 Claude session 配置",
	})
	if !started {
		return "当前 Claude session 正在执行任务，请在任务结束后再切换模型或推理强度。", true
	}
	defer h.finishActiveTask(taskKey, task)
	target, ok := h.currentClaudeSessionSettingTarget(req.route, req.name, req.agent)
	if !ok {
		return "当前窗口没有有效 Claude session，请先发送 /cc ls 选择或 /cc new 新建。", true
	}
	update := agent.ClaudeSessionConfigUpdate{
		ConversationID: target.conversationID, Model: req.model, Effort: req.effort,
	}
	if err := target.agent.SetClaudeSessionConfig(taskCtx, update); err != nil {
		return fmt.Sprintf("切换当前 Claude session 配置失败: %v", err), true
	}
	return renderCurrentClaudeSessionSetting(req.model, req.effort), true
}

// currentClaudeSessionSettingTarget 解析当前 route 的 ready 绑定与运行时配置。
func (h *Handler) currentClaudeSessionSettingTarget(route modelAgentRoute, name string, ag agent.Agent) (claudeSessionSettingTarget, bool) {
	configAgent, ok := ag.(agent.ClaudeSessionConfigAgent)
	if !ok {
		return claudeSessionSettingTarget{}, false
	}
	ref, ok := h.currentClaudeSessionSettingRef(route, name)
	if !ok {
		return claudeSessionSettingTarget{}, false
	}
	config, _ := configAgent.ClaudeSessionConfig(ref.conversationID)
	return claudeSessionSettingTarget{
		agent: configAgent, conversationID: ref.conversationID, config: config,
	}, true
}

func (h *Handler) currentClaudeSessionSettingRef(route modelAgentRoute, name string) (claudeSessionSettingRef, bool) {
	if strings.TrimSpace(route.routeUserID) == "" {
		return claudeSessionSettingRef{}, false
	}
	binding := h.ensureClaudeSessions().binding(claudeBindingKey(route.routeUserID, name))
	if binding.Status != claudeBindingReady || binding.SessionID == "" || binding.WorkspaceRoot == "" {
		return claudeSessionSettingRef{}, false
	}
	return claudeSessionSettingRef{
		conversationID: buildClaudeConversationID(route.routeUserID, name, binding.WorkspaceRoot),
		sessionID:      binding.SessionID,
	}, true
}

// renderCurrentClaudeSessionSetting 返回当前 session 已生效的明确反馈。
func renderCurrentClaudeSessionSetting(model string, effort string) string {
	if strings.TrimSpace(model) != "" {
		return wechatCommandText("已将当前 Claude session 模型切换为: "+model, "从下一轮任务开始生效。")
	}
	return wechatCommandText("已将当前 Claude session 推理强度切换为: "+effort, "从下一轮任务开始生效。")
}
