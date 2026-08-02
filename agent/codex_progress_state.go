package agent

import (
	"strconv"
	"strings"
)

type codexProgressState struct {
	emitted       bool
	currentKind   string
	currentAction string
	currentDetail string
	changedFiles  map[string]struct{}
}

func newCodexProgressState() *codexProgressState {
	return &codexProgressState{changedFiles: make(map[string]struct{})}
}

func (s *codexProgressState) hasEmitted() bool {
	return s != nil && s.emitted
}

func (s *codexProgressState) record(evt *codexTurnEvent) (string, bool) {
	event, ok := s.recordEvent(evt)
	if !ok {
		return "", false
	}
	return event.DisplayText(), true
}

func (s *codexProgressState) recordEvent(evt *codexTurnEvent) (ProgressEvent, bool) {
	if s == nil || evt == nil || evt.Kind != "progress" {
		return ProgressEvent{}, false
	}
	var (
		text string
		ok   bool
	)
	if evt.Progress != nil {
		text, ok = s.recordStructured(evt.Progress, evt.Text)
	} else {
		text, ok = s.recordText(evt.Text)
	}
	if !ok {
		return ProgressEvent{}, false
	}
	return codexStructuredProgressEvent(evt, text), true
}

func codexStructuredProgressEvent(evt *codexTurnEvent, text string) ProgressEvent {
	event := ProgressEvent{
		Kind:     ProgressKindStatus,
		State:    ProgressStateRunning,
		Sequence: evt.Sequence,
		Text:     text,
	}
	if evt.Progress != nil {
		event.ID = firstNonEmpty(evt.Progress.ID, evt.ItemID)
		event.Kind = codexProgressKind(evt.Progress.Kind)
		event.State = normalizeProgressState(evt.Progress.Status)
		if event.State == ProgressStateUnknown {
			event.State = ProgressStateRunning
		}
		event.Path = strings.TrimSpace(evt.Progress.FilePath)
	}
	display := strings.TrimSpace(strings.TrimPrefix(text, codexProgressPrefix))
	event.Summary, event.Detail = splitProgressDisplay(display)
	return event
}

func codexProgressKind(kind string) ProgressKind {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "command":
		return ProgressKindCommand
	case "file":
		return ProgressKindFile
	case "plan":
		return ProgressKindPlan
	case "approval":
		return ProgressKindApproval
	case "tool":
		return ProgressKindTool
	default:
		return ProgressKindStatus
	}
}

func splitProgressDisplay(text string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(text), " · ", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// recordText 兼容旧进度事件；统一补上进度前缀，避免卡片把它当成普通正文。
func (s *codexProgressState) recordText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	s.emitted = true
	if strings.HasPrefix(text, codexProgressPrefix) {
		return text, true
	}
	return codexProgressPrefix + trimRunes(text, codexRealtimeLineMaxRunes), true
}

func (s *codexProgressState) recordStructured(evt *codexProgressEvent, fallback string) (string, bool) {
	switch evt.Kind {
	case "command":
		return s.recordCommand(evt, fallback)
	case "file":
		return s.recordFile(evt, fallback)
	default:
		return s.recordText(firstNonEmpty(evt.Action, evt.Detail, fallback))
	}
}

// recordCommand 只保留归一化动作；命令参数和输出不属于用户进度。
func (s *codexProgressState) recordCommand(evt *codexProgressEvent, fallback string) (string, bool) {
	action := strings.TrimSpace(evt.Action)
	if action == "" {
		return "", false
	}
	s.currentKind = "command"
	s.currentAction = safeCodexCommandAction(action)
	s.currentDetail = ""
	return s.emitCurrent()
}

func safeCodexCommandAction(action string) string {
	switch strings.TrimSpace(action) {
	case "检查项目", "运行测试", "运行代码检查", "构建项目", "准备发布", "执行工具操作":
		return strings.TrimSpace(action)
	default:
		return "执行工具操作"
	}
}

// recordFile 记录本轮触达的文件集合，只展示聚合数量。
func (s *codexProgressState) recordFile(evt *codexProgressEvent, fallback string) (string, bool) {
	action := strings.TrimSpace(evt.Action)
	if action == "" {
		return "", false
	}
	s.currentKind = "file"
	s.currentAction = "修改代码"
	s.currentDetail = ""
	if path := strings.TrimSpace(evt.FilePath); path != "" {
		s.changedFiles[path] = struct{}{}
	}
	return s.emitCurrent()
}

// emitCurrent 输出单行结构化进度，适配飞书卡片只显示最新状态的渲染规则。
func (s *codexProgressState) emitCurrent() (string, bool) {
	action := strings.TrimSpace(s.currentAction)
	if action == "" {
		return "", false
	}
	detail := s.currentDetailText()
	line := action
	if detail != "" {
		line += " · " + detail
	}
	s.emitted = true
	return codexProgressPrefix + trimRunes(line, codexRealtimeLineMaxRunes), true
}

func (s *codexProgressState) currentDetailText() string {
	if s.currentKind == "file" && len(s.changedFiles) > 0 {
		return "已变更 " + strconv.Itoa(len(s.changedFiles)) + " 个文件"
	}
	return s.currentDetail
}

func (s *codexProgressState) emitGeneratingEvent() (ProgressEvent, bool) {
	if s.hasEmitted() {
		return ProgressEvent{}, false
	}
	s.emitted = true
	return ProgressEvent{
		Kind: ProgressKindGenerating, State: ProgressStateRunning,
		Summary: strings.TrimPrefix(codexGeneratingProgress, codexProgressPrefix), Text: codexGeneratingProgress,
	}, true
}
