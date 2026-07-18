package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testCodexUnixHostSocketEnv = "WECLAW_TEST_CODEX_UNIX_HOST_SOCKET"
	testCodexUnixHostCountEnv  = "WECLAW_TEST_CODEX_UNIX_HOST_COUNT"
)

func TestCodexSharedHostArgsReplacesConfiguredTransport(t *testing.T) {
	got := codexSharedHostArgs(
		[]string{"--config", "profile=test", "app-server", "--listen", "stdio://", "--analytics-default-enabled=false"},
		"/tmp/weclaw.sock",
	)
	want := []string{"--config", "profile=test", "app-server", "--analytics-default-enabled=false", "--listen", "unix:///tmp/weclaw.sock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codexSharedHostArgs()=%q, want %q", got, want)
	}
}

func TestResolveCodexHostSocketFallsBackForLongDefaultPath(t *testing.T) {
	t.Setenv("WECLAW_HOME", filepath.Join(t.TempDir(), strings.Repeat("long-home-", 16)))
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})

	got, err := a.resolveCodexHostSocket()
	if err != nil {
		t.Fatalf("resolveCodexHostSocket() error=%v", err)
	}
	wantParent := filepath.Join(string(filepath.Separator), "tmp", "weclaw-"+strconv.Itoa(os.Geteuid()))
	if filepath.Dir(got) != wantParent || len([]byte(got)) > codexHostSocketMaxBytes {
		t.Fatalf("socket=%q parent=%q, want short path under %q", got, filepath.Dir(got), wantParent)
	}
	if again, err := a.resolveCodexHostSocket(); err != nil || again != got {
		t.Fatalf("second resolve=(%q,%v), want deterministic %q", again, err, got)
	}
}

func TestResolveCodexHostSocketRejectsLongExplicitPath(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		AppServerSocket: filepath.Join(t.TempDir(), strings.Repeat("x", codexHostSocketMaxBytes)),
	})
	if _, err := a.resolveCodexHostSocket(); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("resolveCodexHostSocket() error=%v, want explicit path length rejection", err)
	}
}

func TestACPAgentMultipleClientsShareExistingCodexHost(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "weclaw-codex-host-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "app-server.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	server := newFakeCodexHost(listener)
	server.start(t)

	newClient := func(stateName string) *ACPAgent {
		return NewACPAgent(ACPAgentConfig{
			Command:         "codex",
			Args:            []string{"app-server", "--listen", "stdio://"},
			AppServerSocket: socketPath,
			StateFile:       filepath.Join(dir, stateName+".json"),
		})
	}
	first := newClient("first")
	second := newClient("second")
	t.Cleanup(first.Stop)
	t.Cleanup(second.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := first.Start(ctx); err != nil {
		t.Fatalf("first Start() error=%v", err)
	}
	if err := second.Start(ctx); err != nil {
		t.Fatalf("second Start() error=%v", err)
	}

	startResult, err := first.rpc(ctx, "thread/start", map[string]interface{}{"cwd": "/workspace"})
	if err != nil {
		t.Fatalf("thread/start error=%v", err)
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(startResult, &started); err != nil {
		t.Fatal(err)
	}
	readResult, err := second.rpc(ctx, "thread/read", map[string]string{"threadId": started.Thread.ID})
	if err != nil {
		t.Fatalf("thread/read error=%v", err)
	}
	var read struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(readResult, &read); err != nil {
		t.Fatal(err)
	}
	if read.Thread.ID != "thread-shared" {
		t.Fatalf("shared thread id=%q, want thread-shared", read.Thread.ID)
	}
	if got := server.connectionCount(); got != 2 {
		t.Fatalf("host connection count=%d, want 2", got)
	}
	if first.runtimePID() != 0 || second.runtimePID() != 0 {
		t.Fatal("clients attached to an existing host must not claim process ownership")
	}
}

func TestACPAgentConcurrentClientsStartOneCodexHost(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "weclaw-codex-race-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "app-server.sock")
	countPath := filepath.Join(dir, "starts.log")
	commandPath := filepath.Join(dir, "codex")
	script := `#!/bin/sh
socket=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--listen" ]; then
    shift
    socket="${1#unix://}"
  fi
  shift
done
WECLAW_TEST_CODEX_UNIX_HOST_SOCKET="$socket" exec "$WECLAW_TEST_CODEX_BINARY" -test.run='^TestHelperCodexUnixHost$'
`
	if err := os.WriteFile(commandPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	newClient := func(stateName string) *ACPAgent {
		return NewACPAgent(ACPAgentConfig{
			Command:         commandPath,
			Args:            []string{"app-server"},
			AppServerSocket: socketPath,
			StateFile:       filepath.Join(dir, stateName+".json"),
			Env: map[string]string{
				"WECLAW_TEST_CODEX_BINARY": os.Args[0],
				testCodexUnixHostCountEnv:  countPath,
			},
		})
	}
	first := newClient("first")
	second := newClient("second")
	t.Cleanup(first.Stop)
	t.Cleanup(second.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- first.Start(ctx) }()
	go func() { errs <- second.Start(ctx) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Start() error=%v", err)
		}
	}

	data, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if starts := len(strings.Fields(string(data))); starts != 1 {
		t.Fatalf("host starts=%d, want 1; log=%q", starts, data)
	}
	owned := 0
	if first.runtimePID() != 0 {
		owned++
	}
	if second.runtimePID() != 0 {
		owned++
	}
	if owned != 1 {
		t.Fatalf("clients owning host=%d, want exactly 1", owned)
	}
}

func TestACPAgentRejectsNonSocketCodexHostPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command:         "codex",
		Args:            []string{"app-server"},
		AppServerSocket: path,
		StateFile:       filepath.Join(dir, "state.json"),
	})

	err := a.Start(context.Background())
	if err == nil {
		t.Fatal("Start() error=nil, want non-socket rejection")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "do not replace" {
		t.Fatalf("non-socket path was modified: data=%q err=%v", data, readErr)
	}
}

func TestACPAgentRejectsSymlinkCodexHostParent(t *testing.T) {
	realParent := t.TempDir()
	linkRoot := t.TempDir()
	linkedParent := filepath.Join(linkRoot, "runtime")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	err := a.prepareCodexHostSocket(filepath.Join(linkedParent, "codex.sock"))
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("prepareCodexHostSocket() error=%v, want symlink parent rejection", err)
	}
}

func TestACPAgentRejectsInsecureCodexHostDirectory(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	err := a.prepareCodexHostSocket(filepath.Join(parent, "codex.sock"))
	if err == nil || !strings.Contains(err.Error(), "group or others") {
		t.Fatalf("prepareCodexHostSocket() error=%v, want directory permission rejection", err)
	}
}

func TestCodexHostStartupLockSerializesClients(t *testing.T) {
	parent := t.TempDir()
	socketPath := filepath.Join(parent, "codex.sock")
	firstAgent := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	secondAgent := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})

	first, err := firstAgent.acquireCodexHostStartupLock(context.Background(), socketPath)
	if err != nil {
		t.Fatalf("first startup lock error=%v", err)
	}
	defer releaseCodexHostStartupLock(first)

	secondAcquired := make(chan *os.File, 1)
	secondErr := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		lockFile, err := secondAgent.acquireCodexHostStartupLock(ctx, socketPath)
		if err != nil {
			secondErr <- err
			return
		}
		secondAcquired <- lockFile
	}()

	select {
	case lockFile := <-secondAcquired:
		releaseCodexHostStartupLock(lockFile)
		t.Fatal("second client acquired startup lock before first released it")
	case err := <-secondErr:
		t.Fatalf("second startup lock failed early: %v", err)
	case <-time.After(75 * time.Millisecond):
	}

	releaseCodexHostStartupLock(first)
	first = nil
	select {
	case lockFile := <-secondAcquired:
		releaseCodexHostStartupLock(lockFile)
	case err := <-secondErr:
		t.Fatalf("second startup lock error=%v", err)
	case <-ctx.Done():
		t.Fatal("second client did not acquire startup lock after release")
	}
}

func TestCodexHostStartupLockRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	socketPath := filepath.Join(parent, "codex.sock")
	target := filepath.Join(parent, "target")
	if err := os.WriteFile(target, []byte("do not lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, socketPath+".lock"); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})

	lockFile, err := a.acquireCodexHostStartupLock(context.Background(), socketPath)
	releaseCodexHostStartupLock(lockFile)
	if err == nil {
		t.Fatal("startup lock error=nil, want symlink rejection")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "do not lock" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, readErr)
	}
}

func TestHelperCodexUnixHost(t *testing.T) {
	socketPath := os.Getenv(testCodexUnixHostSocketEnv)
	if socketPath == "" {
		return
	}
	countPath := os.Getenv(testCodexUnixHostCountEnv)
	countFile, err := os.OpenFile(countPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := countFile.WriteString("start\n"); err != nil {
		_ = countFile.Close()
		t.Fatal(err)
	}
	if err := countFile.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := newFakeCodexHost(listener)
	server.start(t)
	select {}
}

type fakeCodexHost struct {
	listener net.Listener
	mu       sync.Mutex
	accepted int
	threads  map[string]bool
}

func newFakeCodexHost(listener net.Listener) *fakeCodexHost {
	return &fakeCodexHost{listener: listener, threads: make(map[string]bool)}
}

func (s *fakeCodexHost) start(t *testing.T) {
	t.Helper()
	go func() {
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.accepted++
			s.mu.Unlock()
			go s.serve(conn)
		}
	}()
}

func (s *fakeCodexHost) connectionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

func (s *fakeCodexHost) serve(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	encoder := json.NewEncoder(conn)
	for scanner.Scan() {
		var req struct {
			ID     *int64          `json:"id,omitempty"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		result := map[string]interface{}{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{"serverInfo": map[string]string{"name": "fake-codex-host"}}
		case "thread/start":
			s.mu.Lock()
			s.threads["thread-shared"] = true
			s.mu.Unlock()
			result = map[string]interface{}{"thread": map[string]string{"id": "thread-shared"}}
		case "thread/read":
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(req.Params, &params)
			s.mu.Lock()
			exists := s.threads[params.ThreadID]
			s.mu.Unlock()
			if exists {
				result = map[string]interface{}{"thread": map[string]string{"id": params.ThreadID}}
			}
		}
		_ = encoder.Encode(map[string]interface{}{"id": *req.ID, "result": result})
	}
}
