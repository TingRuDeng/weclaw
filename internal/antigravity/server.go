package antigravity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

// ServerOptions configures a sidecar instance. Zero values resolve from the
// environment and the current user's Antigravity directories.
type ServerOptions struct {
	SocketPath          string
	AppDataDir          string
	ProjectsDir         string
	ConversationService ConversationService
	Runner              AgentRunner
	InstanceID          string
}

// Server serves the private sidecar HTTP protocol over one Unix socket.
type Server struct {
	socketPath  string
	appDataDir  string
	projectsDir string
	service     ConversationService
	runner      AgentRunner
	instanceID  string

	listener      net.Listener
	httpServer    *http.Server
	createdSocket bool
	socketInfo    os.FileInfo
	socketLock    *socketLock
}

// NewServer builds a sidecar from explicit options and environment defaults.
func NewServer(opts ServerOptions) (*Server, error) {
	socketPath, err := ResolveSocketPath(opts.SocketPath)
	if err != nil {
		return nil, err
	}
	appDataDir := strings.TrimSpace(opts.AppDataDir)
	if appDataDir == "" {
		appDataDir = DefaultAppDataDir()
	}
	projectsDir := strings.TrimSpace(opts.ProjectsDir)
	if projectsDir == "" {
		projectsDir = DefaultProjectsDir()
	}
	instanceID := strings.TrimSpace(opts.InstanceID)
	if instanceID == "" {
		instanceID = newInstanceID()
	}
	return &Server{
		socketPath: socketPath, appDataDir: appDataDir, projectsDir: projectsDir,
		service: opts.ConversationService, runner: opts.Runner, instanceID: instanceID,
	}, nil
}

// SocketPath reports the resolved socket path.
func (s *Server) SocketPath() string { return s.socketPath }

// InstanceID reports this process's random sidecar identity.
func (s *Server) InstanceID() string { return s.instanceID }

// Run listens on the private socket, serves until ctx is cancelled, and removes
// the socket it created.
func (s *Server) Run(ctx context.Context) error {
	if s.service == nil {
		return fmt.Errorf("antigravity ConversationService is not configured")
	}
	if err := validateSocketPathLength(s.socketPath); err != nil {
		return err
	}
	if err := securefile.EnsureDir(filepath.Dir(s.socketPath)); err != nil {
		return fmt.Errorf("prepare antigravity socket directory: %w", err)
	}
	lock, err := acquireSocketLock(s.socketPath)
	if err != nil {
		return err
	}
	s.socketLock = lock
	defer s.releaseSocketLock()
	if err := s.prepareSocketPath(); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen antigravity socket: %w", err)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		// removeSocket owns path cleanup so a replacement at the same path is
		// never unlinked by the listener.
		unixListener.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.socketPath)
		return fmt.Errorf("chmod antigravity socket: %w", err)
	}
	info, err := os.Lstat(s.socketPath)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(s.socketPath)
		return fmt.Errorf("inspect antigravity socket after listen: %w", err)
	}
	s.listener = listener
	s.socketInfo = info
	s.createdSocket = true
	s.httpServer = &http.Server{Handler: s.routes()}
	serveDone := make(chan error, 1)
	go func() {
		err := s.httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveDone <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.httpServer.Shutdown(shutdownCtx)
		cancel()
		<-serveDone
		s.removeSocket()
		return nil
	case err := <-serveDone:
		s.removeSocket()
		return err
	}
}

func (s *Server) releaseSocketLock() {
	if s.socketLock == nil {
		return
	}
	if err := s.socketLock.Close(); err != nil {
		log.Printf("[antigravity] failed to release sidecar lock: %v", err)
	}
	s.socketLock = nil
}

func (s *Server) prepareSocketPath() error {
	info, err := os.Lstat(s.socketPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect antigravity socket: %w", err)
	}
	if err := validateSocketInfo(info); err != nil {
		return err
	}
	if err := os.Remove(s.socketPath); err != nil {
		return fmt.Errorf("remove stale antigravity socket: %w", err)
	}
	return nil
}

func (s *Server) removeSocket() {
	if !s.createdSocket || s.socketInfo == nil {
		return
	}
	current, err := os.Lstat(s.socketPath)
	if err != nil {
		return
	}
	if !os.SameFile(s.socketInfo, current) {
		return
	}
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		log.Printf("[antigravity] failed to remove sidecar socket: %v", err)
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/conversations", s.handleListConversations)
	mux.HandleFunc("GET /v1/conversations/{id}", s.handleGetConversation)
	mux.HandleFunc("POST /v1/conversations", s.handleCreateConversation)
	mux.HandleFunc("POST /v1/conversations/{id}/messages", s.handleSendMessage)
	mux.HandleFunc("POST /v1/conversations/{id}/observe", s.handleObserve)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
		mux.ServeHTTP(writer, request)
	})
}

type protocolEnvelope struct {
	ProtocolVersion int    `json:"protocol_version"`
	InstanceID      string `json:"instance_id"`
}

type healthResponse struct {
	protocolEnvelope
	Status string `json:"status"`
}

type errorResponse struct {
	protocolEnvelope
	Error string `json:"error"`
}

type conversationListResponse struct {
	protocolEnvelope
	Conversations []Conversation `json:"conversations"`
}

type conversationResponse struct {
	protocolEnvelope
	Conversation Conversation `json:"conversation"`
	Baseline     Baseline     `json:"baseline"`
}

type messageResponse struct {
	protocolEnvelope
	OK bool `json:"ok"`
}

type observeResponse struct {
	protocolEnvelope
	Conversation  Conversation `json:"conversation"`
	Events        []Event      `json:"events"`
	NextFileID    string       `json:"next_file_id"`
	NextOffset    int64        `json:"next_offset"`
	Discontinuous bool         `json:"discontinuous"`
}

func (s *Server) envelope() protocolEnvelope {
	return protocolEnvelope{ProtocolVersion: ProtocolVersion, InstanceID: s.instanceID}
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, healthResponse{protocolEnvelope: s.envelope(), Status: "ok"})
}

func (s *Server) handleListConversations(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), defaultRequestTimeout)
	defer cancel()
	upstream, err := s.service.ListConversations(ctx)
	if err != nil {
		s.writeError(writer, http.StatusBadGateway, err)
		return
	}
	seen := make(map[string]bool, len(upstream))
	conversations := make([]Conversation, 0, len(upstream))
	for _, item := range upstream {
		if item.Archived || strings.TrimSpace(item.ParentConversationID) != "" || strings.TrimSpace(item.ID) == "" {
			continue
		}
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		conversations = append(conversations, s.conversationView(item))
	}
	conversationProjectName(conversations, LoadProjects(s.projectsDir))
	writeJSON(writer, http.StatusOK, conversationListResponse{protocolEnvelope: s.envelope(), Conversations: conversations})
}

func (s *Server) handleGetConversation(writer http.ResponseWriter, request *http.Request) {
	conversationID := request.PathValue("id")
	ctx, cancel := context.WithTimeout(request.Context(), defaultRequestTimeout)
	defer cancel()
	upstream, err := s.service.GetConversation(ctx, conversationID)
	if err != nil {
		s.writeError(writer, http.StatusBadGateway, err)
		return
	}
	baseline, err := s.baselineFor(conversationID)
	if err != nil {
		s.writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, conversationResponse{
		protocolEnvelope: s.envelope(), Conversation: s.conversationView(upstream), Baseline: baseline,
	})
}

func (s *Server) handleCreateConversation(writer http.ResponseWriter, request *http.Request) {
	var body CreateConversationRequest
	if err := decodeJSONBody(request, &body); err != nil {
		s.writeError(writer, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.ProjectID) == "" {
		s.writeError(writer, http.StatusBadRequest, fmt.Errorf("project_id is required"))
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		s.writeError(writer, http.StatusBadRequest, fmt.Errorf("prompt is required"))
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), defaultRequestTimeout)
	defer cancel()
	upstream, err := s.service.CreateConversation(ctx, body)
	if err != nil {
		s.writeError(writer, http.StatusBadGateway, err)
		return
	}
	writeJSON(writer, http.StatusOK, conversationResponse{
		protocolEnvelope: s.envelope(), Conversation: s.conversationView(upstream),
		Baseline: Baseline{Offset: 0, LatestReply: ""},
	})
}

func (s *Server) handleSendMessage(writer http.ResponseWriter, request *http.Request) {
	conversationID := request.PathValue("id")
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSONBody(request, &body); err != nil {
		s.writeError(writer, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		s.writeError(writer, http.StatusBadRequest, fmt.Errorf("content is required"))
		return
	}
	if s.runner == nil {
		s.writeError(writer, http.StatusNotImplemented, fmt.Errorf("agentapi runner is not configured"))
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), defaultRequestTimeout)
	defer cancel()
	if err := s.runner.SendMessage(ctx, conversationID, body.Content); err != nil {
		s.writeError(writer, http.StatusBadGateway, err)
		return
	}
	writeJSON(writer, http.StatusOK, messageResponse{protocolEnvelope: s.envelope(), OK: true})
}

func (s *Server) handleObserve(writer http.ResponseWriter, request *http.Request) {
	conversationID := request.PathValue("id")
	var body struct {
		FileID string `json:"file_id"`
		Offset int64  `json:"offset"`
	}
	if err := decodeJSONBody(request, &body); err != nil {
		s.writeError(writer, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), defaultRequestTimeout)
	defer cancel()
	upstream, err := s.service.GetConversation(ctx, conversationID)
	if err != nil {
		s.writeError(writer, http.StatusBadGateway, err)
		return
	}
	path, err := TranscriptPath(s.appDataDir, conversationID)
	if err != nil {
		s.writeError(writer, http.StatusBadRequest, err)
		return
	}
	observed, err := ReadTranscript(path, body.FileID, body.Offset)
	if err != nil {
		s.writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, observeResponse{
		protocolEnvelope: s.envelope(), Conversation: s.conversationView(upstream),
		Events: observed.Events, NextFileID: observed.NextFileID, NextOffset: observed.NextOffset,
		Discontinuous: observed.Discontinuous,
	})
}

func (s *Server) conversationView(upstream UpstreamConversation) Conversation {
	runState := strings.TrimSpace(upstream.RunState)
	if runState == "" {
		runState = "RUN_STATE_IDLE"
	}
	return Conversation{
		ID: upstream.ID, Title: upstream.Title, ProjectID: upstream.ProjectID,
		ProjectName:          ProjectName(LoadProjects(s.projectsDir), upstream.ProjectID),
		ParentConversationID: upstream.ParentConversationID, Archived: upstream.Archived, RunState: runState,
	}
}

func (s *Server) baselineFor(conversationID string) (Baseline, error) {
	path, err := TranscriptPath(s.appDataDir, conversationID)
	if err != nil {
		return Baseline{}, err
	}
	return ReadBaseline(path)
}

func (s *Server) writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, errorResponse{protocolEnvelope: s.envelope(), Error: err.Error()})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func decodeJSONBody(request *http.Request, target any) error {
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(payload) > maxRequestBodyBytes {
		return fmt.Errorf("request body exceeds 1 MiB")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	return nil
}

func validateSocketPathLength(path string) error {
	if len(path) >= 104 {
		return fmt.Errorf("antigravity socket path is too long for a Unix socket")
	}
	return nil
}

func validateSocketInfo(info os.FileInfo) error {
	if info == nil {
		return ErrUnsafeSocket
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink", ErrUnsafeSocket)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: not a socket", ErrUnsafeSocket)
	}
	return validateSocketOwner(info)
}

func newInstanceID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("instance-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(random)
}
