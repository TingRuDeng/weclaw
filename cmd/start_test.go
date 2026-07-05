package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCreateAgentByNameRetriesCodexSQLiteRuntimeStartup(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	t.Setenv("WECLAW_TEST_CODEX_RETRY_HELPER", "1")
	countFile := filepath.Join(t.TempDir(), "attempts")
	t.Setenv("WECLAW_TEST_CODEX_RETRY_COUNT", countFile)
	binDir := t.TempDir()
	codexPath := filepath.Join(binDir, "codex")
	if err := os.Symlink(os.Args[0], codexPath); err != nil {
		t.Fatalf("create codex helper symlink: %v", err)
	}
	oldDelay := codexACPStartupRetryDelay
	codexACPStartupRetryDelay = time.Millisecond
	t.Cleanup(func() { codexACPStartupRetryDelay = oldDelay })

	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{
		Type:    "acp",
		Command: codexPath,
		Args:    []string{"-test.run=TestHelperRetryingCodexAppServer", "app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
	}

	ag := createAgentByName(context.Background(), cfg, "codex")
	if ag == nil {
		t.Fatal("createAgentByName() = nil, want agent after retry")
	}
	t.Cleanup(func() {
		if stopper, ok := ag.(interface{ Stop() }); ok {
			stopper.Stop()
		}
	})
	if got := readRetryHelperAttempts(t, countFile); got != 3 {
		t.Fatalf("attempts=%d, want 3", got)
	}
}

func TestHelperRetryingCodexAppServer(t *testing.T) {
	if os.Getenv("WECLAW_TEST_CODEX_RETRY_HELPER") != "1" {
		return
	}
	countFile := os.Getenv("WECLAW_TEST_CODEX_RETRY_COUNT")
	attempt := readRetryHelperAttempts(t, countFile) + 1
	if err := os.WriteFile(countFile, []byte(fmt.Sprintf("%d", attempt)), 0o600); err != nil {
		t.Fatalf("write retry helper attempts: %v", err)
	}
	if attempt < 3 {
		fmt.Fprintln(os.Stderr, "Error: failed to initialize sqlite state runtime under /Users/dengtingru/.codex: failed to initialize state runtime at /Users/dengtingru/.codex")
		os.Exit(1)
	}
	serveMinimalCodexInitialize(t, os.Stdin, os.Stdout)
	os.Exit(0)
}

func readRetryHelperAttempts(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read retry helper attempts: %v", err)
	}
	var attempts int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &attempts); err != nil {
		t.Fatalf("parse retry helper attempts: %v", err)
	}
	return attempts
}

func serveMinimalCodexInitialize(t *testing.T, stdin io.Reader, stdout io.Writer) {
	t.Helper()
	line, err := bufio.NewReader(stdin).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read initialize request: %v", err)
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		t.Fatalf("parse initialize request: %v", err)
	}
	resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos"}}`+"\n", req.ID)
	if _, err := io.WriteString(stdout, resp); err != nil {
		t.Fatalf("write initialize response: %v", err)
	}
	if _, err := bufio.NewReader(stdin).ReadBytes('\n'); err != nil {
		t.Fatalf("read initialized notification: %v", err)
	}
}

func TestCompanionAutoLaunchDefaultsToRemoteOnly(t *testing.T) {
	cfg := config.AgentConfig{Type: "companion"}
	if companionAutoLaunchEnabled("codex", cfg) {
		t.Fatal("codex companion should not auto launch by default")
	}
	if companionAutoLaunchEnabled("opencode", cfg) {
		t.Fatal("opencode companion should not auto launch by default")
	}
	enabled := true
	cfg.AutoLaunch = &enabled
	if companionAutoLaunchEnabled("codex", cfg) {
		return
	}
	t.Fatal("explicit true should enable codex auto launch")
}

func TestCreateAgentByNameCreatesCompanionAgent(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents["opencode"] = config.AgentConfig{
		Type:    "companion",
		Command: "opencode",
		Cwd:     workspace,
	}

	ag := createAgentByName(context.Background(), cfg, "opencode")
	if ag == nil {
		t.Fatal("createAgentByName() = nil, want companion agent")
	}
	t.Cleanup(func() {
		if stopper, ok := ag.(interface{ Stop() }); ok {
			stopper.Stop()
		}
	})
	info := ag.Info()
	if info.Type != "companion" || info.Name != "opencode" {
		t.Fatalf("Info() = %#v, want opencode companion", info)
	}
}

func TestCreateAgentByNameRejectsUnknownCompanionCommand(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents["opencode"] = config.AgentConfig{
		Type: "companion",
		Cwd:  t.TempDir(),
	}

	ag := createAgentByName(context.Background(), cfg, "opencode")
	if ag != nil {
		t.Fatalf("createAgentByName() = %#v, want nil without command", ag)
	}
}

func TestWechatEnabledDefaultsToTrue(t *testing.T) {
	cfg := config.DefaultConfig()

	if !wechatEnabled(cfg) {
		t.Fatal("wechat should be enabled when platforms.wechat.enabled is omitted")
	}
}

func TestWechatEnabledCanBeDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	disabled := false
	cfg.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{Enabled: &disabled}

	if wechatEnabled(cfg) {
		t.Fatal("wechat should be disabled when platforms.wechat.enabled=false")
	}
}

func TestWechatAggregationWindowDefaultsAndDisables(t *testing.T) {
	if got := wechatAggregationWindow(config.PlatformConfig{}); got != 800*time.Millisecond {
		t.Fatalf("default aggregation window=%s, want 800ms", got)
	}
	zero := 0
	if got := wechatAggregationWindow(config.PlatformConfig{MessageAggregationMs: &zero}); got != 0 {
		t.Fatalf("disabled aggregation window=%s, want 0", got)
	}
}

func TestBuildPlatformRegistryRequiresFeishuCredentialsWhenEnabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	enabled := true
	disabled := false
	cfg.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{Enabled: &disabled}
	cfg.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{Enabled: &enabled}

	_, err := buildPlatformRegistry(nil, cfg)

	if err == nil || !strings.Contains(err.Error(), "load feishu credentials") {
		t.Fatalf("buildPlatformRegistry error=%v, want feishu credential error", err)
	}
}

func TestStopAllWeclawRemovesPidFileAfterProcessExit(t *testing.T) {
	exists := true
	removed := false
	var signals []syscall.Signal

	err := stopAllWeclawWithOps(stopProcessOps{
		readPid: func() (int, error) { return 1234, nil },
		processExists: func(pid int) bool {
			if pid != 1234 {
				t.Fatalf("processExists pid=%d, want 1234", pid)
			}
			return exists
		},
		signalPID: func(pid int, sig syscall.Signal) error {
			signals = append(signals, sig)
			if sig == syscall.SIGTERM {
				exists = false
			}
			return nil
		},
		signalProcessGroup: func(int, syscall.Signal) error { return nil },
		removePIDFile: func() error {
			if exists {
				t.Fatal("进程仍存在时不应删除 pid 文件")
			}
			removed = true
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err != nil {
		t.Fatalf("stopAllWeclawWithOps error: %v", err)
	}
	if !removed {
		t.Fatal("进程退出后应删除 pid 文件")
	}
	if len(signals) != 1 || signals[0] != syscall.SIGTERM {
		t.Fatalf("signals=%v, want only SIGTERM", signals)
	}
}

func TestStopAllWeclawKillsProcessGroupAfterGracefulTimeout(t *testing.T) {
	existsChecks := 0
	removed := false
	var pidSignals []syscall.Signal
	var groupSignals []syscall.Signal

	err := stopAllWeclawWithOps(stopProcessOps{
		readPid: func() (int, error) { return 1234, nil },
		processExists: func(int) bool {
			existsChecks++
			return existsChecks <= gracefulStopChecks+1
		},
		signalPID: func(_ int, sig syscall.Signal) error {
			pidSignals = append(pidSignals, sig)
			return nil
		},
		signalProcessGroup: func(_ int, sig syscall.Signal) error {
			groupSignals = append(groupSignals, sig)
			return nil
		},
		removePIDFile: func() error {
			removed = true
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err != nil {
		t.Fatalf("stopAllWeclawWithOps error: %v", err)
	}
	if !removed {
		t.Fatal("强杀后确认退出时应删除 pid 文件")
	}
	if len(pidSignals) != 2 || pidSignals[0] != syscall.SIGTERM || pidSignals[1] != syscall.SIGKILL {
		t.Fatalf("pidSignals=%v, want SIGTERM then SIGKILL", pidSignals)
	}
	if len(groupSignals) != 1 || groupSignals[0] != syscall.SIGKILL {
		t.Fatalf("groupSignals=%v, want SIGKILL", groupSignals)
	}
}

func TestStopAllWeclawKeepsPidFileWhenProcessSurvivesKill(t *testing.T) {
	err := stopAllWeclawWithOps(stopProcessOps{
		readPid:            func() (int, error) { return 1234, nil },
		processExists:      func(int) bool { return true },
		signalPID:          func(int, syscall.Signal) error { return nil },
		signalProcessGroup: func(int, syscall.Signal) error { return nil },
		removePIDFile: func() error {
			t.Fatal("进程仍存在时不应删除 pid 文件")
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err == nil {
		t.Fatal("stopAllWeclawWithOps error = nil, want process survival error")
	}
}

func TestStopAllWeclawRemovesStalePidFile(t *testing.T) {
	removed := false
	err := stopAllWeclawWithOps(stopProcessOps{
		readPid:            func() (int, error) { return 1234, nil },
		processExists:      func(int) bool { return false },
		signalPID:          func(int, syscall.Signal) error { return errors.New("不应发送信号") },
		signalProcessGroup: func(int, syscall.Signal) error { return errors.New("不应发送信号") },
		removePIDFile: func() error {
			removed = true
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err != nil {
		t.Fatalf("stopAllWeclawWithOps error: %v", err)
	}
	if !removed {
		t.Fatal("陈旧 pid 文件应被删除")
	}
}

func TestReadRuntimeStateSupportsLegacyPidFile(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		t.Fatalf("create weclaw dir: %v", err)
	}
	if err := os.WriteFile(pidFile(), []byte("1234"), 0o600); err != nil {
		t.Fatalf("write legacy pid: %v", err)
	}

	state, err := readRuntimeState()

	if err != nil {
		t.Fatalf("readRuntimeState error: %v", err)
	}
	if state.PID != 1234 {
		t.Fatalf("PID=%d, want 1234", state.PID)
	}
}

func TestWriteRuntimeStatePersistsExecutableIdentity(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	err := writeRuntimeState(runtimeState{
		PID:       1234,
		Exe:       "/tmp/weclaw",
		Version:   "test-version",
		Mode:      "foreground",
		StartedAt: time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("writeRuntimeState error: %v", err)
	}

	state, err := readRuntimeState()
	if err != nil {
		t.Fatalf("readRuntimeState error: %v", err)
	}
	if state.PID != 1234 || state.Exe != "/tmp/weclaw" || state.Mode != "foreground" {
		t.Fatalf("state=%+v, want persisted pid/exe/mode", state)
	}
}

func TestAcquireRuntimeLockRejectsSecondHolder(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	first, err := acquireRuntimeLock()
	if err != nil {
		t.Fatalf("first acquireRuntimeLock error: %v", err)
	}
	defer first.Close()

	second, err := acquireRuntimeLock()
	if err == nil {
		_ = second.Close()
		t.Fatal("second acquireRuntimeLock error = nil, want already running")
	}
	if !strings.Contains(err.Error(), "weclaw 已在运行") {
		t.Fatalf("error=%v, want running hint", err)
	}
}

func TestAcquireDaemonLaunchLockRejectsSecondLauncher(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	first, err := acquireDaemonLaunchLock()
	if err != nil {
		t.Fatalf("first acquireDaemonLaunchLock error: %v", err)
	}
	defer first.Close()

	second, err := acquireDaemonLaunchLock()
	if err == nil {
		_ = second.Close()
		t.Fatal("second acquireDaemonLaunchLock error = nil, want already starting")
	}
	if !strings.Contains(err.Error(), "weclaw 正在启动") {
		t.Fatalf("error=%v, want starting hint", err)
	}
}

func TestHandleDaemonPIDWriteFailureStopsStartedProcess(t *testing.T) {
	stopped := false
	released := false
	err := handleDaemonPIDWriteResult(errors.New("write failed"), daemonPIDWriteProcess{
		kill: func() error {
			stopped = true
			return nil
		},
		wait: func() error {
			return nil
		},
		release: func() error {
			released = true
			return nil
		},
	})

	if err == nil {
		t.Fatal("handleDaemonPIDWriteResult error = nil, want write failure")
	}
	if !stopped {
		t.Fatal("pid 写入失败时应停止已启动进程")
	}
	if released {
		t.Fatal("pid 写入失败时不应 release 失控进程")
	}
}
