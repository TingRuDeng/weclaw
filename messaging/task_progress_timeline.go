package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

const (
	taskProgressTimelineLimit        = 8
	taskProgressTimelineItemMaxRunes = 180
)

type taskProgressUpdate struct {
	latest   string
	card     string
	timeline bool
}

func appendTaskProgressTimeline(current []agent.ProgressEvent, incoming agent.ProgressEvent) []agent.ProgressEvent {
	next := append([]agent.ProgressEvent(nil), current...)
	if index := matchingTaskProgressEntry(next, incoming); index >= 0 {
		next[index] = incoming
		return next
	}
	if incoming.Kind == agent.ProgressKindPlan {
		completePreviousRunningPlan(next)
	}
	next = append(next, incoming)
	if len(next) > taskProgressTimelineLimit {
		next = append([]agent.ProgressEvent(nil), next[len(next)-taskProgressTimelineLimit:]...)
	}
	return next
}

func matchingTaskProgressEntry(entries []agent.ProgressEvent, incoming agent.ProgressEvent) int {
	incomingID := strings.TrimSpace(incoming.ID)
	incomingDisplay := strings.TrimSpace(incoming.DisplayText())
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if incomingID != "" && strings.TrimSpace(entry.ID) == incomingID && entry.Kind == incoming.Kind {
			if incoming.Kind != agent.ProgressKindPlan || strings.TrimSpace(entry.DisplayText()) == incomingDisplay {
				return index
			}
		}
		if incomingID == "" && strings.TrimSpace(entry.ID) == "" && entry.Kind == incoming.Kind &&
			strings.TrimSpace(entry.DisplayText()) == incomingDisplay {
			return index
		}
	}
	return -1
}

func completePreviousRunningPlan(entries []agent.ProgressEvent) {
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Kind != agent.ProgressKindPlan {
			continue
		}
		if entries[index].State == agent.ProgressStateRunning || entries[index].State == agent.ProgressStatePending ||
			entries[index].State == agent.ProgressStateUnknown {
			entries[index].State = agent.ProgressStateCompleted
		}
		return
	}
}

func isStructuredTaskProgress(event agent.ProgressEvent) bool {
	return event.Sequence > 0 || strings.TrimSpace(event.ID) != "" ||
		(event.Kind != "" && event.Kind != agent.ProgressKindStatus)
}

func renderTaskProgressCard(state taskViewState) (string, bool) {
	if !state.progressTimelineEnabled || len(state.progressTimeline) == 0 {
		return strings.TrimSpace(state.lastProgress), false
	}
	lines := make([]string, 0, len(state.progressTimeline)+1)
	lines = append(lines, "**执行进度**")
	for _, event := range state.progressTimeline {
		display := compactTaskProgressDisplay(event)
		if display == "" {
			continue
		}
		lines = append(lines, "- "+taskProgressMarker(event.State)+" "+display)
	}
	if len(lines) == 1 {
		return strings.TrimSpace(state.lastProgress), false
	}
	return strings.Join(lines, "\n"), true
}

func compactTaskProgressDisplay(event agent.ProgressEvent) string {
	display := strings.TrimSpace(event.DisplayText())
	display = strings.TrimSpace(strings.TrimPrefix(display, "进展："))
	runes := []rune(display)
	if len(runes) > taskProgressTimelineItemMaxRunes {
		return string(runes[:taskProgressTimelineItemMaxRunes]) + "…"
	}
	return display
}

func taskProgressMarker(state agent.ProgressState) string {
	switch state {
	case agent.ProgressStateCompleted:
		return "✅"
	case agent.ProgressStateFailed:
		return "❌"
	case agent.ProgressStatePending:
		return "○"
	default:
		return "•"
	}
}

func combineTaskProgressTimeline(timeline string, content string) string {
	timeline = strings.TrimSpace(timeline)
	content = strings.TrimSpace(content)
	if timeline == "" {
		return content
	}
	if content == "" {
		return timeline
	}
	return timeline + "\n\n---\n\n**处理结果**\n\n" + content
}
