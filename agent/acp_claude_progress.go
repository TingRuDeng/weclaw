package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	claudeProgressMaxRunes      = 240
	claudeThoughtBufferMaxRunes = 4096
	claudeProgressHistoryLimit  = 128
)

type claudeACPProgressState struct {
	toolTitles     map[string]string
	thoughtMessage string
	thoughtText    string
	lastText       string
	emitted        []string
	toolOrder      []string
}

func newClaudeACPProgressState() *claudeACPProgressState {
	return &claudeACPProgressState{toolTitles: make(map[string]string)}
}

// progressText 记录工具标题，并只输出允许展示的结构化字段。
func (s *claudeACPProgressState) progressText(update *sessionUpdate) (string, bool) {
	if update == nil {
		return "", false
	}
	if update.SessionUpdate == "agent_thought_chunk" {
		return s.thoughtProgressText(update)
	}
	if update.SessionUpdate == "tool_call" {
		s.rememberToolTitle(update)
	}
	if update.SessionUpdate == "tool_call_update" && strings.TrimSpace(update.Title) == "" {
		update = cloneUpdateWithTitle(update, s.toolTitles[update.ToolCallID])
	}
	return s.uniqueStructuredProgress(claudeACPProgressText(update))
}

func (s *claudeACPProgressState) thoughtProgressText(update *sessionUpdate) (string, bool) {
	chunk := extractChunkText(update)
	if chunk == "" {
		return "", false
	}
	messageID := strings.TrimSpace(update.MessageID)
	if messageID != "" && messageID != s.thoughtMessage {
		s.thoughtMessage = messageID
		s.thoughtText = ""
	}
	s.thoughtText = appendThoughtChunk(s.thoughtText, chunk)
	return s.uniqueProgressText(prefixedProgress("思考", s.thoughtText))
}

func appendThoughtChunk(existing string, chunk string) string {
	combined := []rune(existing + chunk)
	if len(combined) <= claudeThoughtBufferMaxRunes {
		return string(combined)
	}
	return string(combined[len(combined)-claudeThoughtBufferMaxRunes:])
}

func (s *claudeACPProgressState) uniqueProgressText(text string, ok bool) (string, bool) {
	if !ok || text == s.lastText {
		return "", false
	}
	s.lastText = text
	return text, true
}

func (s *claudeACPProgressState) uniqueStructuredProgress(text string, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	if containsProgressText(s.emitted, text) {
		return "", false
	}
	s.emitted = appendBoundedProgress(s.emitted, text)
	s.lastText = text
	return text, true
}

func containsProgressText(history []string, text string) bool {
	for _, item := range history {
		if item == text {
			return true
		}
	}
	return false
}

func appendBoundedProgress(history []string, text string) []string {
	if len(history) < claudeProgressHistoryLimit {
		return append(history, text)
	}
	copy(history, history[1:])
	history[len(history)-1] = text
	return history
}

func (s *claudeACPProgressState) rememberToolTitle(update *sessionUpdate) {
	toolID := strings.TrimSpace(update.ToolCallID)
	title := progressSummary(update.Title)
	if toolID != "" && title != "" {
		if _, exists := s.toolTitles[toolID]; !exists {
			s.rememberToolID(toolID)
		}
		s.toolTitles[toolID] = title
	}
}

func (s *claudeACPProgressState) rememberToolID(toolID string) {
	if len(s.toolOrder) < claudeProgressHistoryLimit {
		s.toolOrder = append(s.toolOrder, toolID)
		return
	}
	oldest := s.toolOrder[0]
	delete(s.toolTitles, oldest)
	copy(s.toolOrder, s.toolOrder[1:])
	s.toolOrder[len(s.toolOrder)-1] = toolID
}

func cloneUpdateWithTitle(update *sessionUpdate, title string) *sessionUpdate {
	cloned := *update
	cloned.Title = title
	return &cloned
}

// claudeACPProgressText 将标准 ACP 更新映射为飞书和微信可读的单行进度。
func claudeACPProgressText(update *sessionUpdate) (string, bool) {
	if update == nil {
		return "", false
	}
	switch update.SessionUpdate {
	case "agent_thought_chunk":
		return prefixedProgress("思考", extractChunkText(update))
	case "tool_call", "tool_call_update":
		return toolProgressText(update)
	case "plan":
		return planProgressText(update.Entries)
	default:
		return "", false
	}
}

func prefixedProgress(prefix string, value string) (string, bool) {
	text := progressSummary(value)
	if text == "" {
		return "", false
	}
	return prefix + "：" + text, true
}

func toolProgressText(update *sessionUpdate) (string, bool) {
	title := progressSummary(update.Title)
	if title == "" {
		return "", false
	}
	status := claudeProgressStatus(update.Status)
	if status == "" {
		return "工具：" + title, true
	}
	return fmt.Sprintf("工具：%s（%s）", title, status), true
}

func planProgressText(entries []acpPlanEntry) (string, bool) {
	entry, ok := currentPlanEntry(entries)
	if !ok {
		return "", false
	}
	content := progressSummary(entry.Content)
	if content == "" {
		return "", false
	}
	status := claudeProgressStatus(entry.Status)
	if status == "" {
		return "计划：" + content, true
	}
	return fmt.Sprintf("计划：%s（%s）", content, status), true
}

func currentPlanEntry(entries []acpPlanEntry) (acpPlanEntry, bool) {
	for _, entry := range entries {
		if entry.Status == "in_progress" && strings.TrimSpace(entry.Content) != "" {
			return entry, true
		}
	}
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Status == "completed" && strings.TrimSpace(entries[index].Content) != "" {
			return entries[index], true
		}
	}
	for _, entry := range entries {
		if entry.Status == "pending" && strings.TrimSpace(entry.Content) != "" {
			return entry, true
		}
	}
	return acpPlanEntry{}, false
}

func claudeProgressStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "pending":
		return "等待中"
	case "in_progress":
		return "进行中"
	case "completed":
		return "已完成"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	default:
		return ""
	}
}

func progressSummary(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.Join(strings.Fields(lines[index]), " "); line != "" {
			return truncateProgress(line)
		}
	}
	return ""
}

func truncateProgress(value string) string {
	if utf8.RuneCountInString(value) <= claudeProgressMaxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:claudeProgressMaxRunes-1]) + "…"
}
