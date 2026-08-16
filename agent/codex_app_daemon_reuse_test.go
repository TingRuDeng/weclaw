package agent

import (
	"context"
	"errors"
	"testing"
)

func TestEnsureCodexAppReusesDaemonRejectsPrivateAppServer(t *testing.T) {
	enabled := true
	a := &ACPAgent{
		codexHostMode: codexHostModeDaemon, codexAppReuseDaemon: &enabled,
		codexAppDaemonReuseCall: func(context.Context, bool, string) (codexAppDaemonReuseResult, error) {
			return codexAppDaemonReuseResult{AppRunning: true, PrivateAppServer: true}, nil
		},
	}
	err := a.ensureCodexAppReusesDaemon(context.Background(), "/tmp/codex.sock")
	if !errors.Is(err, ErrCodexAppRestartRequired) {
		t.Fatalf("ensureCodexAppReusesDaemon() error=%v, want restart required", err)
	}
}

func TestEnsureCodexAppReusesDaemonIsScopedToOfficialDaemon(t *testing.T) {
	enabled := true
	called := false
	a := &ACPAgent{
		codexHostMode: codexHostModeManaged, codexAppReuseDaemon: &enabled,
		codexAppDaemonReuseCall: func(context.Context, bool, string) (codexAppDaemonReuseResult, error) {
			called = true
			return codexAppDaemonReuseResult{}, nil
		},
	}
	if err := a.ensureCodexAppReusesDaemon(context.Background(), "/tmp/codex.sock"); err != nil || called {
		t.Fatalf("error=%v called=%v, managed Host must not configure App daemon reuse", err, called)
	}
}

func TestApplyCodexAppDaemonReusePreferenceUnsetsExplicitOptOut(t *testing.T) {
	disabled := false
	var gotEnabled bool
	a := &ACPAgent{
		protocol: protocolCodexAppServer, codexAppReuseDaemon: &disabled,
		codexAppDaemonReuseCall: func(_ context.Context, enabled bool, socketPath string) (codexAppDaemonReuseResult, error) {
			gotEnabled = enabled
			if socketPath != "" {
				t.Fatalf("disable socketPath=%q, want empty", socketPath)
			}
			return codexAppDaemonReuseResult{Changed: true, AppRunning: true}, nil
		},
	}
	if err := a.applyCodexAppDaemonReusePreference(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotEnabled {
		t.Fatal("explicit opt-out must call the controller with enabled=false")
	}
}

func TestValidateRunningCodexAppDaemonReuseRejectsLatePrivateHost(t *testing.T) {
	enabled := true
	a := &ACPAgent{
		codexHostMode: codexHostModeDaemon, codexAppReuseDaemon: &enabled,
		codexAppDaemonInspectCall: func(context.Context) (codexAppDaemonReuseResult, error) {
			return codexAppDaemonReuseResult{AppRunning: true, PrivateAppServer: true}, nil
		},
	}
	if err := a.validateRunningCodexAppDaemonReuse(context.Background()); !errors.Is(err, ErrCodexAppRestartRequired) {
		t.Fatalf("validateRunningCodexAppDaemonReuse() error=%v, want restart required", err)
	}
}

func TestReconcileCodexHostTopologyRejectsLatePrivateAppServer(t *testing.T) {
	enabled := true
	a := &ACPAgent{
		codexHostMode: codexHostModeDaemon, codexRuntimeMode: CodexRuntimeWeClaw,
		codexAppReuseDaemon: &enabled,
		codexAppDaemonInspectCall: func(context.Context) (codexAppDaemonReuseResult, error) {
			return codexAppDaemonReuseResult{AppRunning: true, PrivateAppServer: true}, nil
		},
	}
	if err := a.reconcileCodexHostTopologyLocked(context.Background()); !errors.Is(err, ErrCodexAppRestartRequired) {
		t.Fatalf("reconcileCodexHostTopologyLocked() error=%v, want restart required", err)
	}
}
