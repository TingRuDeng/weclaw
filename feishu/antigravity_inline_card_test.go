package feishu

import (
	"strings"
	"testing"
)

func TestAntigravityCommandsAreInlineCardCommands(t *testing.T) {
	for _, command := range []string{
		"/ag", "/ag ls", "/ag project p1", "/ag page 2", "/ag switch conv-a", "/ag 3",
		"/ag status", "/ag release", "/ag help", "/ag new hello", "/ag new-select @ag_x p1",
	} {
		if !isInlineCardCommand(command) {
			t.Fatalf("%q must be handled inline", command)
		}
	}
	for _, command := range []string{"/ag unknown", "/agx ls"} {
		if isInlineCardCommand(command) {
			t.Fatalf("%q must not be handled inline", command)
		}
	}
}

func TestAntigravityDeferredCardCommands(t *testing.T) {
	for _, command := range []string{"/ag switch conv-a", "/ag 3", "/ag new hello", "/ag new-select @ag_x p1"} {
		if !isDeferredCardResultCommand(command) {
			t.Fatalf("%q must use the deferred card path", command)
		}
	}
	for _, command := range []string{"/ag ls", "/ag project p1", "/ag status", "/ag release", "/ag help"} {
		if isDeferredCardResultCommand(command) {
			t.Fatalf("%q must not use the deferred card path", command)
		}
	}
}

func TestAntigravityCardTitlesAndColors(t *testing.T) {
	if title := deferredCardResultTitle("/ag switch conv-a"); !strings.Contains(title, "Antigravity") {
		t.Fatalf("switch title = %q", title)
	}
	if title := deferredCardResultTitle("/ag new-select @ag_x p1"); !strings.Contains(title, "新建") {
		t.Fatalf("new title = %q", title)
	}
	if timeout := deferredCardTimeoutText("/ag switch conv-a"); !strings.Contains(timeout, "/ag status") {
		t.Fatalf("timeout text = %q", timeout)
	}
	if color := choiceCommandResultTemplate("/ag switch conv-a", "已切换并绑定 Antigravity 会话"); color != "green" {
		t.Fatalf("success color = %q", color)
	}
	if color := choiceCommandResultTemplate("/ag switch conv-a", "切换失败，保留原绑定"); color != "red" {
		t.Fatalf("failure color = %q", color)
	}
}
