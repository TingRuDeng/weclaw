package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type offlineRestartControllerFunc func(context.Context, func(agent.CodexRestartSnapshot) error, agent.CodexRestartOptions) (agent.CodexRestartSnapshot, error)

func (f offlineRestartControllerFunc) PrepareCodexRestartWithOptions(ctx context.Context, persist func(agent.CodexRestartSnapshot) error, opts agent.CodexRestartOptions) (agent.CodexRestartSnapshot, error) {
	return f(ctx, persist, opts)
}

func TestOfflineForcePersistsBeforeServiceAndHostChanges(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	h := &Handler{}
	serviceStopped := false
	checkPending := func() {
		t.Helper()
		state, exists, err := h.readRuntimeRestartState()
		if err != nil || !exists || !state.OfflineForcePending || state.Version != 2 {
			t.Fatalf("pending state=%+v exists=%v err=%v", state, exists, err)
		}
		if err := h.RecoverRuntimeRestart(context.Background()); err == nil {
			t.Fatal("停止阶段不得开放启动")
		}
	}
	controller := offlineRestartControllerFunc(func(_ context.Context, persist func(agent.CodexRestartSnapshot) error, opts agent.CodexRestartOptions) (agent.CodexRestartSnapshot, error) {
		checkPending()
		if !serviceStopped || !opts.ForceTerminateCodex {
			t.Fatal("旧服务退出前不能停止 Host，且必须携带 force")
		}
		snapshot := agent.CodexRestartSnapshot{SocketPath: "/test/socket", HostGeneration: 9, HostStopped: true}
		return snapshot, persist(snapshot)
	})
	err := PrepareOfflineRuntimeRestart(context.Background(), func() error { checkPending(); serviceStopped = true; return nil }, controller, "")
	if err != nil {
		t.Fatal(err)
	}
	state, exists, err := h.readRuntimeRestartState()
	if err != nil || !exists || state.OfflineForcePending || state.Version != 1 || state.CodexHost.HostGeneration != 9 {
		t.Fatalf("prepared state=%+v exists=%v err=%v", state, exists, err)
	}
	if lease, err := agent.AcquireCodexCLIFrontendLease(); err == nil {
		lease.Close()
		t.Fatal("启动恢复前受控 CLI 必须继续被阻止")
	}
}

func TestOfflineForceFailureRetainsStateAndCanRetry(t *testing.T) {
	for _, phase := range []string{"service", "host", "partial-host"} {
		t.Run(phase, func(t *testing.T) {
			t.Setenv("WECLAW_HOME", t.TempDir())
			wantErr := errors.New("停止失败")
			called := false
			controller := offlineRestartControllerFunc(func(_ context.Context, persist func(agent.CodexRestartSnapshot) error, _ agent.CodexRestartOptions) (agent.CodexRestartSnapshot, error) {
				called = true
				snapshot := agent.CodexRestartSnapshot{SocketPath: "/test/socket", HostGeneration: 9, HostStopped: true}
				if phase == "partial-host" {
					snapshot.ConflictingHosts = []agent.CodexRestartConflictSnapshot{{PGID: 42, Stopped: false}}
				}
				if err := persist(snapshot); err != nil {
					return snapshot, err
				}
				if phase == "host" {
					return snapshot, wantErr
				}
				return snapshot, nil
			})
			err := PrepareOfflineRuntimeRestart(context.Background(), func() error {
				if phase == "service" {
					return wantErr
				}
				return nil
			}, controller, "")
			if err == nil {
				t.Fatal("停止失败不得成功")
			}
			if phase == "service" && called {
				t.Fatal("服务未停止不能处理 Host")
			}
			h := &Handler{}
			state, exists, err := h.readRuntimeRestartState()
			if err != nil || !exists || !state.OfflineForcePending {
				t.Fatalf("state=%+v err=%v", state, err)
			}
			if err := h.RecoverRuntimeRestart(context.Background()); err == nil {
				t.Fatal("失败记录不能直接恢复启动")
			}
			// 新 CLI 从已退出的 Host 得到 generation=0，仍须保留首次已知 generation。
			retry := offlineRestartControllerFunc(func(_ context.Context, persist func(agent.CodexRestartSnapshot) error, _ agent.CodexRestartOptions) (agent.CodexRestartSnapshot, error) {
				snapshot := agent.CodexRestartSnapshot{SocketPath: "/test/socket", HostStopped: true}
				return snapshot, persist(snapshot)
			})
			if err := PrepareOfflineRuntimeRestart(context.Background(), func() error { return nil }, retry, ""); err != nil {
				t.Fatal(err)
			}
			state, _, err = h.readRuntimeRestartState()
			if err != nil || state.OfflineForcePending {
				t.Fatalf("retry state=%+v err=%v", state, err)
			}
			if phase != "service" && state.CodexHost.HostGeneration != 9 {
				t.Fatal("重试丢失原 Host generation")
			}
		})
	}
}

func TestOfflineForceJournalWriteFailureDoesNotStopService(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WECLAW_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "state"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	err := PrepareOfflineRuntimeRestart(context.Background(), func() error { t.Fatal("journal 失败前不能停止服务"); return nil }, nil, "")
	if err == nil {
		t.Fatal("journal 写失败应返回错误")
	}
}
