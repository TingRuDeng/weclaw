package agent

import (
	"regexp"
	"strings"
	"time"
)

var codexChineseProcessStart = regexp.MustCompile(`^([日一二三四五六])\s+([0-9]{1,2})月?/\s*([0-9]{1,2})\s+([0-9]{2}:[0-9]{2}:[0-9]{2})\s+([0-9]{4})$`)

// sameCodexProcessStart accepts the C and macOS Chinese representations of the
// same local start time. Old records have no timezone, so different wall times
// must still fail closed. Do not use this for metadata snapshot CAS: those
// comparisons must continue to detect any change to the original record.
func sameCodexProcessStart(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	if left == right {
		return true
	}
	l, lok := parseCodexProcessStart(left)
	r, rok := parseCodexProcessStart(right)
	return lok && rok && l.Equal(r)
}

func parseCodexProcessStart(value string) (time.Time, bool) {
	if parts := codexChineseProcessStart.FindStringSubmatch(strings.TrimSpace(value)); parts != nil {
		parsed, err := time.Parse("2006-1-2 15:04:05", parts[5]+"-"+parts[2]+"-"+parts[3]+" "+parts[4])
		if err != nil {
			return time.Time{}, false
		}
		weekdays := [...]string{"日", "一", "二", "三", "四", "五", "六"}
		return parsed, weekdays[parsed.Weekday()] == parts[1]
	}
	normalized := strings.Join(strings.Fields(value), " ")
	const layout = "Mon Jan 2 15:04:05 2006"
	parsed, err := time.Parse(layout, normalized)
	// time.Parse does not validate that the supplied weekday matches the date.
	return parsed, err == nil && parsed.Format(layout) == normalized
}
