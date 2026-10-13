package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
	"github.com/gorilla/websocket"
)

func TestCodexAppSharedHostStartsAndSurvivesFrontendCancellation(t *testing.T) {
	if !codexAppSharedHostAvailable() {
		t.Skip("Codex App signed Node is unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "weclaw-app-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "app-server.sock")
	bridgeDir := codexAppBridgeDirectory(socket)
	if err := securefile.EnsureDir(bridgeDir); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexAppBridgeArtifact(filepath.Join(bridgeDir, "host.cjs"), codexAppHostScript, false); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: os.Args[0], Args: []string{"app-server"}, Cwd: dir,
		CodexHostMode: "shared",
		Env: map[string]string{
			testCodexUnixHostSocketEnv: socket,
			testCodexUnixHostCountEnv:  filepath.Join(dir, "starts.log"),
		},
	})
	// Keep identity validation real without scanning unrelated user processes.
	a.codexHostConflictPreflightCall = func(context.Context, int) error {
		_, err := a.validateManagedCodexHost(socket)
		return err
	}
	t.Cleanup(func() {
		metadata, err := a.readCodexHostMetadata(socket)
		if err != nil {
			return
		}
		process, err := os.FindProcess(metadata.PID)
		if err != nil {
			t.Error(err)
			return
		}
		defer process.Release()
		if err := process.Kill(); err != nil {
			t.Error(err)
			return
		}
		deadline := time.Now().Add(3 * time.Second)
		for codexHostProcessAlive(metadata.PID) {
			if time.Now().After(deadline) {
				t.Error("signed Node launcher did not reap its test Host")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := a.startCodexAppSharedHostLocked(ctx, socket, os.Args[0], []string{
		"-test.run=^TestHelperCodexUnixHost$", "-test.timeout=15s", "--",
	})
	if err != nil {
		t.Fatalf("start shared Host through signed Node: %v", err)
	}
	defer conn.Close()
	cancel()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{"id": 1, "method": "initialize", "params": codexInitializeParams()}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     int `json:"id"`
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatalf("shared Host stopped with its frontend: %v", err)
	}
	if response.ID != 1 || response.Result.ServerInfo.Name != "fake-codex-host" {
		t.Fatalf("unexpected shared Host handshake: %+v", response)
	}
}

func TestCodexAppBridgeMCPPreloadFollowsCurrentAppPipe(t *testing.T) {
	node := codexAppSharedNodePath()
	if info, err := os.Stat(node); err != nil || !info.Mode().IsRegular() {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Skip("Node is unavailable")
		}
	}
	dir := filepath.Join(t.TempDir(), "bridge with spaces")
	if err := securefile.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexAppBridgeArtifact(filepath.Join(dir, "mcp-env.mjs"), codexAppMCPEnvironmentScript, false); err != nil {
		t.Fatal(err)
	}
	args, _, err := normalizeCodexAppBridgeArgs([]string{"app-server", "-c", `mcp_servers.codex_app={env={CODEX_APP_TOOLS_PIPE_PATH="/tmp/first.sock"}}`}, dir)
	if err != nil {
		t.Fatal(err)
	}
	var options string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(args[len(args)-1], "mcp_servers.codex_app.env.NODE_OPTIONS=")), &options); err != nil {
		t.Fatal(err)
	}
	for _, pipe := range []string{"/tmp/first.sock", "/tmp/restarted app.sock"} {
		if _, err := saveCodexAppPipe(dir, pipe); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		command := exec.CommandContext(ctx, node, "-e", "process.stdout.write(process.env.CODEX_APP_TOOLS_PIPE_PATH)")
		command.Env = append(os.Environ(), "NODE_OPTIONS="+options, "CODEX_APP_TOOLS_PIPE_PATH=stale")
		output, err := command.CombinedOutput()
		cancel()
		if err != nil || string(output) != pipe {
			t.Fatalf("MCP pipe=%q want=%q err=%v", output, pipe, err)
		}
	}
}

func TestCodexAppBridgePreservesSettingsAcrossAppPipeChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "path with spaces")
	appConfig := `mcp_servers.codex_app={command="/app/official-mcp", args=["./server.mjs"], enabled=true, tools={send_message_to_thread={approval_mode="prompt"}}, env={CODEX_APP_TOOLS_PIPE_PATH="/tmp/first.sock", CODEX_MCP_NODE_PATH="/app/node"}}`
	args := []string{"-c", "features.code_mode_host=true", "app-server", "--stdio", "--analytics-default-enabled", "-c", appConfig}
	first, firstPipe, err := normalizeCodexAppBridgeArgs(args, dir)
	if err != nil {
		t.Fatal(err)
	}
	secondArgs := slices.Clone(args)
	secondArgs[len(secondArgs)-1] = strings.ReplaceAll(appConfig, "/tmp/first.sock", "/tmp/second.sock")
	second, secondPipe, err := normalizeCodexAppBridgeArgs(secondArgs, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) || firstPipe != "/tmp/first.sock" || secondPipe != "/tmp/second.sock" {
		t.Fatalf("App restart changed static launch configuration: %v %v", first, second)
	}
	for _, want := range []string{"features.code_mode_host=true", "--analytics-default-enabled", `approval_mode="prompt"`, `CODEX_MCP_NODE_PATH="/app/node"`} {
		if !strings.Contains(strings.Join(first, "\n"), want) {
			t.Fatalf("App configuration lost: %s", want)
		}
	}
	if args[3] != "--stdio" || !strings.Contains(args[len(args)-1], "/tmp/first.sock") {
		t.Fatal("mutated caller arguments")
	}
}

func TestCodexAppBridgeAcceptsDynamicMCPConfigOnExistingHost(t *testing.T) {
	prior := []string{"-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled"}
	current := append(slices.Clone(prior), "-c", `mcp_servers.codex_app={env={CODEX_APP_TOOLS_PIPE_PATH="/tmp/app.sock"}}`)
	if !slices.Equal(codexAppStaticArgs(prior), codexAppStaticArgs(current)) {
		t.Fatalf("dynamic MCP config changed shared Host identity: prior=%v current=%v", prior, current)
	}
	withBridgePreload := append(slices.Clone(current), "-c", `mcp_servers.codex_app.env.NODE_OPTIONS="\\\"--import=file:///tmp/mcp-env.mjs\\\""`)
	if !slices.Equal(codexAppStaticArgs(prior), codexAppStaticArgs(withBridgePreload)) {
		t.Fatalf("bridge preload config changed shared Host identity: prior=%v current=%v", prior, withBridgePreload)
	}
	withPluginOverride := append(slices.Clone(withBridgePreload), "-c", "plugins.codex-app-tools@openai-bundled.mcp_servers.codex_app.enabled=true")
	if !slices.Equal(codexAppStaticArgs(prior), codexAppStaticArgs(withPluginOverride)) {
		t.Fatalf("plugin MCP config changed shared Host identity: prior=%v current=%v", prior, withPluginOverride)
	}
	withPluginDefinition := append(slices.Clone(withPluginOverride), "-c", `plugins.codex-app-tools@openai-bundled.mcp_servers.codex_app={enabled=true}`)
	if !slices.Equal(codexAppStaticArgs(prior), codexAppStaticArgs(withPluginDefinition)) {
		t.Fatalf("plugin MCP definition changed shared Host identity: prior=%v current=%v", prior, withPluginDefinition)
	}
	changed := append(slices.Clone(prior), "--listen", "unix:///tmp/other.sock")
	if slices.Equal(codexAppStaticArgs(prior), codexAppStaticArgs(changed)) {
		t.Fatal("transport override was incorrectly treated as dynamic MCP config")
	}
}

func TestCodexAppBootstrapIsReplacedWithCompleteConfiguration(t *testing.T) {
	if !codexAppSharedHostAvailable() {
		t.Skip("Codex App signed Node is unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "weclaw-bootstrap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("WECLAW_HOME", filepath.Join(dir, "weclaw"))
	socket := codexAppSharedSocketForHome(dir)
	bridgeDir := codexAppBridgeDirectory(socket)
	if err := securefile.EnsureDir(bridgeDir); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexAppBridgeArtifact(filepath.Join(bridgeDir, "host.cjs"), codexAppHostScript, false); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(dir, "starts.log")
	managed := filepath.Join(dir, "packages", "standalone", "current", "codex")
	if err := securefile.EnsureDir(filepath.Dir(managed)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{Command: os.Args[0], Cwd: dir, CodexHostMode: "shared", Env: map[string]string{
		"CODEX_HOME": dir, testCodexUnixHostSocketEnv: socket, testCodexUnixHostCountEnv: count,
	}})
	a.protocol = protocolCodexAppServer
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	t.Cleanup(func() {
		if _, err := a.validateManagedCodexHost(socket); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.stopManagedCodexHostLocked(ctx, socket); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bootstrap := []string{"-test.run=^TestHelperCodexUnixHost$", "-test.timeout=30s", "--", "-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled"}
	first, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], bootstrap, "")
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := a.readCodexAppBootstrap(socket); err != nil || pending == nil || pending.Host == nil {
		t.Fatalf("bootstrap must remain provisional: %#v %v", pending, err)
	}
	a.codexAppSharedEnvironmentCall = func(context.Context, string) error {
		t.Fatal("message client attached before App configuration was complete")
		return nil
	}
	if ready, err := a.attachReadyCodexAppHost(ctx, socket, "/tmp/test-launcher"); err != nil || ready {
		t.Fatalf("provisional Host admitted a message client: %v %v", ready, err)
	}
	_ = first.Close()
	before, err := a.readCodexHostMetadata(socket)
	if err != nil {
		t.Fatal(err)
	}
	formal := append(slices.Clone(bootstrap), "-c", "plugins.codex-app-tools@openai-bundled.mcp_servers.codex_app.enabled=true", "-c", "plugins.code-review@openai-bundled.mcp_servers.code-review.enabled=true")
	// A controlled CLI waiting for App startup already holds its lifetime lease.
	frontend, err := AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatal(err)
	}
	defer frontend.Close()
	if _, err := a.PrepareCodexCLILaunch(ctx, CodexCLILaunchOptions{AllowHostStart: false}); err == nil || !strings.Contains(err.Error(), "尚未确认") {
		t.Fatalf("CLI attached before formal configuration: %v", err)
	}
	second, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], formal, "/tmp/test-app.sock")
	if err != nil {
		t.Fatalf("App formal configuration could not replace bootstrap: %v", err)
	}
	defer second.Close()
	after, err := a.readCodexHostMetadata(socket)
	if err != nil {
		t.Fatal(err)
	}
	if after.PID == before.PID || after.Generation <= before.Generation {
		t.Fatal("formal settings were not applied to a new Host generation")
	}
	_, liveArgs, err := readCodexHostProcessArgs(after.PID)
	if err != nil || !slices.Equal(liveArgs[1:], codexSharedHostArgs(formal, socket)) {
		t.Fatalf("live Host did not receive full settings: %v %v", liveArgs, err)
	}
	if pending, err := a.readCodexAppBootstrap(socket); err != nil || pending != nil {
		t.Fatalf("formal configuration not marked ready: %#v %v", pending, err)
	}
	a.codexAppSharedEnvironmentCall = func(context.Context, string) error { return nil }
	if ready, err := a.attachReadyCodexAppHost(ctx, socket, "/tmp/test-launcher"); err != nil || !ready {
		t.Fatalf("message client could not attach to confirmed Host: %v %v", ready, err)
	}
	if _, err := a.rpc(ctx, "thread/loaded/list", map[string]any{"limit": 1}); err != nil {
		t.Fatalf("confirmed connection refers to a stopped provisional Host: %v", err)
	}
	connection, _, _ := a.disconnectCodexHostClient(false)
	if connection != nil {
		_ = connection.Close()
	}
	data, err := securefile.Read(filepath.Join(bridgeDir, "app-args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var applied []string
	if err := json.Unmarshal(data, &applied); err != nil || !slices.Equal(applied, formal) {
		t.Fatalf("applied args=%v err=%v", applied, err)
	}
	// Reopening the App must let its configuration-only bootstrap use this Host.
	third, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], bootstrap, "")
	if err != nil {
		t.Fatalf("reopen bootstrap: %v", err)
	}
	_ = third.Close()
	if current, err := a.readCodexHostMetadata(socket); err != nil || current.PID != after.PID {
		t.Fatalf("reopen changed Host: %#v %v", current, err)
	}
	starts, err := os.ReadFile(count)
	if err != nil || string(starts) != "start\nstart\n" {
		t.Fatalf("starts=%q err=%v", starts, err)
	}
	// After a coordinated stop, early config reads use the saved full settings.
	_ = second.Close()
	if err := a.stopManagedCodexHostLocked(ctx, socket); err != nil {
		t.Fatal(err)
	}
	cold, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], bootstrap, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = cold.Close()
	restarted, err := a.readCodexHostMetadata(socket)
	if err != nil {
		t.Fatal(err)
	}
	_, liveArgs, err = readCodexHostProcessArgs(restarted.PID)
	if err != nil || !slices.Equal(liveArgs[1:], codexSharedHostArgs(formal, socket)) {
		t.Fatalf("cold bootstrap discarded saved settings: %v %v", liveArgs, err)
	}
	confirmed, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], formal, "/tmp/new-app.sock")
	if err != nil {
		t.Fatal(err)
	}
	_ = confirmed.Close()
	if current, err := a.readCodexHostMetadata(socket); err != nil || current.PID != restarted.PID {
		t.Fatalf("cached configuration required another restart: %#v %v", current, err)
	}
	// Confirmed Hosts retain their configuration even when no thread is loaded.
	changed := append(slices.Clone(formal), "-c", codexAppReviewEnabledKey+"false")
	if conn, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], changed, "/tmp/ignored.sock"); err == nil {
		_ = conn.Close()
		t.Fatal("confirmed Host was silently replaced")
	}
	if current, err := a.validateManagedCodexHost(socket); err != nil || !sameCodexHostGeneration(current, restarted) {
		t.Fatalf("confirmed Host was changed: %#v %v", current, err)
	}
}

func TestCodexAppBootstrapPreservesLoadedThreadAndRejectsStaleRecord(t *testing.T) {
	if !codexAppSharedHostAvailable() {
		t.Skip("Codex App signed Node is unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "weclaw-bootstrap-guard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "host.sock")
	bridgeDir := codexAppBridgeDirectory(socket)
	if err := securefile.EnsureDir(bridgeDir); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexAppBridgeArtifact(filepath.Join(bridgeDir, "host.cjs"), codexAppHostScript, false); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{Command: os.Args[0], Cwd: dir, CodexHostMode: "shared", Env: map[string]string{
		testCodexUnixHostSocketEnv: socket, testCodexUnixHostCountEnv: filepath.Join(dir, "starts.log"),
	}})
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	t.Cleanup(func() {
		if _, err := a.validateManagedCodexHost(socket); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.stopManagedCodexHostLocked(ctx, socket); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bootstrap := []string{"-test.run=^TestHelperCodexUnixHost$", "-test.timeout=30s", "--", "app-server"}
	conn, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], bootstrap, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.WriteJSON(map[string]any{"id": 1, "method": "thread/start", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	var response struct{ Result json.RawMessage }
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	before, err := a.validateManagedCodexHost(socket)
	if err != nil {
		t.Fatal(err)
	}
	formal := append(slices.Clone(bootstrap), "-c", codexAppReviewEnabledKey+"true")
	if c, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], formal, ""); err == nil || !strings.Contains(err.Error(), "已有会话") {
		if c != nil {
			_ = c.Close()
		}
		t.Fatalf("loaded provisional Host was not protected: %v", err)
	}
	if after, err := a.validateManagedCodexHost(socket); err != nil || !sameCodexHostGeneration(before, after) {
		t.Fatalf("loaded Host identity changed: %#v %v", after, err)
	}
	stale := before
	stale.Generation++
	if err := writeCodexAppBootstrap(socket, &stale); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{bootstrap, formal} {
		if c, err := a.connectCodexAppBridgeLocked(ctx, socket, os.Args[0], args, ""); err == nil || !strings.Contains(err.Error(), "Host 已变化") {
			if c != nil {
				_ = c.Close()
			}
			t.Fatalf("stale record allowed an App connection: %v", err)
		}
	}
}

func TestCodexAppBootstrapCannotExecuteTasks(t *testing.T) {
	client, server := newCodexWebSocketPair(t)
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := strings.NewReader(`{"id":7,"method":"turn/start","params":{"threadId":"original"}}` + "\n")
	err := relayCodexAppBridgeFiltered(ctx, client, input, io.Discard, validateCodexAppBootstrapMessage)
	if err == nil || !strings.Contains(err.Error(), "不能执行") {
		t.Fatalf("bootstrap accepted task: %v", err)
	}
	_ = server.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := server.ReadMessage(); err == nil {
		t.Fatal("task was forwarded before configuration readiness")
	}
	for _, method := range []string{"initialize", "initialized", "configRequirements/read", "config/read", "account/logout"} {
		message, _ := json.Marshal(map[string]string{"method": method})
		if err := validateCodexAppBootstrapMessage(message); err != nil {
			t.Fatalf("startup RPC %s: %v", method, err)
		}
	}
}

func TestCodexAppModernPipeUsesSignedNodeLauncher(t *testing.T) {
	if !codexAppSharedHostAvailable() {
		t.Skip("Codex App signed Node is unavailable")
	}
	dir := filepath.Join(t.TempDir(), "bridge with spaces")
	if err := securefile.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexAppBridgeArtifact(filepath.Join(dir, "mcp-env.mjs"), codexAppMCPEnvironmentScript, false); err != nil {
		t.Fatal(err)
	}
	args := []string{"app-server", "-c", codexAppToolsEnabledKey + "true", "-c", codexAppReviewEnabledKey + "true"}
	normalized, pipe, err := normalizeCodexAppBridgeArgsWithPipe(args, dir, "/tmp/first-app.sock")
	if err != nil || !slices.Equal(normalized, args) || pipe != "/tmp/first-app.sock" {
		t.Fatalf("modern settings changed: %v %q %v", normalized, pipe, err)
	}
	launcher, err := writeCodexAppMCPNodeLauncher(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/tmp/first-app.sock", "/tmp/reopened app.sock"} {
		if _, err := saveCodexAppPipe(dir, want); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(context.Background(), launcher, "-e", "process.stdout.write(process.env.CODEX_APP_TOOLS_PIPE_PATH)")
		command.Env = append(os.Environ(), "CODEX_APP_TOOLS_PIPE_PATH=stale")
		output, err := command.CombinedOutput()
		if err != nil || string(output) != want {
			t.Fatalf("signed MCP Node pipe=%q, want=%q, err=%v", output, want, err)
		}
	}
	for _, invalid := range []string{"", "relative.sock"} {
		if _, _, err := normalizeCodexAppBridgeArgsWithPipe(args, dir, invalid); err == nil {
			t.Fatalf("enabled App tools accepted pipe %q", invalid)
		}
	}
}

func TestCodexAppBridgeRejectsConflictingTransportAndPreload(t *testing.T) {
	for _, args := range [][]string{
		{"app-server", "--listen", "unix:///tmp/other.sock"},
		{"app-server", "--listen=stdio://"},
		{"app-server", "-c", `mcp_servers.codex_app={env={CODEX_APP_TOOLS_PIPE_PATH="/tmp/app.sock", NODE_OPTIONS="--inspect"}}`},
	} {
		if _, _, err := normalizeCodexAppBridgeArgs(args, t.TempDir()); err == nil {
			t.Fatalf("accepted conflicting configuration %v", args)
		}
	}
}

func TestExistingSharedHostConfiguresNextAppLaunch(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		CodexHostMode: "shared",
	})
	called := false
	a.codexAppSharedEnvironmentCall = func(_ context.Context, launcher string) error {
		called = true
		if launcher != "/tmp/weclaw-app-launcher" {
			t.Fatalf("launcher=%q", launcher)
		}
		return nil
	}
	if err := a.configureCodexAppSharedEnvironment(context.Background(), "/tmp/weclaw-app-launcher", false); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("existing shared Host did not configure the next App launch")
	}
}

func TestCodexAppBridgeRelaysAppJSONLWithoutChangingMessages(t *testing.T) {
	client, server := newCodexWebSocketPair(t)
	defer client.Close()
	defer server.Close()
	appInput, inputWriter := io.Pipe()
	defer appInput.Close()
	defer inputWriter.Close()
	appOutput, outputWriter := io.Pipe()
	defer appOutput.Close()
	defer outputWriter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relayCodexAppBridge(ctx, client, appInput, outputWriter) }()
	request := `{"id":"app-1","method":"turn/steer","params":{"threadId":"original","expectedTurnId":"running","input":[{"type":"text","text":"继续原任务"}]}}`
	go func() { _, _ = io.WriteString(inputWriter, request+"\n") }()
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, message, err := server.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(message) != request {
		t.Fatalf("changed App request: %s", message)
	}
	response := `{"id":"app-1","result":{"turnId":"running"}}`
	if err := server.WriteMessage(websocket.TextMessage, []byte(response)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(appOutput).ReadString('\n')
	if err != nil || line != response+"\n" {
		t.Fatalf("App response=%q err=%v", line, err)
	}
	_ = inputWriter.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("App EOF did not release its connection relay")
	}
}

func TestCodexAppSharedHostDoesNotUsePrivateDesktopOwnership(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"), CodexHostMode: "shared", CodexDesktopBridge: true})
	if a.desktopProbe != nil || a.codexDesktopHostSelection || a.codexOwners.enforcesControl() {
		t.Fatal("shared App routing still depends on private Desktop ownership")
	}
	if !a.officialDaemonIsAuthoritativeForUnknownBinding() {
		t.Fatal("shared Host cannot read a thread with an unknown cached binding")
	}
	a.codexDesktopPresenceCall = func() (bool, bool) { return false, true }
	if !a.shouldDeferCodexAppDynamicToolCall() {
		t.Fatal("shared Host rejects a dynamic tool owned by the connected App")
	}
}
