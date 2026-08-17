package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

const (
	testCodexDaemonProtocolEnv = "WECLAW_TEST_CODEX_DAEMON_PROTOCOL"
	testCodexDaemonHomeEnv     = "WECLAW_TEST_CODEX_HOME"
	protocolLiveEventTimeout   = 45 * time.Second
)

// TestCodexOfficialDaemonTwoClientProtocol is an opt-in compatibility gate for
// assumptions that cannot be proven with an in-process fake app-server.
func TestCodexOfficialDaemonTwoClientProtocol(t *testing.T) {
	if os.Getenv(testCodexDaemonProtocolEnv) != "1" {
		t.Skip("set WECLAW_TEST_CODEX_DAEMON_PROTOCOL=1 to run the official daemon protocol gate")
	}

	preparedHome := requireIsolatedCodexDaemonHome(t)
	runtimeHome := prepareCodexDaemonLiveRuntimeHome(t, preparedHome)
	codexHome := runtimeHome.codexHome
	workspace := t.TempDir()
	const (
		approvalCommand = "mkdir LIVE_GATE_APPROVAL_7F3A"
		approvalCallID  = "weclaw-live-approval"
		steerMarker     = "LIVE_GATE_STEER_MARKER_7F3A"
		finalAnswer     = "LIVE_GATE_READY " + steerMarker
	)
	approvalTarget := filepath.Join(workspace, "LIVE_GATE_APPROVAL_7F3A")
	modelServer := newCodexDaemonLiveModelServer(t, workspace, approvalCommand, approvalCallID, finalAnswer)
	installCodexDaemonLiveModelConfig(t, codexHome, modelServer.URL())
	managedBinary := codexDaemonManagedBinaryPath(codexHome)
	clientA := newCodexDaemonLiveClient(
		managedBinary, runtimeHome, workspace, filepath.Join(workspace, "client-a.json"),
	)
	clientB := newCodexDaemonLiveClient(
		managedBinary, runtimeHome, workspace, filepath.Join(workspace, "client-b.json"),
	)
	socketPath := codexDaemonSocketPath(codexHome)

	var ownedMetadata codexHostMetadata
	daemonOwned := false
	t.Cleanup(func() {
		clientB.Stop()
		clientA.Stop()
		err := runCodexDaemonLiveCleanupSteps(codexDaemonLiveCleanupSteps{
			stopDaemon: func() error {
				if !daemonOwned {
					return fmt.Errorf("isolated official daemon ownership was not recorded")
				}
				stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				lifecycleLock, err := clientA.acquireCodexHostStartupLock(stopCtx, socketPath)
				if err != nil {
					return fmt.Errorf("lock isolated official daemon cleanup: %w", err)
				}
				defer releaseCodexHostStartupLock(lifecycleLock)
				currentMetadata, err := clientA.readCodexHostMetadata(socketPath)
				if err != nil {
					return fmt.Errorf("inspect isolated official daemon before cleanup: %w", err)
				}
				if !sameCodexHostGeneration(currentMetadata, ownedMetadata) {
					return fmt.Errorf("isolated official daemon generation changed; preserving current Host")
				}
				if err := clientA.stopManagedCodexHostLocked(stopCtx, socketPath); err != nil {
					return fmt.Errorf("stop isolated official daemon: %w", err)
				}
				return nil
			},
			stopUpdater: func() error {
				updaterCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := stopCodexDaemonLiveUpdater(updaterCtx, clientA, runtimeHome); err != nil {
					return fmt.Errorf("stop verified isolated official daemon updater: %w", err)
				}
				return nil
			},
		})
		if err != nil {
			t.Errorf("clean up isolated official daemon runtime: %v", err)
			return
		}
		runtimeHome.cleanupSafe = true
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := clientA.prepareCodexHostSocket(socketPath); err != nil {
		t.Fatalf("prepare isolated official daemon socket: %v", err)
	}
	lifecycleLock, err := clientA.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		t.Fatalf("lock isolated official daemon startup: %v", err)
	}
	runtimeHome.cleanupSafe = false
	output, startErr := clientA.runAndValidateCodexDaemonLifecycle(ctx, "start", socketPath)
	if startErr != nil {
		releaseCodexHostStartupLock(lifecycleLock)
		t.Fatalf("start isolated official daemon: %v", startErr)
	}
	if output.Status != "started" {
		releaseCodexHostStartupLock(lifecycleLock)
		t.Fatalf("isolated official daemon start status=%q, want started", output.Status)
	}
	ownedMetadata, err = clientA.recordCodexDaemonMetadata(ctx, output, socketPath)
	releaseCodexHostStartupLock(lifecycleLock)
	if err != nil {
		t.Fatalf("record isolated official daemon ownership: %v", err)
	}
	daemonOwned = true
	if err := clientA.Start(ctx); err != nil {
		t.Fatalf("start official daemon client A: %v", err)
	}
	if err := clientB.Start(ctx); err != nil {
		t.Fatalf("start official daemon client B: %v", err)
	}

	threadID := startCodexDaemonLiveThread(t, ctx, clientA, workspace)

	ownerA := make(chan *codexTurnEvent, codexTurnEventBufferSize)
	if !clientA.registerTurnChannel(threadID, ownerA) {
		t.Fatalf("register client A active turn owner for %s", threadID)
	}
	defer clientA.unregisterTurnChannel(threadID, ownerA)
	eventsA := make(chan *codexTurnEvent, codexTurnEventBufferSize)
	eventsB := make(chan *codexTurnEvent, codexTurnEventBufferSize)
	observerA := clientA.registerTurnObserver(threadID, eventsA)
	observerB := clientB.registerTurnObserver(threadID, eventsB)
	defer clientA.unregisterTurnObserver(threadID, observerA, eventsA)
	defer clientB.unregisterTurnObserver(threadID, observerB, eventsB)

	turnID := startCodexDaemonLiveTurn(t, ctx, clientA, codexTurnStartParams{
		ThreadID:       threadID,
		ApprovalPolicy: "untrusted",
		Input: []codexUserInput{{
			Type: "text",
			Text: "Run the single command supplied by the local protocol fixture, then return its requested final marker.",
		}},
		SandboxPolicy: map[string]interface{}{"type": "readOnly"},
		Cwd:           workspace,
	})
	waitForCodexDaemonLiveEvent(t, eventsA, turnID, func(event *codexTurnEvent) bool {
		return event.Kind == "started"
	}, "client A turn/started")
	approvalA := waitForCodexDaemonLiveApproval(t, eventsA, turnID, "client A")
	denyDecision := defaultDenyDecision(approvalA.Approval.Request.Options)
	allowDecision := selectApprovalOption(approvalA.Approval.Request.Options, "allow")
	approvalSettled := false
	defer func() {
		if approvalSettled {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		decisionCtx := ContextWithApprovalHandler(cleanupCtx, func(context.Context, ApprovalRequest) (string, error) {
			return denyDecision, nil
		})
		if err := clientA.handleCodexApprovalEvent(decisionCtx, approvalA); err != nil {
			t.Errorf("decline live approval during cleanup: %v", err)
		}
	}()
	if err := validateCodexDaemonLiveApproval(approvalA, approvalCommand, workspace); err != nil {
		t.Fatal(err)
	}
	if !isApprovalOption(approvalA.Approval.Request.Options, denyDecision) ||
		approvalKindFromDecision(denyDecision) != "deny" {
		t.Fatalf("client A approval options=%#v, want an explicit deny decision", approvalA.Approval.Request.Options)
	}
	if !isApprovalOption(approvalA.Approval.Request.Options, allowDecision) ||
		approvalKindFromDecision(allowDecision) != "allow" {
		t.Fatalf("client A approval options=%#v, want an explicit allow decision", approvalA.Approval.Request.Options)
	}

	resumeCodexDaemonLiveThread(t, ctx, clientB, threadID, workspace)
	approvalB := waitForCodexDaemonLiveApproval(t, eventsB, turnID, "client B")
	if approvalB.Approval.Request.RequestID != approvalA.Approval.Request.RequestID {
		t.Fatalf(
			"approval replay request id=%q, want %q",
			approvalB.Approval.Request.RequestID,
			approvalA.Approval.Request.RequestID,
		)
	}
	if err := clientB.SteerCodexThread(
		ctx, "client-b", threadID, turnID,
		"Include the exact marker "+steerMarker+" in this turn's final answer.",
	); err != nil {
		t.Fatalf("steer active turn from client B: %v", err)
	}

	decisionCtx := ContextWithApprovalHandler(ctx, func(context.Context, ApprovalRequest) (string, error) {
		return allowDecision, nil
	})
	if err := clientA.handleCodexApprovalEvent(decisionCtx, approvalA); err != nil {
		t.Fatalf("accept approval from client A: %v", err)
	}
	approvalSettled = true
	select {
	case <-approvalB.Approval.Request.Resolution.Done():
	case <-time.After(protocolLiveEventTimeout):
		t.Fatal("client B did not receive serverRequest/resolved after client A answered")
	}
	if err := approvalB.Approval.Request.Resolution.Err(); !errors.Is(err, ErrCodexInteractionResolvedExternally) {
		t.Fatalf("client B resolution error=%v, want external resolution", err)
	}
	assertCodexDaemonLiveUniqueCompletion(t, eventsA, turnID, "client A")
	assertCodexDaemonLiveUniqueCompletion(t, eventsB, turnID, "client B")
	if info, err := os.Stat(approvalTarget); err != nil || !info.IsDir() {
		t.Fatalf("accepted command did not create the isolated marker directory: info=%v err=%v", info, err)
	}
	modelServer.assertRequests(t, approvalCallID, steerMarker)

	history, err := clientB.rpc(ctx, "thread/read", map[string]interface{}{
		"threadId": threadID, "includeTurns": true,
	})
	if err != nil {
		t.Fatalf("thread/read after cross-client completion: %v", err)
	}
	if !strings.Contains(string(history), finalAnswer) {
		t.Fatalf("thread history does not contain final answer marker: %s", history)
	}
}

type codexDaemonLiveModelServer struct {
	server    *httptest.Server
	mu        sync.Mutex
	requests  [][]byte
	responses []string
}

func newCodexDaemonLiveModelServer(
	t *testing.T,
	workspace string,
	approvalCommand string,
	approvalCallID string,
	finalAnswer string,
) *codexDaemonLiveModelServer {
	t.Helper()
	arguments, err := json.Marshal(map[string]interface{}{
		"command":    approvalCommand,
		"workdir":    workspace,
		"timeout_ms": 5000,
	})
	if err != nil {
		t.Fatalf("marshal live approval tool arguments: %v", err)
	}
	model := &codexDaemonLiveModelServer{
		responses: []string{
			codexDaemonLiveSSE(t,
				map[string]interface{}{
					"type":     "response.created",
					"response": map[string]interface{}{"id": "weclaw-live-tool-response"},
				},
				map[string]interface{}{
					"type": "response.output_item.done",
					"item": map[string]interface{}{
						"type": "function_call", "call_id": approvalCallID,
						"name": "shell_command", "arguments": string(arguments),
					},
				},
				codexDaemonLiveCompletedEvent("weclaw-live-tool-response"),
			),
			codexDaemonLiveSSE(t,
				map[string]interface{}{
					"type":     "response.created",
					"response": map[string]interface{}{"id": "weclaw-live-final-response"},
				},
				map[string]interface{}{
					"type": "response.output_item.done",
					"item": map[string]interface{}{
						"type": "message", "role": "assistant", "id": "weclaw-live-final-message",
						"content": []map[string]interface{}{{"type": "output_text", "text": finalAnswer}},
					},
				},
				codexDaemonLiveCompletedEvent("weclaw-live-final-response"),
			),
		},
	}
	model.server = httptest.NewServer(http.HandlerFunc(model.serveHTTP))
	t.Cleanup(model.server.Close)
	return model
}

func (s *codexDaemonLiveModelServer) URL() string {
	return s.server.URL
}

func (s *codexDaemonLiveModelServer) serveHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet && req.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
		return
	}
	if req.Method != http.MethodPost || req.URL.Path != "/v1/responses" {
		http.NotFound(w, req)
		return
	}
	if encoding := strings.TrimSpace(req.Header.Get("Content-Encoding")); encoding != "" {
		http.Error(w, "compressed live protocol requests are not supported", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read request body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	requestIndex := len(s.requests)
	s.requests = append(s.requests, append([]byte(nil), body...))
	if requestIndex >= len(s.responses) {
		s.mu.Unlock()
		http.Error(w, "unexpected extra model request", http.StatusInternalServerError)
		return
	}
	response := s.responses[requestIndex]
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, response)
}

func (s *codexDaemonLiveModelServer) assertRequests(t *testing.T, callID string, steerMarker string) {
	t.Helper()
	s.mu.Lock()
	requests := make([][]byte, len(s.requests))
	for index := range s.requests {
		requests[index] = append([]byte(nil), s.requests[index]...)
	}
	s.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("local Responses fixture received %d request(s), want 2", len(requests))
	}
	var second struct {
		Input []map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(requests[1], &second); err != nil {
		t.Fatalf("parse second local Responses request: %v", err)
	}
	var sawCall, sawOutput bool
	for _, item := range second.Input {
		if item["call_id"] != callID {
			continue
		}
		switch item["type"] {
		case "function_call":
			sawCall = true
		case "function_call_output":
			sawOutput = true
		}
	}
	if !sawCall || !sawOutput {
		t.Fatalf("second Responses request lost tool call pairing: call=%t output=%t", sawCall, sawOutput)
	}
	var payload interface{}
	if err := json.Unmarshal(requests[1], &payload); err != nil {
		t.Fatalf("parse second Responses request for steer marker: %v", err)
	}
	if !codexDaemonLiveJSONContains(payload, steerMarker) {
		t.Fatalf("second Responses request does not contain cross-client steer marker %q", steerMarker)
	}
}

func codexDaemonLiveJSONContains(value interface{}, target string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, target)
	case []interface{}:
		for _, item := range typed {
			if codexDaemonLiveJSONContains(item, target) {
				return true
			}
		}
	case map[string]interface{}:
		for _, item := range typed {
			if codexDaemonLiveJSONContains(item, target) {
				return true
			}
		}
	}
	return false
}

func codexDaemonLiveCompletedEvent(responseID string) map[string]interface{} {
	return map[string]interface{}{
		"type": "response.completed",
		"response": map[string]interface{}{
			"id": responseID,
			"usage": map[string]interface{}{
				"input_tokens": 0, "input_tokens_details": nil,
				"output_tokens": 0, "output_tokens_details": nil, "total_tokens": 0,
			},
		},
	}
}

func codexDaemonLiveSSE(t *testing.T, events ...map[string]interface{}) string {
	t.Helper()
	var body strings.Builder
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal live Responses event: %v", err)
		}
		kind, _ := event["type"].(string)
		fmt.Fprintf(&body, "event: %s\ndata: %s\n\n", kind, data)
	}
	return body.String()
}

func installCodexDaemonLiveModelConfig(t *testing.T, codexHome string, serverURL string) {
	t.Helper()
	configPath := filepath.Join(codexHome, "config.toml")
	if _, err := os.Lstat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("isolated Codex home must not contain config.toml: path=%s err=%v", configPath, err)
	}
	config := []byte(fmt.Sprintf(`model = "mock-model"
model_provider = "weclaw_live"
approval_policy = "untrusted"
sandbox_mode = "read-only"

[model_providers.weclaw_live]
name = "WeClaw live protocol fixture"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
`, strings.TrimRight(serverURL, "/")+"/v1"))
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatalf("write isolated Codex model config: %v", err)
	}
}

type codexDaemonLiveRuntimeHome struct {
	path             string
	userHome         string
	codexHome        string
	sqliteHome       string
	dailyEntrypoints []codexDaemonLiveEntrypointSnapshot
	cleanupSafe      bool
}

func prepareCodexDaemonLiveRuntimeHome(t *testing.T, preparedHome string) *codexDaemonLiveRuntimeHome {
	t.Helper()
	dailyEntrypoints, err := snapshotCodexDaemonLiveEntrypoints()
	if err != nil {
		t.Fatalf("snapshot daily Codex commands before live gate: %v", err)
	}
	home, err := createCodexDaemonLiveRuntimeRoot()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &codexDaemonLiveRuntimeHome{
		path:             home,
		userHome:         filepath.Join(home, "user-home"),
		codexHome:        filepath.Join(home, "codex-home"),
		sqliteHome:       filepath.Join(home, "sqlite-home"),
		dailyEntrypoints: dailyEntrypoints,
		cleanupSafe:      true,
	}
	t.Cleanup(func() {
		var cleanupErrors []error
		if !runtime.cleanupSafe {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("daemon stop was not confirmed"))
		}
		if err := verifyCodexDaemonLiveCleanup(runtime); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if err := errors.Join(cleanupErrors...); err != nil {
			t.Errorf("preserving isolated Codex runtime home because cleanup isolation was not proven: %s: %v", runtime.path, err)
			return
		}
		if err := os.RemoveAll(runtime.path); err != nil {
			t.Errorf("remove isolated Codex runtime home: %v", err)
		}
	})
	for _, directory := range []string{runtime.userHome, runtime.codexHome, runtime.sqliteHome} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create isolated Codex directory %s: %v", directory, err)
		}
	}
	if err := copyCodexDaemonLiveStandalonePackage(preparedHome, runtime.codexHome); err != nil {
		t.Fatalf("copy isolated standalone package: %v", err)
	}
	aliasDir := filepath.Join(runtime.userHome, ".local", "bin")
	if err := os.MkdirAll(aliasDir, 0o700); err != nil {
		t.Fatalf("create isolated Codex PATH alias directory: %v", err)
	}
	aliasPath := filepath.Join(aliasDir, "codex")
	managedBinary := filepath.Join(runtime.codexHome, "packages", "standalone", "current", "bin", "codex")
	relativeTarget, err := filepath.Rel(aliasDir, managedBinary)
	if err != nil || filepath.IsAbs(relativeTarget) || !codexPathWithinRoot(runtime.path, managedBinary) {
		t.Fatalf("resolve isolated Codex PATH alias: target=%s err=%v", managedBinary, err)
	}
	if err := os.Symlink(relativeTarget, aliasPath); err != nil {
		t.Fatalf("create isolated Codex PATH alias: %v", err)
	}
	return runtime
}

func validateCodexDaemonLiveApproval(event *codexTurnEvent, command string, workspace string) error {
	if event == nil || event.Approval == nil {
		return fmt.Errorf("live protocol event has no approval request")
	}
	var tool struct {
		Cmd     permissionCommand `json:"cmd"`
		Command permissionCommand `json:"command"`
		Cwd     string            `json:"cwd"`
	}
	if err := json.Unmarshal(event.Approval.Request.ToolCall, &tool); err != nil {
		return fmt.Errorf("parse live approval tool call: %w", err)
	}
	actualCommand := strings.Join(tool.Cmd, " ")
	if actualCommand == "" {
		actualCommand = strings.Join(tool.Command, " ")
	}
	if !isExactCodexDaemonLiveApprovalCommand(actualCommand, command) {
		return fmt.Errorf("live approval command=%q, want %q", actualCommand, command)
	}
	if filepath.Clean(tool.Cwd) != filepath.Clean(workspace) {
		return fmt.Errorf("live approval cwd=%q, want %q", tool.Cwd, workspace)
	}
	return nil
}

func isExactCodexDaemonLiveApprovalCommand(actual string, command string) bool {
	if actual == command {
		return true
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return false
	}
	expected := shell + " -lc " + shellQuoteCodexDaemonLiveCommand(command)
	return actual == expected
}

func shellQuoteCodexDaemonLiveCommand(command string) string {
	return "'" + strings.ReplaceAll(command, "'", "'\"'\"'") + "'"
}

func requireIsolatedCodexDaemonHome(t *testing.T) string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(testCodexDaemonHomeEnv))
	if raw == "" {
		t.Fatalf("%s must point to a prepared isolated Codex home", testCodexDaemonHomeEnv)
	}
	codexHome, err := filepath.Abs(raw)
	if err != nil {
		t.Fatalf("resolve isolated Codex home: %v", err)
	}
	info, err := os.Lstat(codexHome)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("isolated Codex home must be an existing real directory: path=%s err=%v", codexHome, err)
	}
	currentHome, err := codexauth.ResolveCodexHome(nil, "")
	if err != nil {
		t.Fatalf("resolve current Codex home: %v", err)
	}
	dailyEntrypoints, err := snapshotCodexDaemonLiveEntrypoints()
	if err != nil {
		t.Fatalf("inspect daily Codex commands before live gate: %v", err)
	}
	processCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	processes, err := snapshotCodexDaemonLiveProcesses(processCtx)
	if err != nil {
		t.Fatalf("inspect prepared Codex home processes: %v", err)
	}
	for _, dailyEntrypoint := range dailyEntrypoints {
		if err := validateCodexDaemonLivePreparedHome(codexHome, currentHome, dailyEntrypoint, processes); err != nil {
			t.Fatalf("%s is not a clean isolated Codex home: %v", testCodexDaemonHomeEnv, err)
		}
	}
	return codexHome
}

func sameCodexDaemonLivePath(first string, second string) bool {
	firstPath, firstErr := filepath.EvalSymlinks(filepath.Clean(first))
	secondPath, secondErr := filepath.EvalSymlinks(filepath.Clean(second))
	if firstErr == nil && secondErr == nil {
		return firstPath == secondPath
	}
	return filepath.Clean(first) == filepath.Clean(second)
}

func newCodexDaemonLiveClient(
	command string,
	runtime *codexDaemonLiveRuntimeHome,
	workspace string,
	stateFile string,
) *ACPAgent {
	packageRoot := filepath.Join(runtime.codexHome, "packages", "standalone", "current")
	return newACPAgent(ACPAgentConfig{
		ConfiguredName:     "codex-live-protocol",
		Command:            command,
		Args:               []string{"app-server"},
		Cwd:                workspace,
		Env:                codexDaemonLiveEnvironment(runtime.userHome, runtime.codexHome, runtime.sqliteHome, packageRoot),
		StateFile:          stateFile,
		CodexHostMode:      codexHostModeDaemon,
		CodexAutoUpdate:    "off",
		ApprovalPolicy:     "untrusted",
		SandboxMode:        "read-only",
		CodexDesktopBridge: false,
	}, acpAgentOptions{allowCodexLiveTestPaths: true})
}

func startCodexDaemonLiveThread(t *testing.T, ctx context.Context, client *ACPAgent, workspace string) string {
	t.Helper()
	result, err := client.rpc(ctx, "thread/start", map[string]interface{}{
		"cwd":            workspace,
		"ephemeral":      false,
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
		"developerInstructions": "This is an isolated WeClaw protocol conformance test. " +
			"Follow the user's exact harmless command request and never modify files unless the user explicitly asks.",
	})
	if err != nil {
		t.Fatalf("thread/start on client A: %v", err)
	}
	threadID, err := codexThreadIDFromStartResult(result)
	if err != nil {
		t.Fatalf("parse live thread/start response: %v", err)
	}
	return threadID
}

func resumeCodexDaemonLiveThread(t *testing.T, ctx context.Context, client *ACPAgent, threadID string, workspace string) {
	t.Helper()
	result, err := client.rpc(ctx, "thread/resume", map[string]interface{}{
		"threadId":       threadID,
		"cwd":            workspace,
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
	})
	if err != nil {
		t.Fatalf("thread/resume on client B: %v", err)
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil || response.Thread.ID != threadID {
		t.Fatalf("thread/resume identity mismatch: response=%s err=%v", result, err)
	}
}

func startCodexDaemonLiveTurn(t *testing.T, ctx context.Context, client *ACPAgent, params codexTurnStartParams) string {
	t.Helper()
	result, err := client.rpc(ctx, "turn/start", params)
	if err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	turnID := codexTurnIDFromStartResult(result)
	if turnID == "" {
		t.Fatalf("turn/start response has no turn id: %s", result)
	}
	return turnID
}

func waitForCodexDaemonLiveApproval(t *testing.T, events <-chan *codexTurnEvent, turnID string, client string) *codexTurnEvent {
	t.Helper()
	return waitForCodexDaemonLiveEvent(t, events, turnID, func(event *codexTurnEvent) bool {
		return event.Approval != nil
	}, client+" approval request")
}

func waitForCodexDaemonLiveEvent(
	t *testing.T,
	events <-chan *codexTurnEvent,
	turnID string,
	match func(*codexTurnEvent) bool,
	label string,
) *codexTurnEvent {
	t.Helper()
	timer := time.NewTimer(protocolLiveEventTimeout)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event != nil && event.TurnID == turnID && match(event) {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s on turn %s", label, turnID)
		}
	}
}

func assertCodexDaemonLiveUniqueCompletion(t *testing.T, events <-chan *codexTurnEvent, turnID string, client string) {
	t.Helper()
	waitForCodexDaemonLiveEvent(t, events, turnID, func(event *codexTurnEvent) bool {
		switch event.Kind {
		case "completed":
			return true
		case "interrupted", "error":
			t.Fatalf("%s observed terminal kind=%s text=%q", client, event.Kind, event.Text)
		}
		return false
	}, client+" turn/completed")

	grace := time.NewTimer(500 * time.Millisecond)
	defer grace.Stop()
	for {
		select {
		case event := <-events:
			if event != nil && event.TurnID == turnID && isCodexTurnTerminalEvent(event) {
				t.Fatalf("%s observed duplicate terminal event %#v", client, event)
			}
		case <-grace.C:
			return
		}
	}
}
