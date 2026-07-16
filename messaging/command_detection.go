package messaging

import "strings"

func isProgressCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && fields[0] == "/progress"
}

func isCodexSessionCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || !isCodexSessionCommandToken(fields[0]) {
		return false
	}
	if isCodexShortSelectionToken(fields[1]) {
		return len(fields) == 2
	}
	switch fields[1] {
	case "whoami", "ls", "new", "switch", "cd", "pwd", "model", "quota", "cli", "detach", "app", "status", "clean", "help", "owner", "page":
		return true
	case "attach":
		return len(fields) == 2
	default:
		return false
	}
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
