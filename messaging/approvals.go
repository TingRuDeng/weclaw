package messaging

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type pendingApproval struct {
	choices   chan string
	allowed   map[string]bool
	aliases   map[string]string
	key       string
	userID    string
	route     string
	kind      string
	yolo      string
	deny      string
	code      string
	expiresAt time.Time
	resolved  atomic.Bool
}

type approvalCodeConsumeResult uint8

const (
	approvalCodeNotFound approvalCodeConsumeResult = iota
	approvalCodeConsumed
	approvalCodeAlreadyResolved
	approvalCodeDecisionUnavailable
)

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
		approvalKey := approvalPendingKey(req.RequestID)
		choices := approvalChoices(
			req.Options, approvalKey, taskCardIDFromReplier(opts.reply),
			opts.actorUserID, opts.routeUserID, opts.agentName,
		)
		if len(choices) == 0 {
			return "", fmt.Errorf("approval request has no options")
		}
		if h.isYoloMode(approvalModeKey(opts.actorUserID, opts.routeUserID)) {
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
		prompt := approvalPromptWithTextFallback(approvalPrompt(req, opts.agentName), pending)
		if err := opts.reply.AskChoices(ctx, prompt, choices); err != nil {
			return "", err
		}
		wait := time.Until(pending.expiresAt)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
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
		choices:   make(chan string, 1),
		allowed:   approvalOptionSet(options),
		aliases:   approvalOptionAliases(options),
		key:       pendingApprovalMapKey(userID, routeUserID, interactionKind, approvalKey),
		userID:    strings.TrimSpace(userID),
		route:     strings.TrimSpace(routeUserID),
		kind:      strings.TrimSpace(interactionKind),
		yolo:      strings.TrimSpace(yoloDecision),
		deny:      defaultDenyApprovalOption(options),
		expiresAt: time.Now().Add(pendingApprovalTimeout),
	}
	h.pendingApprovalsMu.Lock()
	h.cleanupResolvedApprovalCodesLocked(time.Now())
	if h.pendingApprovals == nil {
		h.pendingApprovals = make(map[string]*pendingApproval)
	}
	if pending.kind == platform.ChoiceInteractionApproval {
		code, err := h.newApprovalCodeLocked(pending.userID, pending.route)
		if err != nil {
			h.pendingApprovalsMu.Unlock()
			return nil, err
		}
		pending.code = code
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
	if pending == nil {
		return
	}
	h.pendingApprovalsMu.Lock()
	if h.pendingApprovals[pending.key] == pending {
		delete(h.pendingApprovals, pending.key)
	}
	if pending.resolved.Load() && pending.code != "" {
		if h.resolvedApprovalCodes == nil {
			h.resolvedApprovalCodes = make(map[string]time.Time)
		}
		h.resolvedApprovalCodes[approvalCodeMapKey(pending.userID, pending.route, pending.code)] = time.Now().Add(pendingApprovalTimeout)
	}
	h.cleanupResolvedApprovalCodesLocked(time.Now())
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

func (h *Handler) consumePendingApprovalCode(userID string, routeUserID string, code string, approve bool) approvalCodeConsumeResult {
	userID = strings.TrimSpace(userID)
	routeUserID = strings.TrimSpace(routeUserID)
	code = normalizeApprovalCode(code)
	if userID == "" || code == "" {
		return approvalCodeNotFound
	}
	h.pendingApprovalsMu.Lock()
	h.cleanupResolvedApprovalCodesLocked(time.Now())
	if expiresAt := h.resolvedApprovalCodes[approvalCodeMapKey(userID, routeUserID, code)]; !expiresAt.IsZero() {
		h.pendingApprovalsMu.Unlock()
		return approvalCodeAlreadyResolved
	}
	var found *pendingApproval
	now := time.Now()
	for _, pending := range h.pendingApprovals {
		if pending.userID == userID && pending.route == routeUserID &&
			pending.kind == platform.ChoiceInteractionApproval && pending.code == code {
			if !pending.expiresAt.After(now) {
				break
			}
			found = pending
			break
		}
	}
	h.pendingApprovalsMu.Unlock()
	if found == nil {
		return approvalCodeNotFound
	}
	choice := found.deny
	if approve {
		choice = found.yolo
	}
	if strings.TrimSpace(choice) == "" {
		return approvalCodeDecisionUnavailable
	}
	if !deliverPendingApprovalChoice(found, choice) {
		if found.resolved.Load() {
			return approvalCodeAlreadyResolved
		}
		return approvalCodeNotFound
	}
	return approvalCodeConsumed
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

// resolvePendingApprovalsForYolo 只放行当前操作者在当前窗口切换前已经弹出的授权请求。
func (h *Handler) resolvePendingApprovalsForYolo(actorUserID string, routeUserID string) int {
	actorUserID = strings.TrimSpace(actorUserID)
	routeUserID = strings.TrimSpace(routeUserID)
	if actorUserID == "" || routeUserID == "" {
		return 0
	}
	h.pendingApprovalsMu.Lock()
	pending := make([]*pendingApproval, 0)
	for _, item := range h.pendingApprovals {
		if item.userID == actorUserID && item.route == routeUserID && item.yolo != "" {
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

var approvalCodeEncoding = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

func (h *Handler) newApprovalCodeLocked(userID string, routeUserID string) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		raw := make([]byte, 5)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate approval fallback code: %w", err)
		}
		code := approvalCodeEncoding.EncodeToString(raw)
		if !h.approvalCodeInUseLocked(userID, routeUserID, code) {
			return code, nil
		}
	}
	return "", fmt.Errorf("generate unique approval fallback code")
}

func (h *Handler) approvalCodeInUseLocked(userID string, routeUserID string, code string) bool {
	code = normalizeApprovalCode(code)
	for _, pending := range h.pendingApprovals {
		if pending.userID == userID && pending.route == routeUserID && pending.code == code {
			return true
		}
	}
	_, resolved := h.resolvedApprovalCodes[approvalCodeMapKey(userID, routeUserID, code)]
	return resolved
}

func (h *Handler) cleanupResolvedApprovalCodesLocked(now time.Time) {
	for key, expiresAt := range h.resolvedApprovalCodes {
		if !expiresAt.After(now) {
			delete(h.resolvedApprovalCodes, key)
		}
	}
}

func approvalCodeMapKey(userID string, routeUserID string, code string) string {
	return strings.Join([]string{
		strings.TrimSpace(userID),
		strings.TrimSpace(routeUserID),
		normalizeApprovalCode(code),
	}, "\x00")
}

func normalizeApprovalCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 8 {
		return ""
	}
	for _, char := range code {
		if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZ23456789", char) {
			return ""
		}
	}
	return code
}

func approvalPromptWithTextFallback(prompt string, pending *pendingApproval) string {
	if pending == nil || pending.code == "" {
		return prompt
	}
	commands := make([]string, 0, 2)
	if pending.yolo != "" {
		commands = append(commands, "/approve "+pending.code)
	}
	if pending.deny != "" {
		commands = append(commands, "/deny "+pending.code)
	}
	if len(commands) == 0 {
		return prompt
	}
	return strings.TrimSpace(prompt) + "\n\n按钮不可用时，可发送：" + strings.Join(commands, " 或 ")
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
