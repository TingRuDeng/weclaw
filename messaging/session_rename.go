package messaging

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxSessionRenameRunes = 120

type sessionRenameCommandSpec struct {
	Target string
	Name   string
}

func parseSessionRenameCommand(trimmed string, namespace string) (sessionRenameCommandSpec, bool, error) {
	trimmed = strings.TrimSpace(trimmed)
	rest, ok := strings.CutPrefix(trimmed, namespace)
	if !ok || rest != "" && !isSpaceByte(rest[0]) {
		return sessionRenameCommandSpec{}, false, nil
	}
	command, rest := cutCommandWord(strings.TrimSpace(rest))
	if command != "rename" {
		return sessionRenameCommandSpec{}, false, nil
	}
	target, nameRest := cutCommandWord(strings.TrimSpace(rest))
	if target == "" || strings.TrimSpace(nameRest) == "" {
		return sessionRenameCommandSpec{}, true, fmt.Errorf("用法: %s rename current|<编号> <名称>", namespace)
	}
	if strings.ContainsAny(nameRest, "\r\n") {
		return sessionRenameCommandSpec{}, true, fmt.Errorf("会话名称只能是单行文本")
	}
	name := strings.TrimSpace(nameRest)
	if err := validateSessionRenameName(name); err != nil {
		return sessionRenameCommandSpec{}, true, err
	}
	return sessionRenameCommandSpec{Target: target, Name: name}, true, nil
}

func validateSessionRenameName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("会话名称不能为空")
	}
	if utf8.RuneCountInString(name) > maxSessionRenameRunes {
		return fmt.Errorf("会话名称不能超过 %d 个字符", maxSessionRenameRunes)
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			return fmt.Errorf("会话名称不能包含换行或控制字符")
		}
	}
	return nil
}

func redactedSessionIdentifier(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return fmt.Sprintf("sha256:%x", digest[:6])
}
