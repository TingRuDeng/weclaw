package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexSessionSettingRef struct {
	conversationID string
	threadID       string
}

type codexModelSettingRequest struct {
	ctx    context.Context
	route  modelAgentRoute
	name   string
	agent  agent.Agent
	model  string
	effort string
}

// withCurrentCodexSessionStatus 用当前 thread 配置覆盖新 thread 默认值。
func (h *Handler) withCurrentCodexSessionStatus(req modelSettingControllerRequest) modelSettingController {
	if !isCodexAgent(req.name, req.agent.Info()) {
		return req.controller
	}
	ref, ok := h.currentCodexSessionSettingRef(req.route, req.name)
	if !ok {
		return req.controller
	}
	configAgent, ok := req.agent.(agent.CodexThreadConfigAgent)
	if !ok {
		return h.codexSessionStatus(req.controller, ref.threadID, agent.CodexThreadConfig{})
	}
	config, err := configAgent.CodexThreadConfig(req.ctx, ref.conversationID, ref.threadID)
	if err != nil {
		return h.codexSessionStatus(req.controller, ref.threadID, agent.CodexThreadConfig{})
	}
	return h.codexSessionStatus(req.controller, ref.threadID, config)
}

func (h *Handler) codexSessionStatus(control modelSettingController, threadID string, config agent.CodexThreadConfig) modelSettingController {
	status := modelSettingStatus{}
	if strings.TrimSpace(config.Model) == "" || strings.TrimSpace(config.Effort) == "" {
		rollout := h.codexSessionModelStatus(threadID)
		if strings.TrimSpace(rollout.Model) != "" {
			status.Model = rollout.Model
		}
		if strings.TrimSpace(rollout.Effort) != "" {
			status.Effort = rollout.Effort
		}
	}
	if strings.TrimSpace(config.Model) != "" {
		status.Model = config.Model
	}
	if strings.TrimSpace(config.Effort) != "" {
		status.Effort = config.Effort
	}
	return modelSettingStatusOverride{
		modelSettingController: control,
		status:                 status,
	}
}

// setCurrentCodexSessionSetting 只更新当前绑定 thread；无绑定时由调用方修改新 thread 默认值。
func (h *Handler) setCurrentCodexSessionSetting(req codexModelSettingRequest) (string, bool) {
	if strings.TrimSpace(req.route.routeUserID) == "" || !isCodexAgent(req.name, req.agent.Info()) {
		return "", false
	}
	bindingKey := codexBindingKey(req.route.routeUserID, req.name)
	unlockBinding, err := h.lockCodexSessionBinding(req.ctx, bindingKey, "model-setting")
	if err != nil {
		return "前一项 Codex 会话操作仍在处理，本次设置未执行。", true
	}
	defer unlockBinding()
	ref, ok := h.currentCodexSessionSettingRef(req.route, req.name)
	if !ok {
		if req.route.modelSettingCard && req.route.modelSettingCodexThreadID != "" {
			return expiredModelSettingCardText(), true
		}
		return "", false
	}
	if req.route.modelSettingCard && (req.route.modelSettingCodexThreadID == "" || req.route.modelSettingCodexThreadID != ref.threadID) {
		return expiredModelSettingCardText(), true
	}
	configAgent, ok := req.agent.(agent.CodexThreadConfigAgent)
	if !ok {
		return "当前 Codex runtime 不支持修改当前会话配置。", true
	}
	unlockThread, err := h.lockCodexSessionThread(req.ctx, ref.threadID, "model-setting")
	if err != nil {
		return "当前 Codex 会话正在处理其他控制操作，本次设置未执行。", true
	}
	defer unlockThread()
	update := agent.CodexThreadConfigUpdate{
		ConversationID: ref.conversationID, ThreadID: ref.threadID,
		Model: req.model, Effort: req.effort,
	}
	if err := configAgent.SetCodexThreadConfig(req.ctx, update); err != nil {
		return fmt.Sprintf("切换当前 Codex 会话配置失败: %v", err), true
	}
	return renderCurrentCodexSessionSetting(req.model, req.effort), true
}

func (h *Handler) currentCodexSessionSettingRef(route modelAgentRoute, name string) (codexSessionSettingRef, bool) {
	if strings.TrimSpace(route.routeUserID) == "" {
		return codexSessionSettingRef{}, false
	}
	bindingKey := codexBindingKey(route.routeUserID, name)
	workspaceRoot, ok := h.ensureCodexSessions().getActiveWorkspace(bindingKey)
	if !ok {
		return codexSessionSettingRef{}, false
	}
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	threadID = strings.TrimSpace(threadID)
	if pending || threadID == "" {
		return codexSessionSettingRef{}, false
	}
	return codexSessionSettingRef{
		conversationID: buildCodexConversationID(route.routeUserID, name, workspaceRoot),
		threadID:       threadID,
	}, true
}

func renderCurrentCodexSessionSetting(model string, effort string) string {
	if strings.TrimSpace(model) != "" {
		return wechatCommandText("已将当前 Codex 会话模型切换为: "+model, "从下一轮任务开始生效。")
	}
	return wechatCommandText("已将当前 Codex 会话推理强度切换为: "+effort, "从下一轮任务开始生效。")
}
