package agent

import "strings"

func isMissingThreadError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	hasEntity := strings.Contains(msg, "thread") || strings.Contains(msg, "conversation") || strings.Contains(msg, "session")
	hasMissing := strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no conversation found") ||
		strings.Contains(msg, "unknown thread") ||
		strings.Contains(msg, "unknown session")
	return hasEntity && hasMissing
}
