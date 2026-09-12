package agent

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestRequireNoCodexHostsRejectsResidualHostWithoutAgentConfig(t *testing.T) {
	for _, found := range []bool{false, true} {
		a := &ACPAgent{}
		a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
			if !found {
				return nil, nil
			}
			return []codexHostProcessSnapshot{{PID: 42, PGID: 42, UID: uint32(os.Geteuid()), Executable: "/test/codex", Command: "/test/codex app-server", Args: []string{"/test/codex", "app-server"}}}, nil
		}
		err := a.RequireNoCodexHosts(context.Background())
		if found && !errors.Is(err, ErrCodexHostConflict) {
			t.Fatalf("err=%v", err)
		}
		if !found && err != nil {
			t.Fatal(err)
		}
	}
}
