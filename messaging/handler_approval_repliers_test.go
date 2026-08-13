package messaging

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
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

type choiceRequest struct {
	prompt  string
	choices []platform.Choice
}

type automaticApprovalRecord struct {
	prompt string
	choice platform.Choice
}

type choiceRequestCaptureReplier struct {
	approvalKeyCaptureReplier
	choiceCh chan choiceRequest
}

type blockingChoiceRequestCaptureReplier struct {
	*choiceRequestCaptureReplier
	release chan struct{}
}

type approvalStateRecord struct {
	prompt  string
	choices []platform.Choice
	state   agent.ApprovalRequestState
}

type blockingApprovalStateCaptureReplier struct {
	*blockingChoiceRequestCaptureReplier
	stateCh chan approvalStateRecord
}

func newBlockingChoiceRequestCaptureReplier() *blockingChoiceRequestCaptureReplier {
	return &blockingChoiceRequestCaptureReplier{
		choiceRequestCaptureReplier: newChoiceRequestCaptureReplier(),
		release:                     make(chan struct{}),
	}
}

func newBlockingApprovalStateCaptureReplier() *blockingApprovalStateCaptureReplier {
	return &blockingApprovalStateCaptureReplier{
		blockingChoiceRequestCaptureReplier: newBlockingChoiceRequestCaptureReplier(),
		stateCh:                             make(chan approvalStateRecord, 1),
	}
}

func (r *blockingApprovalStateCaptureReplier) RecordApprovalState(_ context.Context, prompt string, choices []platform.Choice, state agent.ApprovalRequestState) error {
	r.stateCh <- approvalStateRecord{prompt: prompt, choices: append([]platform.Choice(nil), choices...), state: state}
	return nil
}

func (r *blockingChoiceRequestCaptureReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	r.choiceCh <- choiceRequest{prompt: prompt, choices: append([]platform.Choice(nil), choices...)}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newChoiceRequestCaptureReplier() *choiceRequestCaptureReplier {
	return &choiceRequestCaptureReplier{choiceCh: make(chan choiceRequest, 1)}
}

func (r *choiceRequestCaptureReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	r.choiceCh <- choiceRequest{prompt: prompt, choices: append([]platform.Choice(nil), choices...)}
	return nil
}

func (r *choiceRequestCaptureReplier) waitChoiceRequest(t *testing.T, ctx context.Context) choiceRequest {
	t.Helper()
	select {
	case request := <-r.choiceCh:
		return request
	case <-ctx.Done():
		t.Fatal("choice request was not captured")
		return choiceRequest{}
	}
}

type automaticApprovalCaptureReplier struct {
	*choiceRequestCaptureReplier
	taskCardID string
	recordErr  error
	recordCh   chan automaticApprovalRecord
}

type blockingAutomaticApprovalCaptureReplier struct {
	*automaticApprovalCaptureReplier
	started chan struct{}
	release chan struct{}
}

type failingChoiceAutomaticApprovalCaptureReplier struct {
	*automaticApprovalCaptureReplier
	release chan struct{}
	askErr  error
}

func newFailingChoiceAutomaticApprovalCaptureReplier(taskCardID string, askErr error) *failingChoiceAutomaticApprovalCaptureReplier {
	return &failingChoiceAutomaticApprovalCaptureReplier{
		automaticApprovalCaptureReplier: newAutomaticApprovalCaptureReplier(taskCardID),
		release:                         make(chan struct{}),
		askErr:                          askErr,
	}
}

func (r *failingChoiceAutomaticApprovalCaptureReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	if err := r.choiceRequestCaptureReplier.AskChoices(ctx, prompt, choices); err != nil {
		return err
	}
	select {
	case <-r.release:
		return r.askErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newBlockingAutomaticApprovalCaptureReplier(taskCardID string) *blockingAutomaticApprovalCaptureReplier {
	return &blockingAutomaticApprovalCaptureReplier{
		automaticApprovalCaptureReplier: newAutomaticApprovalCaptureReplier(taskCardID),
		started:                         make(chan struct{}, 1),
		release:                         make(chan struct{}),
	}
}

func (r *blockingAutomaticApprovalCaptureReplier) RecordAutomaticApproval(ctx context.Context, prompt string, choice platform.Choice) error {
	r.started <- struct{}{}
	select {
	case <-r.release:
		return r.automaticApprovalCaptureReplier.RecordAutomaticApproval(ctx, prompt, choice)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newAutomaticApprovalCaptureReplier(taskCardID string) *automaticApprovalCaptureReplier {
	return &automaticApprovalCaptureReplier{
		choiceRequestCaptureReplier: newChoiceRequestCaptureReplier(),
		taskCardID:                  taskCardID,
		recordCh:                    make(chan automaticApprovalRecord, 2),
	}
}

func (r *automaticApprovalCaptureReplier) CurrentTaskCardID() string {
	return r.taskCardID
}

func (r *automaticApprovalCaptureReplier) RecordAutomaticApproval(_ context.Context, prompt string, choice platform.Choice) error {
	r.recordCh <- automaticApprovalRecord{prompt: prompt, choice: choice}
	return r.recordErr
}

func (r *automaticApprovalCaptureReplier) waitAutomaticApproval(t *testing.T, ctx context.Context) automaticApprovalRecord {
	t.Helper()
	select {
	case record := <-r.recordCh:
		return record
	case <-ctx.Done():
		t.Fatal("automatic approval record was not captured")
		return automaticApprovalRecord{}
	}
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
