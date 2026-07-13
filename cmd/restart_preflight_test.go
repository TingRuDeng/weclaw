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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(runtimeStatusResponse{})
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
