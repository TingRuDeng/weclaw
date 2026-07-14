package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type pendingApproval struct {
	choices chan string
	allowed map[string]bool
	aliases map[string]string
	key     string
	userID  string
}

type approvalTextConsumeResult uint8

const (
	approvalTextUnmatched approvalTextConsumeResult = iota
	approvalTextConsumed
	approvalTextAmbiguous
)

func (h *Handler) approvalHandlerForUser(userID string, routeUserID string, reply platform.Replier) agent.ApprovalHandler {
	return func(ctx context.Context, req agent.ApprovalRequest) (string, error) {
		if err := validateAgentInteractionRoute(agentInteractionContextOptions{
			actorUserID: userID, routeUserID: routeUserID, reply: reply,
		}); err != nil {
			return "", err
		}
		prompt := approvalPrompt(req)
		approvalKey := approvalPendingKey(req.RequestID)
		choices := approvalChoices(req.Options, approvalKey, taskCardIDFromReplier(reply), userID, routeUserID)
		if len(choices) == 0 {
			return "", fmt.Errorf("approval request has no options")
		}
		if h.isYoloMode(routeUserID) {
			decision := autoApproveApprovalOption(req.Options)
			log.Printf("[handler] yolo mode auto-approving sensitive operation for %s -> %q", userID, decision)
			h.auditRecord(auditEntry{User: userID, Action: "approval_auto_yolo", Summary: decision})
			return decision, nil
		}
		pending, err := h.registerPendingApproval(userID, approvalKey, req.Options)
		if err != nil {
			return "", err
		}
		defer h.clearPendingApproval(userID, pending)
		if err := reply.AskChoices(ctx, prompt, choices); err != nil {
			return "", err
		}
		timer := time.NewTimer(pendingApprovalTimeout)
		defer timer.Stop()
		select {
		case choice := <-pending.choices:
			return strings.TrimSpace(choice), nil
		case <-timer.C:
			return defaultDenyApprovalOption(req.Options), nil
		case <-ctx.Done():
			return defaultDenyApprovalOption(req.Options), ctx.Err()
		}
	}
}

func (h *Handler) registerPendingApproval(userID string, approvalKey string, options []agent.ApprovalOption) (*pendingApproval, error) {
	pending := &pendingApproval{
		choices: make(chan string, 1),
		allowed: approvalOptionSet(options),
		aliases: approvalOptionAliases(options),
		key:     pendingApprovalMapKey(userID, approvalKey),
		userID:  strings.TrimSpace(userID),
	}
	h.pendingApprovalsMu.Lock()
	if h.pendingApprovals == nil {
		h.pendingApprovals = make(map[string]*pendingApproval)
	}
	if h.pendingApprovals[pending.key] != nil {
		h.pendingApprovalsMu.Unlock()
		return nil, fmt.Errorf("approval request key collision")
	}
	h.pendingApprovals[pending.key] = pending
	h.pendingApprovalsMu.Unlock()
	return pending, nil
}

func (h *Handler) clearPendingApproval(userID string, pending *pendingApproval) {
	h.pendingApprovalsMu.Lock()
	if h.pendingApprovals[pending.key] == pending {
		delete(h.pendingApprovals, pending.key)
	}
	h.pendingApprovalsMu.Unlock()
}

func (h *Handler) consumePendingApproval(userID string, choice string) bool {
	return h.consumePendingApprovalText(userID, choice) == approvalTextConsumed
}

// consumePendingApprovalText 只消费唯一匹配的文本审批，多个匹配项交给调用方提示用户选择卡片。
func (h *Handler) consumePendingApprovalText(userID string, choice string) approvalTextConsumeResult {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return approvalTextUnmatched
	}
	h.pendingApprovalsMu.Lock()
	pending, resolved, ambiguous := h.findPendingApprovalTextLocked(userID, choice)
	h.pendingApprovalsMu.Unlock()
	if ambiguous {
		return approvalTextAmbiguous
	}
	if pending == nil {
		return approvalTextUnmatched
	}
	deliverPendingApprovalChoice(pending, resolved)
	return approvalTextConsumed
}

func (h *Handler) consumePendingApprovalForKey(userID string, approvalKey string, choice string) bool {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return false
	}
	h.pendingApprovalsMu.Lock()
	pending := h.findPendingApprovalLocked(userID, approvalKey)
	h.pendingApprovalsMu.Unlock()
	if pending == nil {
		return false
	}
	resolved := pending.resolveChoice(choice)
	if resolved == "" {
		return false
	}
	deliverPendingApprovalChoice(pending, resolved)
	return true
}

// findPendingApprovalTextLocked 查找同一用户中唯一支持该文本选项的审批。
func (h *Handler) findPendingApprovalTextLocked(userID string, choice string) (*pendingApproval, string, bool) {
	var found *pendingApproval
	resolvedChoice := ""
	for _, pending := range h.pendingApprovals {
		if pending.userID != strings.TrimSpace(userID) {
			continue
		}
		resolved := pending.resolveChoice(choice)
		if resolved == "" {
			continue
		}
		if found != nil {
			return nil, "", true
		}
		found = pending
		resolvedChoice = resolved
	}
	return found, resolvedChoice, false
}

// deliverPendingApprovalChoice 非阻塞提交审批结果，避免重复平台回调卡住消息处理。
func deliverPendingApprovalChoice(pending *pendingApproval, choice string) {
	select {
	case pending.choices <- choice:
	default:
	}
}

func (h *Handler) findPendingApprovalLocked(userID string, approvalKey string) *pendingApproval {
	if key := pendingApprovalMapKey(userID, approvalKey); key != "" {
		return h.pendingApprovals[key]
	}
	var found *pendingApproval
	for _, pending := range h.pendingApprovals {
		if pending.userID != strings.TrimSpace(userID) {
			continue
		}
		if found != nil {
			return nil
		}
		found = pending
	}
	return found
}

func (p *pendingApproval) resolveChoice(choice string) string {
	if p == nil {
		return ""
	}
	choice = strings.TrimSpace(choice)
	if p.allowed[choice] {
		return choice
	}
	if resolved := p.aliases[strings.ToLower(choice)]; resolved != "" {
		return resolved
	}
	return ""
}

// isApprovalChoiceCommand 判断卡片动作是否来自 Codex 审批按钮，避免过期审批落入普通消息队列。
func isApprovalChoiceCommand(cmd *platform.CardAction) bool {
	return cmd != nil &&
		cmd.Action == "choice" &&
		strings.TrimSpace(cmd.Value["approval_key"]) != ""
}

func reportCardActionResult(cmd *platform.CardAction, result platform.CardActionResult) bool {
	if cmd == nil || cmd.Result == nil {
		return false
	}
	select {
	case cmd.Result <- result:
	default:
	}
	return true
}
