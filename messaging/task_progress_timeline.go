package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

const (
	defaultTaskProgressTimelineLimit = 0
	taskProgressTimelineItemMaxRunes = 180
)

type taskProgressUpdate struct {
	latest             string
	card               string
	timeline           bool
	explanation        bool
	commentary         bool
	currentExplanation string
	timelineItems      []agent.ProgressEvent
}

func taskProgressUpdateHasEffectiveProgress(update taskProgressUpdate) bool {
	if update.explanation || update.commentary || strings.TrimSpace(update.currentExplanation) != "" {
		return true
	}
	for _, event := range update.timelineItems {
		if strings.TrimSpace(event.DisplayText()) == "" {
			continue
		}
		switch event.Kind {
		case agent.ProgressKindCommentary, agent.ProgressKindPlan, agent.ProgressKindFile, agent.ProgressKindTool:
			return true
		}
	}
	return false
}

func appendTaskProgressTimeline(current []agent.ProgressEvent, incoming agent.ProgressEvent, limit int) []agent.ProgressEvent {
	next := append([]agent.ProgressEvent(nil), current...)
	if incoming.Kind == agent.ProgressKindMessage {
		return next
	}
	if index := matchingTaskProgressEntry(next, incoming); index >= 0 {
		next[index] = incoming
		return next
	}
	if incoming.Kind == agent.ProgressKindPlan {
		completePreviousRunningPlan(next)
	}
	next = append(next, incoming)
	if limit > 0 && len(next) > limit {
		next = append([]agent.ProgressEvent(nil), next[len(next)-limit:]...)
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
	if event.Kind == agent.ProgressKindMessage {
		return false
	}
	return event.Sequence > 0 || strings.TrimSpace(event.ID) != "" ||
		(event.Kind != "" && event.Kind != agent.ProgressKindStatus)
}

func renderTaskProgressCard(state taskViewState) (string, bool) {
	var card string
	var timeline bool
	if !state.progressTimelineEnabled || len(state.progressTimeline) == 0 {
		card = strings.TrimSpace(state.lastProgress)
	} else {
		card, timeline = renderTaskProgressTimeline(state.progressTimeline, state.lastProgress)
	}
	return appendTaskCurrentExplanation(card, state.currentExplanation), timeline
}

func renderTaskProgressTimeline(entries []agent.ProgressEvent, fallback string) (string, bool) {
	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, "**执行进度**")
	for _, event := range entries {
		display := taskProgressDisplay(event)
		if display == "" {
			continue
		}
		if event.Kind == agent.ProgressKindCommentary {
			lines = append(lines, "", display)
			continue
		}
		lines = append(lines, "- "+taskProgressMarker(event.State)+" "+display)
	}
	if len(lines) == 1 {
		return strings.TrimSpace(fallback), false
	}
	return strings.Join(lines, "\n"), true
}

func taskProgressDisplay(event agent.ProgressEvent) string {
	if event.Kind == agent.ProgressKindCommentary {
		return strings.TrimSpace(event.DisplayText())
	}
	return compactTaskProgressText(event.DisplayText())
}

func compactTaskProgressText(text string) string {
	display := strings.TrimSpace(text)
	display = strings.TrimSpace(strings.TrimPrefix(display, "进展："))
	runes := []rune(display)
	if len(runes) > taskProgressTimelineItemMaxRunes {
		return string(runes[:taskProgressTimelineItemMaxRunes]) + "…"
	}
	return display
}

func appendTaskCurrentExplanation(card string, explanation string) string {
	explanation = compactTaskProgressText(explanation)
	if explanation == "" {
		return strings.TrimSpace(card)
	}
	section := "**当前说明**\n" + explanation
	if card = strings.TrimSpace(card); card == "" {
		return section
	}
	return card + "\n\n" + section
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
