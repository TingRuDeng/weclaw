package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
		if r.Method != http.MethodPost || r.URL.Path != "/api/runtime/restart/prepare" || r.URL.Query().Get("force") != "true" {
			t.Fatalf("request=%s %s, want force drain POST", r.Method, r.URL.String())
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
	for _, want := range []string{"v0.1.267", "weclaw stop", "weclaw start", "weclaw restart"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error=%v, want %q", err, want)
		}
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
