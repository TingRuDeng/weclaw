package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

// TestEnsureRestartSafeWithConfigAllowsMissingRuntime 验证服务未运行时无需访问状态接口。
func TestEnsureRestartSafeWithConfigAllowsMissingRuntime(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	if err := ensureRestartSafeWithConfig(context.Background(), false, config.DefaultConfig()); err != nil {
		t.Fatalf("ensureRestartSafeWithConfig error=%v", err)
	}
}

// TestEnsureRestartSafeWithConfigUsesValidatedSnapshot 验证安全检查读取已预检配置中的 API 地址。
func TestEnsureRestartSafeWithConfigUsesValidatedSnapshot(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	activeTasks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(runtimeStatusResponse{Status: "ok", ActiveTasks: &activeTasks})
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw"}); err != nil {
		t.Fatalf("writeRuntimeState error=%v", err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	if err := ensureRestartSafeWithConfig(context.Background(), false, cfg); err != nil {
		t.Fatalf("ensureRestartSafeWithConfig error=%v", err)
	}
}

func TestBeginRestartDrainUsesAtomicRuntimeEndpoint(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.Method != http.MethodPost || r.URL.Path != "/api/runtime/restart/prepare" ||
			r.URL.Query().Get("force_drain") != "true" || r.URL.Query().Get("force") != "" {
			t.Fatalf("request=%s %s, want task-only force drain POST", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "ok", Draining: true, ActiveTasks: 1})
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw"}); err != nil {
		t.Fatalf("writeRuntimeState error=%v", err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	if err := beginRestartDrainWithConfig(context.Background(), true, cfg); err != nil {
		t.Fatalf("beginRestartDrainWithConfig: %v", err)
	}
	if !requested {
		t.Fatal("runtime drain endpoint was not called")
	}
}

func TestBeginRestartDrainOptionsPassesCodexTerminationAuthorization(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.Method != http.MethodPost || r.URL.Path != "/api/runtime/restart/prepare" ||
			r.URL.RawQuery != "force=true" {
			t.Fatalf("request=%s %s, want operator force POST", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "ok", Draining: true})
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	if err := beginRestartDrainWithConfigOptions(context.Background(), true, cfg); err != nil {
		t.Fatalf("beginRestartDrainWithConfigOptions: %v", err)
	}
	if !requested {
		t.Fatal("runtime restart endpoint was not called")
	}
}

func TestBeginRestartDrainOptionsKeepsOrdinaryRestartNonForced(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.Method != http.MethodPost || r.URL.Path != "/api/runtime/restart/prepare" ||
			r.URL.RawQuery != "" {
			t.Fatalf("普通重启不得附加强制停止授权，实际请求：%s %s", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "ok", Draining: true})
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	if err := beginRestartDrainWithConfigOptions(context.Background(), false, cfg); err != nil {
		t.Fatalf("beginRestartDrainWithConfigOptions: %v", err)
	}
	if !requested {
		t.Fatal("runtime restart endpoint was not called")
	}
}

func TestBeginRestartDrainReportsActiveTaskConflict(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "busy", ActiveTasks: 2, RemainingTasks: 2})
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw"}); err != nil {
		t.Fatalf("writeRuntimeState error=%v", err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	err := beginRestartDrainWithConfig(context.Background(), false, cfg)
	if err == nil || !strings.Contains(err.Error(), "2 个运行中的任务") {
		t.Fatalf("error=%v, want active task conflict", err)
	}
}

func TestBeginRestartDrainPreservesUnknownHostStopOutcome(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "error",
			"code":     "runtime_restart_host_outcome_unknown",
			"message":  "Codex Host 停止结果未知",
			"draining": true,
		})
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")

	err := beginRestartDrainWithConfig(context.Background(), true, cfg)
	if !errors.Is(err, agent.ErrCodexRestartUnsafe) {
		t.Fatalf("error=%v, want ErrCodexRestartUnsafe across runtime API", err)
	}
}

func TestBeginRestartDrainReportsLegacyRuntimeMigration(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	if err := writeRuntimeState(runtimeState{
		PID: os.Getpid(), Exe: "/tmp/weclaw", Version: "v0.1.267",
	}); err != nil {
		t.Fatalf("writeRuntimeState error=%v", err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")

	err := beginRestartDrainWithConfig(context.Background(), false, cfg)

	if err == nil {
		t.Fatal("beginRestartDrainWithConfig error=nil, want legacy migration guidance")
	}
	message := err.Error()
	if strings.Contains(message, "cannot unmarshal number") {
		t.Fatalf("error=%v, plain-text 404 must not be decoded as a JSON number", err)
	}
	for _, want := range []string{"v0.1.267", "未停止任何进程", "weclaw restart --force", "中断任务"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error=%v, want %q", err, want)
		}
	}
}

func TestStopLegacyRuntimeRequiresNoActiveTasks(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	activeTasks := 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtime/drain" {
			t.Fatalf("path=%s, want runtime drain", r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "busy", ActiveTasks: activeTasks, RemainingTasks: activeTasks, Message: "旧版服务仍有 1 个运行中的任务"})
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw", Version: "v0.1.267"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	stopped := false
	err := stopLegacyRuntime(context.Background(), cfg, func() error {
		stopped = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "1 个运行中的任务") || stopped {
		t.Fatalf("error=%v stopped=%v, want migration blocked before process stop", err, stopped)
	}
}

func TestStopLegacyRuntimeStopsOnlyAfterIdleStatus(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	activeTasks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runtime/drain" {
			t.Fatalf("request=%s %s, want runtime drain POST", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "ok", Draining: true, ActiveTasks: activeTasks, RemainingTasks: 0})
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw", Version: "v0.1.267"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	stopped := false
	if err := stopLegacyRuntime(context.Background(), cfg, func() error {
		stopped = true
		return nil
	}); err != nil {
		t.Fatalf("stopLegacyRuntime: %v", err)
	}
	if !stopped {
		t.Fatal("idle legacy runtime should be stopped")
	}
}

func TestStopLegacyRuntimeReportsAdmissionRecoveryFailure(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "ok", Draining: true})
		case http.MethodDelete:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("recovery unavailable"))
		default:
			t.Fatalf("unexpected request=%s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw", Version: "v0.1.267"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	stopErr := errors.New("service did not exit")
	err := stopLegacyRuntime(context.Background(), cfg, func() error { return stopErr })
	if !errors.Is(err, stopErr) || !strings.Contains(err.Error(), "恢复旧版服务排空失败") {
		t.Fatalf("error=%v, want stop and admission recovery failures", err)
	}
	if got, want := strings.Join(requests, ","), "POST /api/runtime/drain,DELETE /api/runtime/drain"; got != want {
		t.Fatalf("requests=%s, want %s", got, want)
	}
}

func TestStopLegacyRuntimeHoldsCodexFrontendLeaseUntilStopReturns(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtime/drain" {
			t.Fatalf("request=%s %s, want runtime drain", r.Method, r.URL.Path)
		}
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "ok", Draining: true})
		case http.MethodDelete:
			_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "ok", Draining: false})
		default:
			t.Fatalf("request=%s %s, want POST or DELETE", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw", Version: "v0.1.267"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	stopErr := errors.New("service did not exit")
	var stopLeaseErr error
	err := stopLegacyRuntime(context.Background(), cfg, func() error {
		_, stopLeaseErr = agent.AcquireCodexCLIFrontendLease()
		return stopErr
	})
	if !errors.Is(err, stopErr) || !errors.Is(stopLeaseErr, agent.ErrCodexRestartInProgress) {
		t.Fatalf("error=%v stopLeaseErr=%v, want restart lease held during stop", err, stopLeaseErr)
	}
	lease, err := agent.AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatalf("frontend lease remained held after migration stop returned: %v", err)
	}
	_ = lease.Close()
}

func TestStopLegacyRuntimeRefusesWhenCodexCLIFrontendLeaseIsActive(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	activeLease, err := agent.AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatalf("AcquireCodexCLIFrontendLease: %v", err)
	}
	defer activeLease.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "/tmp/weclaw", Version: "v0.1.267"}); err != nil {
		t.Fatal(err)
	}
	stopCalled := false
	err = stopLegacyRuntime(context.Background(), config.DefaultConfig(), func() error {
		stopCalled = true
		return nil
	})
	if !errors.Is(err, agent.ErrCodexCLIFrontendActive) || stopCalled {
		t.Fatalf("error=%v stopCalled=%v, want active CLI lease to block migration", err, stopCalled)
	}
}

func TestCancelRestartDrainRequiresConfirmedAdmissionRecovery(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.Method != http.MethodDelete || r.URL.Path != "/api/runtime/restart/prepare" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(runtimeDrainResponse{Status: "ok", Draining: false})
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.APIAddr = strings.TrimPrefix(server.URL, "http://")
	if err := cancelRestartDrain(context.Background(), cfg); err != nil {
		t.Fatalf("cancelRestartDrain: %v", err)
	}
	if !requested {
		t.Fatal("runtime restart cancellation endpoint was not called")
	}
}
