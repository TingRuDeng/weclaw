package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client is the private Unix-socket client used by the messaging layer.
type Client struct {
	socketPath string
	httpClient *http.Client
	baseURL    string
}

// NewClient resolves the socket path and validates it before every dial.
func NewClient(socketPath string) (*Client, error) {
	path, err := ResolveSocketPath(socketPath)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if err := validateSocketPathLength(path); err != nil {
				return nil, err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return nil, fmt.Errorf("inspect antigravity socket: %w", err)
			}
			if err := validateSocketInfo(info); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, "unix", path)
		},
	}
	return &Client{
		socketPath: path,
		httpClient: &http.Client{Transport: transport, Timeout: defaultRequestTimeout},
		baseURL:    "http://antigravity",
	}, nil
}

// SocketPath reports the connected socket path.
func (c *Client) SocketPath() string { return c.socketPath }

// Health probes the sidecar liveness endpoint.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var response Health
	if err := c.do(ctx, http.MethodGet, "/v1/health", nil, &response); err != nil {
		return Health{}, err
	}
	return response, nil
}

// ListConversations returns filtered desktop conversations.
func (c *Client) ListConversations(ctx context.Context) ([]Conversation, error) {
	var response struct {
		ProtocolVersion int            `json:"protocol_version"`
		InstanceID      string         `json:"instance_id"`
		Conversations   []Conversation `json:"conversations"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/conversations", nil, &response); err != nil {
		return nil, err
	}
	return response.Conversations, nil
}

// GetConversation returns authoritative state and the current log baseline.
func (c *Client) GetConversation(ctx context.Context, conversationID string) (Conversation, Baseline, error) {
	var response struct {
		ProtocolVersion int          `json:"protocol_version"`
		InstanceID      string       `json:"instance_id"`
		Conversation    Conversation `json:"conversation"`
		Baseline        Baseline     `json:"baseline"`
	}
	path := "/v1/conversations/" + conversationID
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return Conversation{}, Baseline{}, err
	}
	return response.Conversation, response.Baseline, nil
}

// CreateConversation creates a desktop conversation seeded with one prompt.
func (c *Client) CreateConversation(ctx context.Context, req CreateConversationRequest) (Conversation, Baseline, error) {
	var response struct {
		ProtocolVersion int          `json:"protocol_version"`
		InstanceID      string       `json:"instance_id"`
		Conversation    Conversation `json:"conversation"`
		Baseline        Baseline     `json:"baseline"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/conversations", req, &response); err != nil {
		return Conversation{}, Baseline{}, err
	}
	return response.Conversation, response.Baseline, nil
}

// SendMessage submits one user message through agentapi.
func (c *Client) SendMessage(ctx context.Context, conversationID string, content string) error {
	request := map[string]string{"content": content}
	return c.do(ctx, http.MethodPost, "/v1/conversations/"+conversationID+"/messages", request, nil)
}

// Observe reads transcript events after the supplied cursor.
func (c *Client) Observe(ctx context.Context, conversationID, fileID string, offset int64) (ObserveResult, error) {
	var response struct {
		ProtocolVersion int          `json:"protocol_version"`
		InstanceID      string       `json:"instance_id"`
		Conversation    Conversation `json:"conversation"`
		Events          []Event      `json:"events"`
		NextFileID      string       `json:"next_file_id"`
		NextOffset      int64        `json:"next_offset"`
		Discontinuous   bool         `json:"discontinuous"`
	}
	request := map[string]any{"file_id": fileID, "offset": offset}
	if err := c.do(ctx, http.MethodPost, "/v1/conversations/"+conversationID+"/observe", request, &response); err != nil {
		return ObserveResult{}, err
	}
	return ObserveResult{
		Conversation: response.Conversation, Events: response.Events,
		NextFileID: response.NextFileID, NextOffset: response.NextOffset,
		Discontinuous: response.Discontinuous,
	}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > defaultRequestTimeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode sidecar request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build sidecar request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call antigravity sidecar: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBodyBytes))
	if err != nil {
		return fmt.Errorf("read sidecar response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &failure)
		message := strings.TrimSpace(failure.Error)
		if message == "" {
			message = strings.TrimSpace(sanitizeTranscriptText(string(payload), 200))
		}
		return fmt.Errorf("antigravity sidecar returned HTTP %d: %s", response.StatusCode, message)
	}
	var envelope struct {
		ProtocolVersion int    `json:"protocol_version"`
		InstanceID      string `json:"instance_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode sidecar response: %w", err)
	}
	if envelope.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported antigravity sidecar protocol version %d", envelope.ProtocolVersion)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode sidecar response: %w", err)
	}
	return nil
}
