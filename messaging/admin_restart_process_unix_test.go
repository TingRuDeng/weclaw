//go:build !windows

package messaging

import "testing"

func TestBuildRestartCommandDetachesFromServiceProcessGroup(t *testing.T) {
	cmd := buildRestartCommand("/tmp/weclaw", []string{"--force"})

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("restart SysProcAttr=%#v, want detached session", cmd.SysProcAttr)
	}
}
