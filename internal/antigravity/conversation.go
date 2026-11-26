package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ConversationServiceClient speaks the official Connect-based
// ConversationService over plain HTTP. Credentials never appear in errors or
// logs.
type ConversationServiceClient struct {
	baseURL    string
	csrfToken  string
	httpClient *http.Client
}

// NewConversationServiceClient validates the upstream address and CSRF token.
func NewConversationServiceClient(address, csrfToken string, httpClient *http.Client) (*ConversationServiceClient, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("ANTIGRAVITY_LS_ADDRESS is empty")
	}
	if strings.Contains(address, "://") {
		return nil, fmt.Errorf("ANTIGRAVITY_LS_ADDRESS must be host:port without scheme")
	}
	if strings.TrimSpace(csrfToken) == "" {
		return nil, fmt.Errorf("ANTIGRAVITY_CSRF_TOKEN is empty")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	return &ConversationServiceClient{
		baseURL: "http://" + address, csrfToken: csrfToken, httpClient: httpClient,
	}, nil
}

// NewConversationServiceFromEnv reads the official upstream configuration.
func NewConversationServiceFromEnv() (*ConversationServiceClient, error) {
	return NewConversationServiceClient(
		os.Getenv("ANTIGRAVITY_LS_ADDRESS"), os.Getenv("ANTIGRAVITY_CSRF_TOKEN"), nil,
	)
}

const (
	listConversationsPageSize = 100
	listConversationsMaxPages = 5
)

func (c *ConversationServiceClient) ListConversations(ctx context.Context) ([]UpstreamConversation, error) {
	conversations := make([]UpstreamConversation, 0, listConversationsPageSize)
	pageToken := ""
	seenTokens := make(map[string]bool)
	for page := 0; page < listConversationsMaxPages; page++ {
		request := map[string]any{
			"pageSize":        listConversationsPageSize,
			"includeArchived": false,
		}
		if pageToken != "" {
			request["pageToken"] = pageToken
		}
		var response struct {
			Conversations      []conversationWire `json:"conversations"`
			NextPageToken      string             `json:"nextPageToken"`
			NextPageTokenSnake string             `json:"next_page_token"`
		}
		if err := c.call(ctx, "ListConversations", request, &response); err != nil {
			return nil, err
		}
		for _, wire := range response.Conversations {
			conversations = append(conversations, wire.normalize())
		}
		nextToken := firstNonEmpty(response.NextPageToken, response.NextPageTokenSnake)
		if nextToken == "" || seenTokens[nextToken] {
			break
		}
		seenTokens[nextToken] = true
		pageToken = nextToken
	}
	return conversations, nil
}

func (c *ConversationServiceClient) GetConversation(ctx context.Context, id string) (UpstreamConversation, error) {
	if err := validateConversationID(id); err != nil {
		return UpstreamConversation{}, err
	}
	request := map[string]any{"conversationId": id}
	var raw json.RawMessage
	if err := c.call(ctx, "GetConversation", request, &raw); err != nil {
		return UpstreamConversation{}, err
	}
	wire, err := decodeConversationResponse(raw)
	if err != nil {
		return UpstreamConversation{}, err
	}
	return wire.normalize(), nil
}

func (c *ConversationServiceClient) CreateConversation(ctx context.Context, req CreateConversationRequest) (UpstreamConversation, error) {
	request := map[string]any{
		"projectId": req.ProjectID,
		"prompt":    []map[string]string{{"text": req.Prompt}},
	}
	if title := strings.TrimSpace(req.Title); title != "" {
		request["title"] = title
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		request["model"] = model
	}
	var raw json.RawMessage
	if err := c.call(ctx, "CreateConversation", request, &raw); err != nil {
		return UpstreamConversation{}, err
	}
	wire, err := decodeConversationResponse(raw)
	if err != nil {
		return UpstreamConversation{}, err
	}
	return wire.normalize(), nil
}

func (c *ConversationServiceClient) call(ctx context.Context, method string, request any, out any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode ConversationService request: %w", err)
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > defaultRequestTimeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	url := c.baseURL + "/product.v1.ConversationService/" + method
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build ConversationService request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Connect-Protocol-Version", "1")
	httpRequest.Header.Set("x-antigravity-csrf-token", c.csrfToken)
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("call ConversationService %s: %w", method, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBodyBytes))
	if err != nil {
		return fmt.Errorf("read ConversationService %s response: %w", method, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ConversationService %s returned HTTP %d: %s", method, response.StatusCode, c.redact(sanitizeTranscriptText(string(body), 200)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode ConversationService %s response: %w", method, err)
	}
	return nil
}

type conversationMetadataWire struct {
	ProjectID             string `json:"projectId"`
	ProjectIDSnake        string `json:"project_id"`
	ParentConversationID  string `json:"parentConversationId"`
	ParentConversationIDS string `json:"parent_conversation_id"`
}

type conversationAnnotationsWire struct {
	Title    string `json:"title"`
	Archived bool   `json:"archived"`
}

type conversationStateWire struct {
	Summary       string `json:"summary"`
	RunState      string `json:"runState"`
	RunStateSnake string `json:"run_state"`
}

type conversationWire struct {
	ID                    string                       `json:"id"`
	ConversationID        string                       `json:"conversationId"`
	ConversationIDSnake   string                       `json:"conversation_id"`
	Title                 string                       `json:"title"`
	Summary               string                       `json:"summary"`
	ParentConversationID  string                       `json:"parentConversationId"`
	ParentConversationIDS string                       `json:"parent_conversation_id"`
	Archived              bool                         `json:"archived"`
	ProjectID             string                       `json:"projectId"`
	ProjectIDSnake        string                       `json:"project_id"`
	RunState              string                       `json:"runState"`
	RunStateSnake         string                       `json:"run_state"`
	Metadata              *conversationMetadataWire    `json:"metadata"`
	Annotations           *conversationAnnotationsWire `json:"annotations"`
	State                 *conversationStateWire       `json:"state"`
}

func (w conversationWire) normalize() UpstreamConversation {
	var metaProjectID, metaParentID string
	if w.Metadata != nil {
		metaProjectID = firstNonEmpty(w.Metadata.ProjectID, w.Metadata.ProjectIDSnake)
		metaParentID = firstNonEmpty(w.Metadata.ParentConversationID, w.Metadata.ParentConversationIDS)
	}
	var annTitle string
	archived := w.Archived
	if w.Annotations != nil {
		annTitle = w.Annotations.Title
		archived = archived || w.Annotations.Archived
	}
	var stateSummary, stateRunState string
	if w.State != nil {
		stateSummary = w.State.Summary
		stateRunState = firstNonEmpty(w.State.RunState, w.State.RunStateSnake)
	}
	title := firstNonEmpty(annTitle, w.Title, sanitizeTranscriptText(stateSummary, 80), sanitizeTranscriptText(w.Summary, 80))
	return UpstreamConversation{
		ID:                   firstNonEmpty(w.ID, w.ConversationID, w.ConversationIDSnake),
		Title:                title,
		ProjectID:            firstNonEmpty(metaProjectID, w.ProjectID, w.ProjectIDSnake),
		ParentConversationID: firstNonEmpty(metaParentID, w.ParentConversationID, w.ParentConversationIDS),
		Archived:             archived,
		RunState:             firstNonEmpty(stateRunState, w.RunState, w.RunStateSnake),
	}
}

func decodeConversationResponse(raw json.RawMessage) (conversationWire, error) {
	if len(raw) == 0 {
		return conversationWire{}, fmt.Errorf("ConversationService returned an empty conversation")
	}
	var envelope struct {
		Conversation *conversationWire `json:"conversation"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Conversation != nil {
		return *envelope.Conversation, nil
	}
	var wire conversationWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return conversationWire{}, fmt.Errorf("decode ConversationService conversation: %w", err)
	}
	return wire, nil
}

// redact removes the CSRF token from upstream-controlled error text.
func (c *ConversationServiceClient) redact(text string) string {
	if strings.TrimSpace(c.csrfToken) == "" {
		return text
	}
	return strings.ReplaceAll(text, c.csrfToken, "[redacted]")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
