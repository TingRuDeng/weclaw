package platformtest

import (
	"context"
	"sync"

	"github.com/fastclaw-ai/weclaw/platform"
)

// Replier 是测试用回复器，记录业务层发出的所有回复意图。
type Replier struct {
	Caps         platform.Capabilities
	Texts        []string
	Images       []string
	Files        []string
	TypingStates []bool
	Choices      []ChoiceRequest
	Stream       *Stream
	StreamOpened chan struct{}
	streamOnce   sync.Once
}

// ChoiceRequest 记录一次 AskChoices 调用。
type ChoiceRequest struct {
	Prompt  string
	Choices []platform.Choice
}

// NewReplier 创建测试回复器。
func NewReplier(caps platform.Capabilities) *Replier {
	return &Replier{Caps: caps, Stream: &Stream{}, StreamOpened: make(chan struct{})}
}

func (r *Replier) Capabilities() platform.Capabilities {
	return r.Caps
}

func (r *Replier) SendText(ctx context.Context, text string) error {
	r.Texts = append(r.Texts, text)
	return nil
}

func (r *Replier) SendImage(ctx context.Context, localPath string) error {
	r.Images = append(r.Images, localPath)
	return nil
}

func (r *Replier) SendFile(ctx context.Context, localPath string) error {
	r.Files = append(r.Files, localPath)
	return nil
}

func (r *Replier) Typing(ctx context.Context, on bool) error {
	r.TypingStates = append(r.TypingStates, on)
	return nil
}

func (r *Replier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.Stream.Options = opts
	r.streamOnce.Do(func() { close(r.StreamOpened) })
	return r.Stream, nil
}

func (r *Replier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	r.Choices = append(r.Choices, ChoiceRequest{Prompt: prompt, Choices: append([]platform.Choice(nil), choices...)})
	return nil
}

// Stream 是测试用流式回复器。
type Stream struct {
	Options   platform.StreamOptions
	Updates   []string
	Completed string
	Failed    string
}

func (s *Stream) Update(ctx context.Context, content string) error {
	s.Updates = append(s.Updates, content)
	return nil
}

func (s *Stream) Complete(ctx context.Context, finalContent string) error {
	s.Completed = finalContent
	return nil
}

func (s *Stream) Fail(ctx context.Context, errText string) error {
	s.Failed = errText
	return nil
}

// Platform 是测试用平台，会按给定顺序同步分发消息。
type Platform struct {
	Account  string
	Caps     platform.Capabilities
	Messages []platform.IncomingMessage
	Reply    platform.Replier
}

// NewPlatform 创建默认微信测试平台。
func NewPlatform(accountID string, messages []platform.IncomingMessage, reply platform.Replier) *Platform {
	return &Platform{
		Account:  accountID,
		Caps:     platform.Capabilities{Text: true},
		Messages: append([]platform.IncomingMessage(nil), messages...),
		Reply:    reply,
	}
}

func (p *Platform) Name() platform.PlatformName {
	return platform.PlatformWeChat
}

func (p *Platform) AccountID() string {
	return p.Account
}

func (p *Platform) Capabilities() platform.Capabilities {
	return p.Caps
}

func (p *Platform) Run(ctx context.Context, dispatch platform.DispatchFunc) error {
	for _, msg := range p.Messages {
		dispatch(ctx, msg, p.Reply)
	}
	return nil
}
