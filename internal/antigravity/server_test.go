package antigravity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeService struct {
	mu            sync.Mutex
	conversations []UpstreamConversation
	created       []CreateConversationRequest
	getErr        error
}

func (f *fakeService) ListConversations(context.Context) ([]UpstreamConversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]UpstreamConversation(nil), f.conversations...), nil
}

func (f *fakeService) GetConversation(_ context.Context, id string) (UpstreamConversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return UpstreamConversation{}, f.getErr
	}
	for _, conversation := range f.conversations {
		if conversation.ID == id {
			return conversation, nil
		}
	}
	return UpstreamConversation{ID: id, Title: "Remote", RunState: "RUN_STATE_IDLE"}, nil
}

func (f *fakeService) CreateConversation(_ context.Context, req CreateConversationRequest) (UpstreamConversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req)
	return UpstreamConversation{ID: "new-conversation", Title: req.Title, ProjectID: req.ProjectID, RunState: "RUN_STATE_RUNNING"}, nil
}

type fakeRunner struct {
	mu       sync.Mutex
	messages []string
	err      error
}

func (f *fakeRunner) SendMessage(_ context.Context, id string, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.messages = append(f.messages, id+"\x00"+content)
	return nil
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func startTestServer(t *testing.T, opts ServerOptions) (*Server, *Client, context.CancelFunc, <-chan error) {
	t.Helper()
	if opts.SocketPath == "" {
		opts.SocketPath = shortSocketPath(t)
	}
	server, err := NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	client, err := NewClient(opts.SocketPath)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, healthErr := client.Health(probeCtx)
		probeCancel()
		if healthErr == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("sidecar did not become healthy: %v", healthErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return server, client, cancel, done
}

func TestSidecarSocketPermissionsAndGracefulCleanup(t *testing.T) {
	socketPath := shortSocketPath(t)
	_, client, cancel, done := startTestServer(t, ServerOptions{
		SocketPath: socketPath, ConversationService: &fakeService{},
	})
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %o, want 700", dirInfo.Mode().Perm())
	}
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.ProtocolVersion != ProtocolVersion || health.InstanceID == "" {
		t.Fatalf("health = %+v", health)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sidecar did not stop")
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket must be removed on shutdown: %v", err)
	}
}

func TestSidecarRejectsSecondInstance(t *testing.T) {
	socketPath := shortSocketPath(t)
	_, _, cancel, done := startTestServer(t, ServerOptions{
		SocketPath: socketPath, ConversationService: &fakeService{},
	})
	defer func() {
		cancel()
		<-done
	}()
	second, err := NewServer(ServerOptions{SocketPath: socketPath, ConversationService: &fakeService{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second instance error = %v, want ErrAlreadyRunning", err)
	}
}

func TestSidecarLockPersistsAndRejectsLiveInstance(t *testing.T) {
	socketPath := shortSocketPath(t)
	_, _, cancel, done := startTestServer(t, ServerOptions{
		SocketPath: socketPath, ConversationService: &fakeService{},
	})
	lockPath := socketPath + ".lock"
	info, err := os.Lstat(lockPath)
	if err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %o, want 600", info.Mode().Perm())
	}
	second, err := NewServer(ServerOptions{SocketPath: socketPath, ConversationService: &fakeService{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second instance error = %v, want ErrAlreadyRunning", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lockPath); err != nil {
		t.Fatalf("lock file must persist after shutdown: %v", err)
	}
	_, client, cancelAgain, doneAgain := startTestServer(t, ServerOptions{
		SocketPath: socketPath, ConversationService: &fakeService{},
	})
	defer func() {
		cancelAgain()
		<-doneAgain
	}()
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("restart after lock release failed: %v", err)
	}
}

func TestSidecarDoesNotRemoveReplacedSocketOnExit(t *testing.T) {
	socketPath := shortSocketPath(t)
	_, _, cancel, done := startTestServer(t, ServerOptions{
		SocketPath: socketPath, ConversationService: &fakeService{},
	})
	replacement := filepath.Join(filepath.Dir(socketPath), "replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, socketPath); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatalf("replacement path was removed: %v", err)
	}
	if string(data) != "replacement" {
		t.Fatalf("replacement content = %q", data)
	}
}

func TestSidecarRejectsSymlinkSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socketPath := filepath.Join(dir, "s.sock")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, socketPath); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{SocketPath: socketPath, ConversationService: &fakeService{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Run(context.Background()); !errors.Is(err, ErrUnsafeSocket) {
		t.Fatalf("symlink error = %v, want ErrUnsafeSocket", err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("symlink must be left untouched: %v", err)
	}
}

func TestSidecarReplacesStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socketPath := filepath.Join(dir, "s.sock")
	address, err := net.ResolveUnixAddr("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("stale socket file missing: %v", err)
	}
	_, client, cancel, done := startTestServer(t, ServerOptions{
		SocketPath: socketPath, ConversationService: &fakeService{},
	})
	defer func() {
		cancel()
		<-done
	}()
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSidecarConversationEndpoints(t *testing.T) {
	appData := t.TempDir()
	projectsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectsDir, "p1.json"), []byte(`{"name":"Alpha"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conversationID := "conv-1"
	transcript, err := TranscriptPath(appData, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{\"type\":\"PLANNER_RESPONSE\",\"content\":\"previous\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{conversations: []UpstreamConversation{
		{ID: "conv-1", Title: "First", ProjectID: "p1", RunState: "RUN_STATE_RUNNING"},
		{ID: "conv-1", Title: "Duplicate", ProjectID: "p1"},
		{ID: "archived", Title: "Archived", ProjectID: "p1", Archived: true},
		{ID: "child", Title: "Child", ProjectID: "p1", ParentConversationID: "conv-1"},
	}}
	runner := &fakeRunner{}
	_, client, cancel, done := startTestServer(t, ServerOptions{
		AppDataDir: appData, ProjectsDir: projectsDir, ConversationService: service, Runner: runner,
	})
	defer func() {
		cancel()
		<-done
	}()

	conversations, err := client.ListConversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 1 || conversations[0].ID != "conv-1" || conversations[0].ProjectName != "Alpha" {
		t.Fatalf("conversations = %+v", conversations)
	}

	conversation, baseline, err := client.GetConversation(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.RunState != "RUN_STATE_RUNNING" || !baseline.HasHistory || baseline.LatestReply != "previous" || baseline.Offset == 0 {
		t.Fatalf("get = %+v baseline = %+v", conversation, baseline)
	}

	created, createdBaseline, err := client.CreateConversation(context.Background(), CreateConversationRequest{
		ProjectID: "p1", Title: "New", Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "new-conversation" || createdBaseline.Offset != 0 {
		t.Fatalf("create = %+v baseline = %+v", created, createdBaseline)
	}
	if len(service.created) != 1 || service.created[0].Prompt != "hello" {
		t.Fatalf("created requests = %+v", service.created)
	}

	if err := client.SendMessage(context.Background(), conversationID, "[WeClaw/飞书] hi"); err != nil {
		t.Fatal(err)
	}
	if len(runner.messages) != 1 || runner.messages[0] != conversationID+"\x00[WeClaw/飞书] hi" {
		t.Fatalf("runner messages = %q", runner.messages)
	}

	observed, err := client.Observe(context.Background(), conversationID, baseline.FileID, baseline.Offset)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Discontinuous || len(observed.Events) != 0 {
		t.Fatalf("observe at baseline = %+v", observed)
	}
}

func TestSidecarRejectsOversizedBody(t *testing.T) {
	_, client, cancel, done := startTestServer(t, ServerOptions{ConversationService: &fakeService{}})
	defer func() {
		cancel()
		<-done
	}()
	// The private protocol limits every request body to 1 MiB.
	body := strings.Repeat("x", maxRequestBodyBytes+1)
	request, err := http.NewRequest(http.MethodPost, "http://antigravity/v1/conversations", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		// The server rejects the oversized body before reading it fully and may
		// close the connection while the client is still writing.
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 400 {
		t.Fatalf("oversized body status = %d", response.StatusCode)
	}
}

func TestSidecarRejectsLongSocketPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("a", 120)+".sock")
	server, err := NewServer(ServerOptions{SocketPath: path, ConversationService: &fakeService{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("long path error = %v", err)
	}
}

func TestServerResponseCarriesEnvelope(t *testing.T) {
	service := &fakeService{conversations: []UpstreamConversation{{ID: "a", RunState: "RUN_STATE_IDLE"}}}
	server, client, cancel, done := startTestServer(t, ServerOptions{ConversationService: service})
	defer func() {
		cancel()
		<-done
	}()
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.InstanceID != server.InstanceID() {
		t.Fatalf("instance id = %q, want %q", health.InstanceID, server.InstanceID())
	}
}
