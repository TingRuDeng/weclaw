package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type pendingApproval struct {
	choices  chan string
	allowed  map[string]bool
	aliases  map[string]string
	key      string
	userID   string
	route    string
	kind     string
	yolo     string
	resolved atomic.Bool
}

type approvalTextConsumeResult uint8

const (
	approvalTextUnmatched approvalTextConsumeResult = iota
	approvalTextConsumed
	approvalTextAmbiguous
)

func (h *Handler) approvalHandlerForUser(userID string, routeUserID string, reply platform.Replier) agent.ApprovalHandler {
	return h.approvalHandlerForRoute(agentInteractionContextOptions{
		actorUserID: userID, routeUserID: routeUserID, reply: reply,
	})
}

func (h *Handler) approvalHandlerForRoute(opts agentInteractionContextOptions) agent.ApprovalHandler {
	return func(ctx context.Context, req agent.ApprovalRequest) (string, error) {
		if err := validateAgentInteractionRoute(opts); err != nil {
			return "", err
		}
		prompt := approvalPrompt(req, opts.agentName)
		approvalKey := approvalPendingKey(req.RequestID)
		choices := approvalChoices(
			req.Options, approvalKey, taskCardIDFromReplier(opts.reply),
			opts.actorUserID, opts.routeUserID, opts.agentName,
		)
		if len(choices) == 0 {
			return "", fmt.Errorf("approval request has no options")
		}
		if h.isYoloMode(opts.routeUserID) {
			decision := autoApproveApprovalOption(req.Options)
			log.Printf("[handler] yolo mode auto-approving sensitive operation for %s -> %q", opts.actorUserID, decision)
			h.auditRecord(auditEntry{User: opts.actorUserID, Action: "approval_auto_yolo", Summary: decision})
			return decision, nil
		}
		pending, err := h.registerPendingApprovalForRoute(
			opts.actorUserID, opts.routeUserID, approvalKey, req.Options,
			autoApproveApprovalOption(req.Options), platform.ChoiceInteractionApproval,
		)
		if err != nil {
			return "", err
		}
		defer h.clearPendingApproval(opts.actorUserID, pending)
		if err := opts.reply.AskChoices(ctx, prompt, choices); err != nil {
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
	return h.registerPendingApprovalForRoute(userID, "", approvalKey, options, "", "")
}

func (h *Handler) registerPendingApprovalForRoute(userID string, routeUserID string, approvalKey string, options []agent.ApprovalOption, yoloDecision string, interactionKind string) (*pendingApproval, error) {
	pending := &pendingApproval{
		choices: make(chan string, 1),
		allowed: approvalOptionSet(options),
		aliases: approvalOptionAliases(options),
		key:     pendingApprovalMapKey(userID, routeUserID, interactionKind, approvalKey),
		userID:  strings.TrimSpace(userID),
		route:   strings.TrimSpace(routeUserID),
		kind:    strings.TrimSpace(interactionKind),
		yolo:    strings.TrimSpace(yoloDecision),
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
	return h.consumePendingApprovalText(userID, userID, choice) == approvalTextConsumed
}

// consumePendingApprovalText 只消费唯一匹配的文本审批，多个匹配项交给调用方提示用户选择卡片。
func (h *Handler) consumePendingApprovalText(userID string, routeUserID string, choice string) approvalTextConsumeResult {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return approvalTextUnmatched
	}
	h.pendingApprovalsMu.Lock()
	pending, resolved, ambiguous := h.findPendingApprovalTextLocked(userID, routeUserID, choice)
	h.pendingApprovalsMu.Unlock()
	if ambiguous {
		return approvalTextAmbiguous
	}
	if pending == nil {
		return approvalTextUnmatched
	}
	if !deliverPendingApprovalChoice(pending, resolved) {
		return approvalTextUnmatched
	}
	return approvalTextConsumed
}

func (h *Handler) consumePendingApprovalForKey(userID string, approvalKey string, choice string) bool {
	return h.consumePendingInteractionForKey(userID, userID, "", approvalKey, choice)
}

func (h *Handler) consumePendingInteractionForKey(userID string, routeUserID string, interactionKind string, approvalKey string, choice string) bool {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return false
	}
	h.pendingApprovalsMu.Lock()
	pending := h.findPendingApprovalLocked(userID, routeUserID, interactionKind, approvalKey)
	h.pendingApprovalsMu.Unlock()
	if pending == nil {
		return false
	}
	resolved := pending.resolveChoice(choice)
	if resolved == "" {
		return false
	}
	return deliverPendingApprovalChoice(pending, resolved)
}

// findPendingApprovalTextLocked 查找同一用户中唯一支持该文本选项的审批。
func (h *Handler) findPendingApprovalTextLocked(userID string, routeUserID string, choice string) (*pendingApproval, string, bool) {
	var found *pendingApproval
	resolvedChoice := ""
	for _, pending := range h.pendingApprovals {
		if pending.userID != strings.TrimSpace(userID) || pending.route != strings.TrimSpace(routeUserID) {
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
func deliverPendingApprovalChoice(pending *pendingApproval, choice string) bool {
	if pending == nil || strings.TrimSpace(choice) == "" || !pending.resolved.CompareAndSwap(false, true) {
		return false
	}
	select {
	case pending.choices <- choice:
		return true
	default:
		return false
	}
}

// resolvePendingApprovalsForYolo 放行当前窗口切换前已经弹出的授权请求；结构化提问不会进入该集合。
func (h *Handler) resolvePendingApprovalsForYolo(routeUserID string) int {
	routeUserID = strings.TrimSpace(routeUserID)
	if routeUserID == "" {
		return 0
	}
	h.pendingApprovalsMu.Lock()
	pending := make([]*pendingApproval, 0)
	for _, item := range h.pendingApprovals {
		if item.route == routeUserID && item.yolo != "" {
			pending = append(pending, item)
		}
	}
	h.pendingApprovalsMu.Unlock()
	resolved := 0
	for _, item := range pending {
		if !deliverPendingApprovalChoice(item, item.yolo) {
			continue
		}
		resolved++
		log.Printf("[handler] yolo mode resolved pending sensitive operation for %s -> %q", item.userID, item.yolo)
		h.auditRecord(auditEntry{User: item.userID, Action: "approval_auto_yolo", Summary: item.yolo})
	}
	return resolved
}

func (h *Handler) findPendingApprovalLocked(userID string, routeUserID string, interactionKind string, approvalKey string) *pendingApproval {
	if interactionKind = strings.TrimSpace(interactionKind); interactionKind != "" {
		if key := pendingApprovalMapKey(userID, routeUserID, interactionKind, approvalKey); key != "" {
			return h.pendingApprovals[key]
		}
	}
	var found *pendingApproval
	for _, pending := range h.pendingApprovals {
		if pending.userID != strings.TrimSpace(userID) ||
			pending.route != strings.TrimSpace(routeUserID) ||
			(interactionKind != "" && pending.kind != interactionKind) {
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

func isPendingInteractionChoiceCommand(cmd *platform.CardAction) bool {
	if cmd == nil || cmd.Action != "choice" || strings.TrimSpace(cmd.Value["approval_key"]) == "" {
		return false
	}
	switch strings.TrimSpace(cmd.Value[platform.ChoiceMetadataInteractionKind]) {
	case platform.ChoiceInteractionApproval, platform.ChoiceInteractionUserInput:
		return true
	default:
		return false
	}
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
