package messaging

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/weclaw/config"
)

func progressModeAllowsProgress(mode string) bool {
	switch mode {
	case progressModeSummary, progressModeVerbose, progressModeStream, progressModeDebug:
		return true
	default:
		return false
	}
}

func progressTaskTitle(text string, maxRunes int) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "…"
}

func shouldSendProgress(now time.Time, state progressSendState, summary string, cfg config.ProgressConfig) bool {
	if strings.TrimSpace(summary) == "" {
		return false
	}
	// stream 始终原地更新同一张卡片，不会制造额外消息，不能套用消息数量上限。
	if cfg.Mode != progressModeStream && cfg.MaxProgressMessages > 0 && state.sentCount >= cfg.MaxProgressMessages {
		return false
	}
	if summary != state.lastSentSummary {
		return true
	}
	interval := durationSeconds(cfg.SummaryIntervalSeconds, 0)
	return interval <= 0 || now.Sub(state.lastSentAt) >= interval
}

func progressTickerInterval(cfg config.ProgressConfig) time.Duration {
	if cfg.SummaryIntervalSeconds <= 0 {
		return 10 * time.Millisecond
	}
	return time.Duration(cfg.SummaryIntervalSeconds) * time.Second
}

func durationSeconds(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func boolValue(v *bool) bool {
	return v != nil && *v
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncateTailRunes(text string, limit int) string {
	if limit <= 0 || text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[len(runes)-limit:])
}
