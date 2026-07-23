package agent

import (
	"bufio"
	"strings"
	"testing"
)

func TestStaleACPReadEpochCannotDispatchAfterReconnect(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"}})
	oldScanner := bufio.NewScanner(strings.NewReader(""))
	currentScanner := bufio.NewScanner(strings.NewReader(""))
	responseCh := a.pending.register(7)
	a.mu.Lock()
	a.scanner = currentScanner
	a.wireEpoch = 2
	a.mu.Unlock()

	if a.handleCurrentACPWireLine(oldScanner, 1, `{"id":7,"result":{"source":"old"}}`) {
		t.Fatal("旧连接 generation 不应继续分发")
	}
	select {
	case response := <-responseCh:
		t.Fatalf("旧连接响应进入了新 generation: %#v", response)
	default:
	}

	if !a.handleCurrentACPWireLine(currentScanner, 2, `{"id":7,"result":{"source":"new"}}`) {
		t.Fatal("当前连接 generation 应允许分发")
	}
	select {
	case <-responseCh:
	default:
		t.Fatal("当前连接响应未分发")
	}
}
