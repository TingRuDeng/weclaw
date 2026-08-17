package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type agentInteractionContextOptions struct {
	actorUserID string
	routeUserID string
	agentName   string
	reply       platform.Replier
	lease       *agentInteractionLease
}

type agentInteractionLease struct {
	mu             sync.Mutex
	detached       bool
	inFlight       int
	detachedSignal chan struct{}
}

type agentInteractionDetachClaim struct {
	lease  *agentInteractionLease
	locked bool
}

func (l *agentInteractionLease) begin() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.detached {
		return false
	}
	l.inFlight++
	return true
}

func (l *agentInteractionLease) end() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.inFlight > 0 {
		l.inFlight--
	}
	l.mu.Unlock()
}

// claimDetach 在调用方完成持久化栅栏前一直阻止新交互进入。
// 调用方必须以 finish(true/false) 结束 claim；false 保持原观察端可交互。
func (l *agentInteractionLease) claimDetach() (*agentInteractionDetachClaim, bool) {
	if l == nil {
		return &agentInteractionDetachClaim{}, true
	}
	l.mu.Lock()
	if l.detached {
		l.mu.Unlock()
		return &agentInteractionDetachClaim{}, true
	}
	if l.inFlight > 0 {
		l.mu.Unlock()
		return nil, false
	}
	return &agentInteractionDetachClaim{lease: l, locked: true}, true
}

// claimForceDetach blocks new interactions while persistence is prepared, but
// permits release to win over an already waiting approval or question. The
// caller closes the detach signal only after the durable release write succeeds.
func (l *agentInteractionLease) claimForceDetach() *agentInteractionDetachClaim {
	if l == nil {
		return &agentInteractionDetachClaim{}
	}
	l.mu.Lock()
	if l.detached {
		l.mu.Unlock()
		return &agentInteractionDetachClaim{}
	}
	return &agentInteractionDetachClaim{lease: l, locked: true}
}

func (c *agentInteractionDetachClaim) finish(detach bool) {
	if c == nil || !c.locked || c.lease == nil {
		return
	}
	if detach {
		c.lease.detachLocked()
	}
	c.locked = false
	c.lease.mu.Unlock()
}

// forceDetach 仅用于服务排空：让正在等待的交互返回 observer-detached，
// 不能把 watcher context 的取消转换成对共享任务的默认拒绝或空回答。
func (l *agentInteractionLease) forceDetach() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.detachLocked()
}

func (l *agentInteractionLease) detachLocked() {
	if l.detached {
		return
	}
	l.detached = true
	if l.detachedSignal != nil {
		close(l.detachedSignal)
	}
}

func (l *agentInteractionLease) isDetached() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.detached
}

func (l *agentInteractionLease) done() <-chan struct{} {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.detachedSignal == nil {
		l.detachedSignal = make(chan struct{})
		if l.detached {
			close(l.detachedSignal)
		}
	}
	return l.detachedSignal
}

func (l *agentInteractionLease) bindContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if l == nil {
		return ctx, func() {}
	}
	bound, cancel := context.WithCancel(ctx)
	done := l.done()
	go func() {
		select {
		case <-done:
			cancel()
		case <-bound.Done():
		}
	}()
	return bound, cancel
}

type userInputQuestionRequest struct {
	requestID  string
	question   agent.UserInputQuestion
	resolution agent.CodexInteractionResolution
	opts       agentInteractionContextOptions
}

// withAgentInteractions 为同一任务注入审批和结构化问答能力。
func (h *Handler) withAgentInteractions(ctx context.Context, opts agentInteractionContextOptions) context.Context {
	ctx = agent.ContextWithApprovalHandler(ctx, h.approvalHandlerForRoute(opts))
	return agent.ContextWithUserInputHandler(ctx, h.userInputHandlerForRoute(opts))
}

// userInputHandlerForRoute 顺序展示问题，确保平台回复与真实任务发起人绑定。
func (h *Handler) userInputHandlerForRoute(opts agentInteractionContextOptions) agent.UserInputHandler {
	return func(ctx context.Context, req agent.UserInputRequest) (agent.UserInputAnswers, error) {
		if !opts.lease.begin() {
			return nil, agent.ErrCodexObserverDetached
		}
		defer opts.lease.end()
		ctx, cancelInteraction := opts.lease.bindContext(ctx)
		defer cancelInteraction()
		if err := validateAgentInteractionRoute(opts); err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.RequestID) == "" || len(req.Questions) == 0 {
			return nil, fmt.Errorf("Codex 结构化问答请求不完整")
		}
		answers := make(agent.UserInputAnswers, len(req.Questions))
		for _, question := range req.Questions {
			answer, err := h.askUserInputQuestion(ctx, userInputQuestionRequest{
				requestID: req.RequestID, question: question, resolution: req.Resolution, opts: opts,
			})
			if err != nil {
				return nil, err
			}
			answers[question.ID] = []string{answer}
		}
		return answers, nil
	}
}

func (h *Handler) askUserInputQuestion(ctx context.Context, req userInputQuestionRequest) (string, error) {
	key, options, err := buildUserInputOptions(req)
	if err != nil {
		return "", err
	}
	pending, err := h.registerPendingApprovalForRoute(
		req.opts.actorUserID, req.opts.routeUserID, key, options, "", platform.ChoiceInteractionUserInput,
	)
	if err != nil {
		return "", err
	}
	defer h.clearPendingApproval(req.opts.actorUserID, pending)
	choices := userInputPlatformChoices(options, key, req.opts)
	if err := req.opts.reply.AskChoices(ctx, userInputPrompt(req.question), choices); err != nil {
		if req.opts.lease.isDetached() {
			return "", agent.ErrCodexObserverDetached
		}
		return "", err
	}
	return waitForUserInputChoice(ctx, pending, req.opts.lease, req.resolution)
}

func buildUserInputOptions(req userInputQuestionRequest) (string, []agent.ApprovalOption, error) {
	questionID := strings.TrimSpace(req.question.ID)
	if questionID == "" {
		return "", nil, fmt.Errorf("结构化问答包含空 question ID")
	}
	if len(req.question.Options) == 0 {
		return "", nil, fmt.Errorf("问题 %s 不支持自由文本问答", questionID)
	}
	seen := make(map[string]bool, len(req.question.Options))
	options := make([]agent.ApprovalOption, 0, len(req.question.Options))
	for _, option := range req.question.Options {
		label := strings.TrimSpace(option.Label)
		if label == "" || seen[label] {
			return "", nil, fmt.Errorf("问题 %s 包含空白或重复选项", questionID)
		}
		seen[label] = true
		options = append(options, agent.ApprovalOption{ID: label, Name: userInputOptionLabel(option)})
	}
	return strings.TrimSpace(req.requestID) + ":" + questionID, options, nil
}

func userInputOptionLabel(option agent.UserInputOption) string {
	label := strings.TrimSpace(option.Label)
	description := strings.TrimSpace(option.Description)
	if description == "" {
		return label
	}
	return label + " - " + description
}

func userInputPlatformChoices(options []agent.ApprovalOption, key string, opts agentInteractionContextOptions) []platform.Choice {
	choices := make([]platform.Choice, 0, len(options))
	metadata := approvalChoiceMetadata(
		key, taskCardIDFromReplier(opts.reply), opts.actorUserID, opts.routeUserID,
		opts.agentName, platform.ChoiceInteractionUserInput,
	)
	for _, option := range options {
		choices = append(choices, platform.Choice{ID: option.ID, Label: option.Name, Metadata: metadata})
	}
	return choices
}

func userInputPrompt(question agent.UserInputQuestion) string {
	header := strings.TrimSpace(question.Header)
	prompt := strings.TrimSpace(question.Prompt)
	if header == "" {
		return prompt
	}
	if prompt == "" {
		return header
	}
	return header + "\n\n" + prompt
}

func waitForUserInputChoice(
	ctx context.Context,
	pending *pendingApproval,
	lease *agentInteractionLease,
	resolution agent.CodexInteractionResolution,
) (string, error) {
	timer := time.NewTimer(pendingApprovalTimeout)
	defer timer.Stop()
	select {
	case choice := <-pending.choices:
		return strings.TrimSpace(choice), nil
	case <-codexInteractionResolutionDone(resolution):
		return "", codexInteractionResolutionError(resolution)
	case <-timer.C:
		return "", fmt.Errorf("Codex 结构化问答等待超时")
	case <-lease.done():
		return "", agent.ErrCodexObserverDetached
	case <-ctx.Done():
		if lease.isDetached() {
			return "", agent.ErrCodexObserverDetached
		}
		return "", ctx.Err()
	}
}

func codexInteractionResolutionDone(resolution agent.CodexInteractionResolution) <-chan struct{} {
	if resolution == nil {
		return nil
	}
	return resolution.Done()
}

func codexInteractionResolutionError(resolution agent.CodexInteractionResolution) error {
	if resolution != nil {
		if err := resolution.Err(); err != nil {
			return err
		}
	}
	return agent.ErrCodexInteractionResolvedExternally
}

func validateAgentInteractionRoute(opts agentInteractionContextOptions) error {
	actor := strings.TrimSpace(opts.actorUserID)
	route := strings.TrimSpace(opts.routeUserID)
	if actor == "" || route == "" || opts.reply == nil {
		return fmt.Errorf("Agent 交互缺少授权路由")
	}
	if route != actor && !strings.HasPrefix(route, string(platform.PlatformFeishu)+":") {
		return fmt.Errorf("用户 %s 无权处理路由 %s 的 Agent 交互", actor, route)
	}
	return nil
}
