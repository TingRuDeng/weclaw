package messaging

import "strings"

func isProgressCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && fields[0] == "/progress"
}

func isCodexSessionCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && isCodexSessionCommandToken(fields[0])
}

func isCodexSessionCommandToken(token string) bool {
	return token == "/cx"
}

func isCodexShortSelectionToken(token string) bool {
	if token == ".." {
		return true
	}
	_, ok := parseCodexListIndex(token)
	return ok
}
