package agent

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates package tests from the operator's real account store.
// Shared-host safety checks intentionally fail closed on unsafe switch records;
// unit tests must never inherit that mutable machine state.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "weclaw-agent-tests-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated WECLAW_HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("WECLAW_HOME", home); err != nil {
		_ = os.RemoveAll(home)
		_, _ = fmt.Fprintf(os.Stderr, "set isolated WECLAW_HOME: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
