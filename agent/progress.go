package agent

import (
	"context"
	"strings"
)

// ProgressKind 标识 Agent 进展的稳定语义类型。
type ProgressKind string

const (
	ProgressKindStatus     ProgressKind = "status"
	ProgressKindThought    ProgressKind = "thought"
	ProgressKindTool       ProgressKind = "tool"
	ProgressKindCommand    ProgressKind = "command"
	ProgressKindFile       ProgressKind = "file"
	ProgressKindPlan       ProgressKind = "plan"
	ProgressKindApproval   ProgressKind = "approval"
	ProgressKindUserInput  ProgressKind = "user_input"
	ProgressKindGenerating ProgressKind = "generating"
)

// ProgressState 标识单条进展事件的生命周期状态。
type ProgressState string

const (
	ProgressStateUnknown   ProgressState = "unknown"
	ProgressStatePending   ProgressState = "pending"
	ProgressStateRunning   ProgressState = "running"
	ProgressStateCompleted ProgressState = "completed"
	ProgressStateFailed    ProgressState = "failed"
)

// ProgressEvent 是 Agent 到消息层之间的结构化进展契约。
// Text 只保存已允许展示的兼容文案；原始工具输出和凭据不得放入该字段。
type ProgressEvent struct {
	ID       string
	Kind     ProgressKind
	State    ProgressState
	Summary  string
	Detail   string
	Path     string
	Sequence uint64
	Text     string
}

// DisplayText 返回平台和旧字符串回调可安全展示的单行文本。
func (e ProgressEvent) DisplayText() string {
	if text := strings.TrimSpace(e.Text); text != "" {
		return text
	}
	summary := strings.TrimSpace(e.Summary)
	detail := strings.TrimSpace(e.Detail)
	if summary == "" {
		return detail
	}
	if detail == "" || detail == summary {
		return summary
	}
	return summary + " · " + detail
}

// TextProgressEvent 把旧字符串进展包装为结构化兼容事件。
func TextProgressEvent(text string) ProgressEvent {
	text = strings.TrimSpace(text)
	return ProgressEvent{
		Kind:    ProgressKindStatus,
		State:   ProgressStateRunning,
		Summary: strings.TrimSpace(strings.TrimPrefix(text, "进展：")),
		Text:    text,
	}
}

// StructuredProgressAgent 是可选能力；调用方应在缺失时回退到旧字符串进展接口。
type StructuredProgressAgent interface {
	ChatWithProgressEvents(ctx context.Context, conversationID string, message string, onProgress func(ProgressEvent)) (string, error)
}

type progressCallbacks struct {
	onText  func(string)
	onEvent func(ProgressEvent)
}

func (c progressCallbacks) emit(event ProgressEvent) {
	if c.onEvent != nil {
		c.onEvent(event)
		return
	}
	if c.onText != nil {
		if text := event.DisplayText(); text != "" {
			c.onText(text)
		}
	}
}

func (c progressCallbacks) enabled() bool {
	return c.onEvent != nil || c.onText != nil
}

func normalizeProgressState(value string) ProgressState {
	normalized := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "pending", "queued", "todo":
		return ProgressStatePending
	case "inprogress", "running", "started", "streaming":
		return ProgressStateRunning
	case "completed", "complete", "done", "success", "succeeded":
		return ProgressStateCompleted
	case "failed", "failure", "error", "cancelled", "canceled":
		return ProgressStateFailed
	default:
		return ProgressStateUnknown
	}
}
