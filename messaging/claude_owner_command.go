package messaging

import (
	"errors"
	"strings"
)

var (
	errClaudeSessionUnbound        = errors.New("Claude 会话未绑定")
	errClaudeSessionBindingInvalid = errors.New("Claude 会话绑定状态不一致")
	errClaudeRuntimeUnavailable    = errors.New("Claude 运行通道暂不可用")
	errClaudeTaskBindingChanged    = errors.New("Claude 会话绑定状态已变化")
)

type claudeTaskBindingSnapshot struct {
	SessionID string
	Revision  uint64
}

// requireWritableBinding validates only this frontend's binding. Cross-window
// exclusivity is intentionally absent here; the session writer lease is
// acquired atomically with task admission.
func (s *claudeSessionStore) requireWritableBinding(bindingKey string) (claudeSessionBinding, error) {
	bindingKey = strings.TrimSpace(bindingKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.bindings[bindingKey]
	if strings.TrimSpace(binding.SessionID) == "" {
		return binding, errClaudeSessionUnbound
	}
	if binding.Status != claudeBindingReady && binding.Status != claudeBindingPendingResume && binding.Status != claudeBindingResumeFailed {
		return binding, errClaudeSessionBindingInvalid
	}
	workspaceRoot := normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot)
	if workspaceRoot == "" || workspaceRoot != binding.WorkspaceRoot || binding.Revision == 0 {
		return binding, errClaudeSessionBindingInvalid
	}
	if binding.Status == claudeBindingResumeFailed {
		return binding, errClaudeRuntimeUnavailable
	}
	return binding, nil
}

func (s *claudeSessionStore) validateBindingSnapshot(bindingKey string, snapshot claudeTaskBindingSnapshot) error {
	binding, err := s.requireWritableBinding(bindingKey)
	if err != nil {
		return err
	}
	if binding.SessionID != snapshot.SessionID || binding.Revision != snapshot.Revision {
		return errClaudeTaskBindingChanged
	}
	return nil
}

func claudeSingleHostEntryDisabled(command string) string {
	return wechatCommandText(
		"/cc "+command+" 已停用。",
		"Claude 现在由单一共享 ClaudeHost 执行；独立 CLI 会重新产生第二个 writer。",
		"请使用 /cc ls、/cc new 和 /cc switch 管理共享会话。",
	)
}

func renderClaudeBindingError(err error) string {
	switch {
	case errors.Is(err, errClaudeSessionUnbound):
		return "当前窗口没有有效的 Claude 会话，请发送 /cc ls 选择或 /cc new 新建。"
	case errors.Is(err, errClaudeRuntimeUnavailable):
		return "当前 Claude 会话已绑定，但共享 ClaudeHost 暂不可用；绑定不会被释放，请稍后重试。"
	case errors.Is(err, errClaudeTaskBindingChanged), errors.Is(err, errClaudeBindingSelectionChanged):
		return "当前窗口的 Claude 会话绑定刚刚发生变化，本次消息未写入，请重试。"
	case errors.Is(err, errClaudeSessionBindingInvalid):
		return "当前 Claude 会话绑定状态异常，请发送 /cc status 检查后重新选择。"
	default:
		return "当前 Claude 会话暂不可写入，请稍后重试。"
	}
}
