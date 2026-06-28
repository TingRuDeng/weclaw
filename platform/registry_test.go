package platform

import (
	"context"
	"testing"
)

func TestRegistryDispatchesAllowedUser(t *testing.T) {
	reply := &recordingReplier{}
	platform := &recordingPlatform{messages: []IncomingMessage{{Platform: PlatformWeChat, UserID: "user-1", Text: "hi"}}}
	registry := NewRegistry([]RegistryEntry{{Platform: platform, Access: NewAccessControl([]string{"user-1"})}})
	var got []IncomingMessage

	err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {
		got = append(got, msg)
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "user-1" {
		t.Fatalf("got messages=%#v, want user-1", got)
	}
	_ = reply
}

func TestRegistryRejectsEmptyAllowlistByDefault(t *testing.T) {
	platform := &recordingPlatform{messages: []IncomingMessage{{Platform: PlatformWeChat, UserID: "user-1", Text: "hi"}}}
	registry := NewRegistry([]RegistryEntry{{Platform: platform, Access: NewAccessControl(nil)}})
	called := false

	err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {
		called = true
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if called {
		t.Fatalf("dispatch should not be called for empty allowlist")
	}
}

type recordingPlatform struct {
	messages []IncomingMessage
	err      error
}

func (p *recordingPlatform) Name() PlatformName {
	return PlatformWeChat
}

func (p *recordingPlatform) AccountID() string {
	return "acct-1"
}

func (p *recordingPlatform) Capabilities() Capabilities {
	return Capabilities{Text: true}
}

func (p *recordingPlatform) Run(ctx context.Context, dispatch DispatchFunc) error {
	for _, msg := range p.messages {
		dispatch(ctx, msg, &recordingReplier{})
	}
	return p.err
}

type recordingReplier struct{}

func (r *recordingReplier) Capabilities() Capabilities { return Capabilities{Text: true} }
func (r *recordingReplier) SendText(ctx context.Context, text string) error {
	return nil
}
func (r *recordingReplier) SendImage(ctx context.Context, localPath string) error {
	return nil
}
func (r *recordingReplier) Typing(ctx context.Context, on bool) error { return nil }
func (r *recordingReplier) OpenStream(ctx context.Context, opts StreamOptions) (Stream, error) {
	return nil, ErrUnsupported
}
func (r *recordingReplier) AskChoices(ctx context.Context, prompt string, choices []Choice) error {
	return nil
}
