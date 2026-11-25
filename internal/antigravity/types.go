// Package antigravity implements the WeClaw sidecar for the Antigravity desktop
// application and the private Unix-socket client consumed by messaging.
package antigravity

import (
	"context"
	"errors"
	"time"
)

// ProtocolVersion is the only private sidecar protocol this build speaks.
const ProtocolVersion = 1

// Default request deadlines keep every sidecar call bounded.
const (
	defaultRequestTimeout = 10 * time.Second
	maxRequestBodyBytes   = 1 << 20
	maxTranscriptChunk    = 4 << 20
)

// ErrAlreadyRunning reports that another live sidecar owns the socket.
var ErrAlreadyRunning = errors.New("antigravity sidecar is already running")

// ErrUnsafeSocket reports a socket path that is a symlink, foreign-owned, or
// not a Unix socket.
var ErrUnsafeSocket = errors.New("antigravity socket is not a current-user socket")

// Conversation is the sidecar view of one desktop conversation.
type Conversation struct {
	ID                   string `json:"id"`
	Title                string `json:"title,omitempty"`
	ProjectID            string `json:"project_id,omitempty"`
	ProjectName          string `json:"project_name,omitempty"`
	ParentConversationID string `json:"parent_conversation_id,omitempty"`
	Archived             bool   `json:"archived,omitempty"`
	RunState             string `json:"run_state,omitempty"`
}

// Baseline is the visible log frontier at the time a caller binds or creates a
// conversation.
type Baseline struct {
	FileID      string `json:"file_id"`
	Offset      int64  `json:"offset"`
	HasHistory  bool   `json:"has_history"`
	LatestReply string `json:"latest_reply,omitempty"`
}

// Event is one user-visible transcript fact. Tool arguments, command output,
// diffs, and raw tool output never cross this boundary.
type Event struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Focus      string `json:"focus,omitempty"`
	FromFeishu bool   `json:"from_feishu,omitempty"`
	Offset     int64  `json:"offset"`
}

// Event kinds emitted by the transcript parser.
const (
	EventTurnInput    = "turn_input"
	EventProgress     = "progress"
	EventReply        = "reply"
	EventTool         = "tool"
	EventFocus        = "focus"
	EventAwaitDesktop = "await_desktop"
	EventError        = "error"
)

// ObserveResult is the response to one incremental transcript read.
type ObserveResult struct {
	Conversation  Conversation `json:"conversation"`
	Events        []Event      `json:"events"`
	NextFileID    string       `json:"next_file_id"`
	NextOffset    int64        `json:"next_offset"`
	Discontinuous bool         `json:"discontinuous"`
}

// CreateConversationRequest describes a new desktop conversation seeded with
// one user prompt.
type CreateConversationRequest struct {
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
}

// UpstreamConversation is the normalized upstream ConversationService state.
type UpstreamConversation struct {
	ID                   string
	Title                string
	ProjectID            string
	ParentConversationID string
	Archived             bool
	RunState             string
}

// ConversationService abstracts the official Antigravity ConversationService so
// the sidecar and its tests never depend on a live desktop process.
type ConversationService interface {
	ListConversations(ctx context.Context) ([]UpstreamConversation, error)
	GetConversation(ctx context.Context, id string) (UpstreamConversation, error)
	CreateConversation(ctx context.Context, req CreateConversationRequest) (UpstreamConversation, error)
}

// AgentRunner abstracts the agentapi binary invocation.
type AgentRunner interface {
	SendMessage(ctx context.Context, conversationID string, content string) error
}

// Health is the sidecar liveness payload.
type Health struct {
	ProtocolVersion int    `json:"protocol_version"`
	InstanceID      string `json:"instance_id"`
	Status          string `json:"status"`
}

// Project is one read-only mapping from ~/.gemini/config/projects.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
