package codexauth

import (
	"path/filepath"
	"testing"
)

func TestResolveCodexHomePrecedenceAndRunAsSafety(t *testing.T) {
	t.Setenv("CODEX_HOME", "/process/codex")
	home, err := ResolveCodexHome(map[string]string{"CODEX_HOME": "/agent/codex"}, "")
	if err != nil || home != filepath.Clean("/agent/codex") {
		t.Fatalf("ResolveCodexHome()=%q error=%v", home, err)
	}

	t.Setenv("CODEX_HOME", "")
	if _, err := ResolveCodexHome(nil, "sandbox-user"); ErrorCode(err) != CodeInvalid {
		t.Fatalf("ResolveCodexHome(run_as_user) error=%v", err)
	}
}

func TestHostIDSeparatesHomeAndSocket(t *testing.T) {
	first, err := HostID("/tmp/codex-a", "/tmp/app.sock")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := HostID("/tmp/codex-b", "/tmp/app.sock")
	third, _ := HostID("/tmp/codex-a", "/tmp/other.sock")
	if first == second || first == third || len(first) != 20 {
		t.Fatalf("host ids not isolated: %q %q %q", first, second, third)
	}
}
