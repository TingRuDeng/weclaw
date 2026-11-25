package messaging

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/antigravity"
	"github.com/fastclaw-ai/weclaw/platform"
)

type antigravityFakeClient struct {
	mu            sync.Mutex
	conversations []antigravity.Conversation
	baselines     map[string]antigravity.Baseline
	listErr       error
	getErr        error
	sendErr       error
	createErr     error
	healthErr     error
	sent          []string
	created       []antigravity.CreateConversationRequest
	observeCalls  int
	observeFn     func(conversationID, fileID string, offset int64) (antigravity.ObserveResult, error)
}

func (f *antigravityFakeClient) Health(context.Context) (antigravity.Health, error) {
	if f.healthErr != nil {
		return antigravity.Health{}, f.healthErr
	}
	return antigravity.Health{ProtocolVersion: antigravity.ProtocolVersion, InstanceID: "fake", Status: "ok"}, nil
}

func (f *antigravityFakeClient) ListConversations(context.Context) ([]antigravity.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]antigravity.Conversation(nil), f.conversations...), nil
}

func (f *antigravityFakeClient) GetConversation(_ context.Context, conversationID string) (antigravity.Conversation, antigravity.Baseline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return antigravity.Conversation{}, antigravity.Baseline{}, f.getErr
	}
	for _, conversation := range f.conversations {
		if conversation.ID == conversationID {
			return conversation, f.baselines[conversationID], nil
		}
	}
	return antigravity.Conversation{}, antigravity.Baseline{}, fmt.Errorf("conversation not found")
}

func (f *antigravityFakeClient) CreateConversation(_ context.Context, req antigravity.CreateConversationRequest) (antigravity.Conversation, antigravity.Baseline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return antigravity.Conversation{}, antigravity.Baseline{}, f.createErr
	}
	f.created = append(f.created, req)
	conversation := antigravity.Conversation{ID: "created-1", Title: req.Title, ProjectID: req.ProjectID, ProjectName: req.ProjectID}
	f.conversations = append(f.conversations, conversation)
	if f.baselines == nil {
		f.baselines = make(map[string]antigravity.Baseline)
	}
	f.baselines[conversation.ID] = antigravity.Baseline{}
	return conversation, antigravity.Baseline{}, nil
}

func (f *antigravityFakeClient) SendMessage(_ context.Context, conversationID, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, conversationID+"\x00"+content)
	return nil
}

func (f *antigravityFakeClient) Observe(_ context.Context, conversationID, fileID string, offset int64) (antigravity.ObserveResult, error) {
	f.mu.Lock()
	f.observeCalls++
	fn := f.observeFn
	conversation := antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_IDLE"}
	for _, item := range f.conversations {
		if item.ID == conversationID {
			conversation = item
		}
	}
	f.mu.Unlock()
	if fn != nil {
		return fn(conversationID, fileID, offset)
	}
	return antigravity.ObserveResult{Conversation: conversation, NextFileID: fileID, NextOffset: offset}, nil
}

func (f *antigravityFakeClient) snapshotSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *antigravityFakeClient) snapshotCreated() []antigravity.CreateConversationRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]antigravity.CreateConversationRequest(nil), f.created...)
}

type antigravityFakeStream struct {
	mu          sync.Mutex
	options     platform.StreamOptions
	updates     []string
	completed   string
	failed      string
	detached    string
	superseded  string
	completeErr error
}

func (s *antigravityFakeStream) Update(_ context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, content)
	return nil
}

func (s *antigravityFakeStream) Complete(_ context.Context, finalContent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = finalContent
	return s.completeErr
}

func (s *antigravityFakeStream) Fail(_ context.Context, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = errText
	return nil
}

func (s *antigravityFakeStream) Detach(_ context.Context, notice string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached = notice
	return nil
}

func (s *antigravityFakeStream) Supersede(_ context.Context, notice string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.superseded = notice
	return nil
}

func (s *antigravityFakeStream) snapshot() (updates []string, completed, failed, detached, superseded string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.updates...), s.completed, s.failed, s.detached, s.superseded
}

type antigravityFakeChoice struct {
	prompt  string
	choices []platform.Choice
}

type antigravityFakeResult struct {
	title string
	text  string
	key   string
}

type antigravityFakeReplier struct {
	mu       sync.Mutex
	route    platform.DeliveryRoute
	texts    []string
	choices  []antigravityFakeChoice
	results  []antigravityFakeResult
	stream   *antigravityFakeStream
	opened   chan struct{}
	openOnce sync.Once
}

func newAntigravityFakeReplier(route platform.DeliveryRoute) *antigravityFakeReplier {
	return &antigravityFakeReplier{route: route, stream: &antigravityFakeStream{}, opened: make(chan struct{})}
}

func (r *antigravityFakeReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Buttons: true, Streaming: true}
}

func (r *antigravityFakeReplier) SendText(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return nil
}

func (r *antigravityFakeReplier) SendImage(context.Context, string) error { return nil }
func (r *antigravityFakeReplier) SendFile(context.Context, string) error  { return nil }
func (r *antigravityFakeReplier) Typing(context.Context, bool) error      { return nil }

func (r *antigravityFakeReplier) OpenStream(_ context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.stream.mu.Lock()
	r.stream.options = opts
	r.stream.mu.Unlock()
	r.openOnce.Do(func() { close(r.opened) })
	return r.stream, nil
}

func (r *antigravityFakeReplier) AskChoices(_ context.Context, prompt string, choices []platform.Choice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.choices = append(r.choices, antigravityFakeChoice{prompt: prompt, choices: append([]platform.Choice(nil), choices...)})
	return nil
}

func (r *antigravityFakeReplier) DeliveryRoute() platform.DeliveryRoute { return r.route }

func (r *antigravityFakeReplier) SendResultIdempotent(_ context.Context, result platform.TerminalResult, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, antigravityFakeResult{title: result.Title, text: result.Text, key: key})
	return nil
}

func (r *antigravityFakeReplier) snapshotTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

func (r *antigravityFakeReplier) snapshotChoices() []antigravityFakeChoice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]antigravityFakeChoice(nil), r.choices...)
}

func (r *antigravityFakeReplier) snapshotResults() []antigravityFakeResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]antigravityFakeResult(nil), r.results...)
}

func newAntigravityTestHandler(client *antigravityFakeClient) *Handler {
	h := NewHandler(nil, nil)
	h.SetAntigravityClient(client)
	return h
}

func antigravityTestMessage(routeUserID string) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "bot", UserID: "user", ChatID: "chat",
		Route: platform.SessionRoute{Key: routeUserID}, MessageID: "message-1",
	}
}

func waitForAntigravity(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
