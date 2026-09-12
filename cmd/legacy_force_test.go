package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
)

func TestCommandsForceRecoverUnsupportedLegacyRuntime(t *testing.T) {
	for _, command := range []string{"restart", "stop", "update"} {
		t.Run(command, func(t *testing.T) {
			var calls []string
			cfg := config.DefaultConfig()
			unsupported := legacyRuntimeRestartError("v0.1.245")
			prepared := preparedStart{cfg: cfg, run: func() error {
				calls = append(calls, "start")
				return nil
			}}
			prepare := func(context.Context) (preparedStart, error) { return prepared, nil }
			ensureSafe := func(_ context.Context, force bool, _ *config.Config) error {
				if !force {
					t.Fatal("缺少强制授权")
				}
				return unsupported
			}
			forceLegacy := func(_ context.Context, got *config.Config, cause error, start func(bool) error) error {
				if got != cfg || !errors.Is(cause, errCoordinatedRestartUnsupported) {
					t.Fatal("迁移未收到原配置和能力协商错误")
				}
				calls = append(calls, "stop-legacy-and-host")
				if start != nil {
					return start(false)
				}
				return nil
			}
			cancel := func(context.Context, *config.Config) error { t.Fatal("不应补偿不存在的旧接口"); return nil }
			var err error
			switch command {
			case "restart":
				err = runRestart(context.Background(), true, restartOps{prepare: prepare, ensureSafe: ensureSafe, forceLegacy: forceLegacy, cancelDrain: cancel, out: &bytes.Buffer{}})
			case "stop":
				err = runStopWithOptions(context.Background(), true, stopOps{loadConfig: func() (*config.Config, error) { return cfg, nil }, isRunning: func() bool { return true }, prepare: ensureSafe, forceLegacy: forceLegacy, cancel: cancel, out: &bytes.Buffer{}})
			case "update":
				err = completeUpdate(context.Background(), true, true, updateCompletionOps{prepare: prepare, ensureSafe: ensureSafe, forceLegacy: forceLegacy, cancelDrain: cancel, out: &bytes.Buffer{}})
			}
			if err != nil {
				t.Fatalf("强制恢复失败：%v", err)
			}
			want := []string{"stop-legacy-and-host"}
			if command != "stop" {
				want = append(want, "start")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v, want %v", calls, want)
			}
		})
	}
}

func TestUpdateLegacyForceFailureDoesNotRestartOldBinary(t *testing.T) {
	want := errors.Join(messaging.ErrOfflineRuntimeRestartIncomplete, errors.New("Host 停止结果未知"))
	err := completeUpdateWithRollback(context.Background(), true, true, updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig(), run: func() error { t.Fatal("不得启动"); return nil }}, nil
		},
		ensureSafe:  func(context.Context, bool, *config.Config) error { return legacyRuntimeRestartError("v0.1.245") },
		forceLegacy: func(context.Context, *config.Config, error, func(bool) error) error { return want },
		cancelDrain: func(context.Context, *config.Config) error { t.Fatal("不得向旧接口补偿"); return nil },
		out:         &bytes.Buffer{},
	}, func() error { t.Fatal("不得恢复不认识 pending 记录的旧二进制"); return nil })
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateLegacyServiceIdentity(t *testing.T) {
	started := time.Now()
	state := runtimeState{PID: os.Getpid() + 100, Exe: "/test/weclaw", StartedAt: started}
	identity := legacyProcessIdentity{pid: state.PID, uid: os.Geteuid(), pgid: state.PID, executable: state.Exe, startedAt: started}
	for _, test := range []struct {
		name   string
		change func(*runtimeState, *legacyProcessIdentity)
		args   []string
		valid  bool
	}{
		{"valid", func(*runtimeState, *legacyProcessIdentity) {}, []string{state.Exe, "start", "-f"}, true},
		{"wrong-uid", func(_ *runtimeState, i *legacyProcessIdentity) { i.uid++ }, []string{state.Exe, "start"}, false},
		{"reused-pid", func(_ *runtimeState, i *legacyProcessIdentity) { i.startedAt = started.Add(time.Minute) }, []string{state.Exe, "start"}, false},
		{"wrong-executable", func(_ *runtimeState, i *legacyProcessIdentity) { i.executable = "/bin/sh" }, []string{state.Exe, "start"}, false},
		{"missing-time", func(s *runtimeState, _ *legacyProcessIdentity) { s.StartedAt = time.Time{} }, []string{state.Exe, "start"}, false},
		{"not-service", func(*runtimeState, *legacyProcessIdentity) {}, []string{state.Exe, "restart"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, i := state, identity
			test.change(&s, &i)
			err := validateLegacyServiceIdentity(s, i, test.args)
			if (err == nil) != test.valid {
				t.Fatalf("err=%v valid=%v", err, test.valid)
			}
		})
	}
}

func TestLegacyForceUsesConfiguredCodexAuthority(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	candidate := config.AgentConfig{Command: "codex", Args: []string{"app-server"}}
	cfg.Agents = map[string]config.AgentConfig{"primary": candidate, "secondary": candidate}
	if _, err := configuredLegacyCodexController(context.Background(), cfg); err == nil {
		t.Fatal("多个无明确权威的 Codex Agent 必须拒绝")
	}
	cfg.Agents["codex"] = candidate
	if controller, err := configuredLegacyCodexController(context.Background(), cfg); err != nil || controller == nil {
		t.Fatalf("controller=%v err=%v", controller, err)
	}
}
