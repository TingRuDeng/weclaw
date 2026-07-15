package messaging

import (
	"errors"
	"fmt"
	"strings"
)

var (
	errClaudeSessionUnbound        = errors.New("Claude 会话未绑定")
	errClaudeSessionNotRemoteOwner = errors.New("当前窗口没有 Claude 远程控制权")
	errClaudeSessionControlInvalid = errors.New("Claude 会话控制状态不一致")
	errClaudeTaskControlChanged    = errors.New("Claude 会话控制状态已变化")
)

type claudeTaskControlSnapshot struct {
	SessionID string
	Revision  uint64
}

// requireRemoteControl 原子读取 route 绑定及其控制意图，只允许当前 route 的 remote owner 写入。
func (s *claudeSessionStore) requireRemoteControl(bindingKey string) (claudeSessionBinding, claudeControlIntent, error) {
	bindingKey = strings.TrimSpace(bindingKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.bindings[bindingKey]
	if strings.TrimSpace(binding.SessionID) == "" {
		return binding, claudeControlIntent{}, errClaudeSessionUnbound
	}
	if binding.Status != claudeBindingReady && binding.Status != claudeBindingPendingResume {
		return binding, claudeControlIntent{}, errClaudeSessionControlInvalid
	}
	if workspaceRoot := normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot); workspaceRoot == "" || workspaceRoot != binding.WorkspaceRoot {
		return binding, claudeControlIntent{}, errClaudeSessionControlInvalid
	}
	rawIntent, exists := s.controls[binding.SessionID]
	if !exists {
		return binding, claudeControlIntent{}, errClaudeSessionControlInvalid
	}
	intent := normalizeClaudeControlIntent(rawIntent)
	if rawIntent.Owner == claudeOwnerRemote && intent.Owner != claudeOwnerRemote {
		return binding, intent, errClaudeSessionControlInvalid
	}
	if intent.Owner == claudeOwnerRemote && intent.BindingKey == bindingKey {
		expectedConversationID := claudeConversationIDForBinding(bindingKey, binding.WorkspaceRoot)
		if expectedConversationID == "" || rawIntent.BindingKey != bindingKey || rawIntent.ConversationID != expectedConversationID {
			return binding, intent, errClaudeSessionControlInvalid
		}
		return binding, intent, nil
	}
	if intent.Owner == claudeOwnerRemote {
		return binding, intent, errClaudeRemoteSelectionOtherRoute
	}
	return binding, intent, errClaudeSessionNotRemoteOwner
}

func (s *claudeSessionStore) validateRemoteControlSnapshot(bindingKey string, snapshot claudeTaskControlSnapshot) error {
	binding, intent, err := s.requireRemoteControl(bindingKey)
	if err != nil {
		return err
	}
	if binding.SessionID != snapshot.SessionID || intent.Revision != snapshot.Revision {
		return errClaudeTaskControlChanged
	}
	return nil
}

func (h *Handler) handleClaudeOwnerCommand(route claudeSessionRoute, args []string) string {
	if len(args) == 0 {
		return h.renderClaudeOwnerStatus(route)
	}
	if len(args) != 1 {
		return "用法: /cc owner [remote|local]"
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "local":
		return h.releaseClaudeOwnerToLocal(route)
	case "remote":
		return h.reacquireClaudeOwner(route)
	default:
		return "用法: /cc owner [remote|local]"
	}
}

func (h *Handler) releaseClaudeOwnerToLocal(route claudeSessionRoute) string {
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(route.BindingKey))
	defer unlock()
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	if strings.TrimSpace(binding.SessionID) == "" {
		return "当前窗口没有有效的 Claude 会话，请先发送 /cc ls 选择或 /cc new 新建。"
	}
	if h.hasActiveClaudeTask(route, binding.WorkspaceRoot) {
		return "当前 Claude 任务正在运行或已有暂存消息，请等待任务结束或先发送 /stop。"
	}
	intent := h.ensureClaudeSessions().controlIntent(binding.SessionID)
	if intent.Owner == claudeOwnerLocal {
		return "Claude 远程控制已释放；结束本地 Claude CLI 后，可发送 /cc owner remote 重新接管。"
	}
	if intent.Owner == claudeOwnerRemote && intent.BindingKey != route.BindingKey {
		return "该 Claude 会话正由其他远程窗口控制，当前窗口不能释放。"
	}
	if intent.Owner != claudeOwnerRemote || intent.BindingKey != route.BindingKey {
		return "当前窗口没有 Claude 远程控制权；确认本地 CLI 已结束后，可发送 /cc owner remote 重新接管。"
	}
	if _, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route: route, WorkspaceRoot: binding.WorkspaceRoot, KeepSelection: true, Command: "owner local",
	}); err != nil {
		return renderClaudeOwnerMutationFailure(err)
	}
	return "已释放 Claude 远程控制；普通消息将被拒绝。结束本地 Claude CLI 后，可发送 /cc owner remote 重新接管。"
}

func (h *Handler) reacquireClaudeOwner(route claudeSessionRoute) string {
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(route.BindingKey))
	defer unlock()
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	if strings.TrimSpace(binding.SessionID) == "" {
		return "当前窗口没有有效的 Claude 会话，请先发送 /cc ls 选择或 /cc new 新建。"
	}
	if h.hasActiveClaudeTask(route, binding.WorkspaceRoot) {
		return "当前 Claude 任务正在运行或已有暂存消息，请等待任务结束或先发送 /stop。"
	}
	selected, err := h.findClaudeSessionForRoute(route, binding.SessionID)
	if err != nil {
		return err.Error()
	}
	if _, err := h.acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest{
		Route: route, Selected: selected, Command: "owner remote",
	}); err != nil {
		return renderClaudeSessionAcquireFailure(err)
	}
	return wechatCommandText(
		"已接管 Claude 会话。",
		"session: "+selected.ID,
		"控制方: 当前远程窗口",
		"重新接管前请确认本地 Claude CLI 已结束。",
	)
}

func (h *Handler) renderClaudeOwnerStatus(route claudeSessionRoute) string {
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	intent := h.ensureClaudeSessions().controlIntent(binding.SessionID)
	active := "无"
	conversationID := strings.TrimSpace(intent.ConversationID)
	if conversationID == "" && strings.TrimSpace(binding.WorkspaceRoot) != "" {
		conversationID = buildClaudeConversationID(route.UserID, route.AgentName, binding.WorkspaceRoot)
	}
	if conversationID != "" {
		if task, ok := h.activeTask(conversationID); ok {
			active = "运行中"
			if task.pendingGuide() != "" {
				active = "运行中（有暂存消息）"
			}
		}
	}
	lines := []string{
		"Claude 控制状态:",
		"session: " + renderClaudeBindingSession(binding),
		"控制方: " + renderClaudeControlOwner(intent, route.BindingKey),
		"恢复状态: " + renderClaudeBindingStatus(binding.Status),
		"活动任务: " + active,
	}
	if intent.Owner == claudeOwnerLocal {
		lines = append(lines, "提示: 结束本地 Claude CLI 后，再发送 /cc owner remote 重新接管。")
	}
	return wechatCommandText(lines...)
}

func renderClaudeControlOwner(intent claudeControlIntent, bindingKey string) string {
	intent = normalizeClaudeControlIntent(intent)
	switch {
	case intent.Owner == claudeOwnerRemote && intent.BindingKey == strings.TrimSpace(bindingKey):
		return "当前远程窗口"
	case intent.Owner == claudeOwnerRemote:
		return "其他远程窗口"
	case intent.Owner == claudeOwnerLocal:
		return "本地 Claude CLI"
	default:
		return "未认领"
	}
}

func renderClaudeRemoteControlError(err error) string {
	switch {
	case errors.Is(err, errClaudeSessionUnbound):
		return "当前窗口没有有效的 Claude 会话，请发送 /cc ls 选择或 /cc new 新建。"
	case errors.Is(err, errClaudeRemoteSelectionOtherRoute):
		return "该 Claude 会话正由其他远程窗口控制，请先在原窗口释放控制权。"
	case errors.Is(err, errClaudeSessionNotRemoteOwner):
		return "当前窗口没有 Claude 远程控制权。请先结束本地 Claude CLI，再发送 /cc owner remote 重新接管。"
	case errors.Is(err, errClaudeSessionControlInvalid):
		return "Claude 会话控制状态不一致，请发送 /cc ls 重新选择或 /cc new 新建。"
	case errors.Is(err, errClaudeTaskControlChanged), errors.Is(err, errClaudeRemoteSelectionChanged):
		return "Claude 会话状态刚刚发生变化，请重新确认控制权后重试。"
	default:
		return fmt.Sprintf("检查 Claude 远程控制权失败: %v", err)
	}
}

func renderClaudeOwnerMutationFailure(err error) string {
	switch {
	case errors.Is(err, errClaudeSessionReleaseActive):
		return "当前 Claude 任务正在运行或已有暂存消息，请等待任务结束或先发送 /stop。"
	case errors.Is(err, errClaudeRemoteSelectionOtherRoute):
		return "该 Claude 会话正由其他远程窗口控制，当前窗口不能释放。"
	case errors.Is(err, errClaudeRemoteSelectionChanged):
		return "Claude 会话状态刚刚发生变化，请重试。"
	default:
		return "释放 Claude 远程控制失败，请稍后重试。"
	}
}
