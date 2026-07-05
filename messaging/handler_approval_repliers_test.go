package messaging

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

type approvalKeyCaptureReplier struct {
	mu         sync.Mutex
	texts      []string
	approvalCh chan string
}

func newApprovalKeyCaptureReplier() *approvalKeyCaptureReplier {
	return &approvalKeyCaptureReplier{approvalCh: make(chan string, 1)}
}

func (r *approvalKeyCaptureReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Buttons: true}
}

func (r *approvalKeyCaptureReplier) SendText(ctx context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return nil
}

func (r *approvalKeyCaptureReplier) SendImage(ctx context.Context, localPath string) error {
	return nil
}

func (r *approvalKeyCaptureReplier) SendFile(ctx context.Context, localPath string) error {
	return nil
}

func (r *approvalKeyCaptureReplier) Typing(ctx context.Context, on bool) error {
	return nil
}

func (r *approvalKeyCaptureReplier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	return nil, errors.New("stream not supported")
}

func (r *approvalKeyCaptureReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	r.approvalCh <- approvalKeyFromChoices(choices)
	return nil
}

func (r *approvalKeyCaptureReplier) waitApprovalKey(t *testing.T, ctx context.Context) string {
	t.Helper()
	select {
	case key := <-r.approvalCh:
		if key == "" {
			t.Fatal("approval key is empty")
		}
		return key
	case <-ctx.Done():
		t.Fatal("approval key was not captured")
		return ""
	}
}

func (r *approvalKeyCaptureReplier) textsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

func approvalKeyFromChoices(choices []platform.Choice) string {
	if len(choices) == 0 {
		return ""
	}
	return strings.TrimSpace(choices[0].Metadata["approval_key"])
}

type taskCardMetadataReplier struct {
	approvalKeyCaptureReplier
	taskCardID string
	choiceCh   chan platform.Choice
}

func newTaskCardMetadataReplier(taskCardID string) *taskCardMetadataReplier {
	return &taskCardMetadataReplier{taskCardID: taskCardID, choiceCh: make(chan platform.Choice, 1)}
}

func (r *taskCardMetadataReplier) CurrentTaskCardID() string {
	return r.taskCardID
}

func (r *taskCardMetadataReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	if len(choices) > 0 {
		r.choiceCh <- choices[0]
	}
	return nil
}

func (r *taskCardMetadataReplier) waitChoice(t *testing.T, ctx context.Context) platform.Choice {
	t.Helper()
	select {
	case choice := <-r.choiceCh:
		return choice
	case <-ctx.Done():
		t.Fatal("choice was not captured")
		return platform.Choice{}
	}
}
