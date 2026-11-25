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

	if !errors.Is(err, errCoordinatedRestartUnsupported) {
		t.Fatalf("error=%v, want errCoordinatedRestartUnsupported", err)
	}
	message := err.Error()
	if strings.Contains(message, "cannot unmarshal number") {
		t.Fatalf("error=%v, plain-text 404 must not be decoded as a JSON number", err)
	}
	for _, want := range []string{"v0.1.267", "未停止任何进程", "手动停止旧版 WeClaw 服务"} {
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
