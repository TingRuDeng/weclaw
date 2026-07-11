package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type agentInteractionContextOptions struct {
	actorUserID string
	routeUserID string
	reply       platform.Replier
}

type userInputQuestionRequest struct {
	requestID string
	question  agent.UserInputQuestion
	opts      agentInteractionContextOptions
}

// withAgentInteractions 为同一任务注入审批和结构化问答能力。
func (h *Handler) withAgentInteractions(ctx context.Context, opts agentInteractionContextOptions) context.Context {
	ctx = agent.ContextWithApprovalHandler(ctx, h.approvalHandlerForUser(
		opts.actorUserID, opts.routeUserID, opts.reply,
	))
	return agent.ContextWithUserInputHandler(ctx, h.userInputHandlerForRoute(opts))
}

// userInputHandlerForRoute 顺序展示问题，确保平台回复与真实任务发起人绑定。
func (h *Handler) userInputHandlerForRoute(opts agentInteractionContextOptions) agent.UserInputHandler {
	return func(ctx context.Context, req agent.UserInputRequest) (agent.UserInputAnswers, error) {
		if err := validateAgentInteractionRoute(opts); err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.RequestID) == "" || len(req.Questions) == 0 {
			return nil, fmt.Errorf("Codex 结构化问答请求不完整")
		}
		answers := make(agent.UserInputAnswers, len(req.Questions))
		for _, question := range req.Questions {
			answer, err := h.askUserInputQuestion(ctx, userInputQuestionRequest{
				requestID: req.RequestID, question: question, opts: opts,
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
	pending, err := h.registerPendingApproval(req.opts.actorUserID, key, options)
	if err != nil {
		return "", err
	}
	defer h.clearPendingApproval(req.opts.actorUserID, pending)
	choices := userInputPlatformChoices(options, key, req.opts)
	if err := req.opts.reply.AskChoices(ctx, userInputPrompt(req.question), choices); err != nil {
		return "", err
	}
	return waitForUserInputChoice(ctx, pending)
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
	metadata := approvalChoiceMetadata(key, taskCardIDFromReplier(opts.reply), opts.actorUserID, opts.routeUserID)
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

func waitForUserInputChoice(ctx context.Context, pending *pendingApproval) (string, error) {
	timer := time.NewTimer(pendingApprovalTimeout)
	defer timer.Stop()
	select {
	case choice := <-pending.choices:
		return strings.TrimSpace(choice), nil
	case <-timer.C:
		return "", fmt.Errorf("Codex 结构化问答等待超时")
	case <-ctx.Done():
		return "", ctx.Err()
	}
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
