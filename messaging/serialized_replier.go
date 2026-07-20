package messaging

import (
	"context"
	"sync"

	"github.com/fastclaw-ai/weclaw/platform"
)

type serializedReplier struct {
	inner platform.Replier
	mu    sync.Mutex
}

func newSerializedReplier(inner platform.Replier) *serializedReplier {
	return &serializedReplier{inner: inner}
}

func (r *serializedReplier) Capabilities() platform.Capabilities { return r.inner.Capabilities() }

func (r *serializedReplier) SendText(ctx context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.SendText(ctx, text)
}

func (r *serializedReplier) SendImage(ctx context.Context, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.SendImage(ctx, path)
}

func (r *serializedReplier) SendFile(ctx context.Context, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.SendFile(ctx, path)
}

func (r *serializedReplier) Typing(ctx context.Context, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.Typing(ctx, on)
}

func (r *serializedReplier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stream, err := r.inner.OpenStream(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &serializedStream{inner: stream, mu: &r.mu}, nil
}

func (r *serializedReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.AskChoices(ctx, prompt, choices)
}

func (r *serializedReplier) CurrentTaskCardID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	reporter, ok := r.inner.(platform.TaskCardReporter)
	if !ok {
		return ""
	}
	return reporter.CurrentTaskCardID()
}

func (r *serializedReplier) BindTaskCard(cardID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if binder, ok := r.inner.(platform.TaskCardBinder); ok {
		binder.BindTaskCard(cardID)
	}
}

type serializedStream struct {
	inner platform.Stream
	mu    *sync.Mutex
}

func (s *serializedStream) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Update(ctx, content)
}

func (s *serializedStream) Complete(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Complete(ctx, content)
}

func (s *serializedStream) Fail(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Fail(ctx, content)
}
