package agent

import (
	"strconv"
	"strings"
)

const codexProgressDetailMaxRunes = 160

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
	if s == nil || evt == nil || evt.Kind != "progress" {
		return "", false
	}
	if evt.Progress != nil {
		return s.recordStructured(evt.Progress, evt.Text)
	}
	return s.recordText(evt.Text)
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

// recordCommand 保留当前运行命令，把后续输出行合并为同一条动作详情。
func (s *codexProgressState) recordCommand(evt *codexProgressEvent, fallback string) (string, bool) {
	action := firstNonEmpty(evt.Action, fallback)
	if strings.HasPrefix(action, codexProgressPrefix) {
		return s.recordText(action)
	}
	if strings.HasPrefix(action, "运行 ") {
		s.currentKind = "command"
		s.currentAction = action
	}
	if detail := cleanCodexProgressDetail(evt.Detail); detail != "" {
		s.currentDetail = detail
	}
	if s.currentKind != "command" || s.currentAction == "" {
		return s.recordText(firstNonEmpty(evt.Detail, fallback))
	}
	return s.emitCurrent()
}

// recordFile 记录本轮触达的文件集合，用最新文件作为主动作，并展示变更计数。
func (s *codexProgressState) recordFile(evt *codexProgressEvent, fallback string) (string, bool) {
	action := firstNonEmpty(evt.Action, fallback)
	if action == "" {
		return "", false
	}
	s.currentKind = "file"
	s.currentAction = action
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
	if s.currentKind == "file" && len(s.changedFiles) > 1 {
		return "已变更 " + strconv.Itoa(len(s.changedFiles)) + " 个文件"
	}
	return s.currentDetail
}

func cleanCodexProgressDetail(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "运行 ") {
		return ""
	}
	return trimRunes(text, codexProgressDetailMaxRunes)
}

func (s *codexProgressState) emitGenerating() (string, bool) {
	if s.hasEmitted() {
		return "", false
	}
	s.emitted = true
	return codexGeneratingProgress, true
}
