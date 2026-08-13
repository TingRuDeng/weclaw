package messaging

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type pendingApproval struct {
	choices           chan string
	stateChanges      chan agent.ApprovalRequestState
	allowed           map[string]bool
	aliases           map[string]string
	key               string
	userID            string
	route             string
	kind              string
	yolo              string
	deny              string
	code              string
	expiresAt         time.Time
	deadlineMu        sync.Mutex
	stateProbe        agent.ApprovalRequestStateProbe
	automaticRecorder automaticApprovalRecorder
	automaticPrompt   string
	automaticChoice   platform.Choice
	stateRecorder     approvalStateRecorder
	stateChoices      []platform.Choice
	displayReady      chan struct{}
	resolved          atomic.Bool
}

// automaticApprovalRecorder 是飞书等平台可选实现的展示能力；审批真值仍由 Agent 决策通道负责。
type automaticApprovalRecorder interface {
	RecordAutomaticApproval(context.Context, string, platform.Choice) error
}

type approvalStateRecorder interface {
	RecordApprovalState(context.Context, string, []platform.Choice, agent.ApprovalRequestState) error
}

type pendingApprovalDisplay struct {
	recorder automaticApprovalRecorder
	prompt   string
	choice   platform.Choice
}

const automaticApprovalDisplayTimeout = 10 * time.Second

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
		if !opts.lease.begin() {
			return "", agent.ErrCodexObserverDetached
		}
		defer opts.lease.end()
		ctx, cancelInteraction := opts.lease.bindContext(ctx)
		defer cancelInteraction()
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
		prompt := approvalPrompt(req, opts.agentName)
		yoloDecision := autoApproveApprovalOption(req.Options)
		modeKey := approvalModeKey(opts.actorUserID, opts.routeUserID)
		if h.isYoloMode(modeKey) {
			log.Printf("[handler] yolo mode auto-approving sensitive operation for %s -> %q", opts.actorUserID, yoloDecision)
			h.auditRecord(auditEntry{User: opts.actorUserID, Action: "approval_auto_yolo", Summary: yoloDecision})
			if choice, ok := approvalChoiceForDecision(choices, yoloDecision); ok {
				h.recordAutomaticApprovalAsync(ctx, opts.reply, prompt, choice, opts.actorUserID, yoloDecision)
			}
			return yoloDecision, nil
		}
		display := pendingApprovalDisplay{prompt: prompt}
		if recorder, ok := optionalAutomaticApprovalRecorder(opts.reply); ok {
			display.recorder = recorder
			display.choice, _ = approvalChoiceForDecision(choices, yoloDecision)
		}
		pending, err := h.registerPendingApprovalForRouteWithDisplay(
			opts.actorUserID, opts.routeUserID, approvalKey, req.Options,
			yoloDecision, platform.ChoiceInteractionApproval, display,
		)
		if err != nil {
			return "", err
		}
		h.pendingApprovalsMu.Lock()
		pending.stateProbe = req.StateProbe
		if recorder, ok := optionalApprovalStateRecorder(opts.reply); ok {
			pending.stateRecorder = recorder
			pending.stateChoices = append([]platform.Choice(nil), choices...)
			pending.automaticPrompt = prompt
		}
		h.pendingApprovalsMu.Unlock()
		defer h.clearPendingApproval(opts.actorUserID, pending)
		// 关闭“初次检查 default、注册前恰好切到 yolo”的窗口；此时尚未发卡。
		if h.isYoloMode(modeKey) {
			resolvedHere := deliverPendingApprovalChoice(pending, yoloDecision)
			pending.markDisplayReady()
			if resolvedHere {
				log.Printf("[handler] yolo mode auto-approving newly pending sensitive operation for %s -> %q", opts.actorUserID, yoloDecision)
				h.auditRecord(auditEntry{User: opts.actorUserID, Action: "approval_auto_yolo", Summary: yoloDecision})
				if choice, ok := approvalChoiceForDecision(choices, yoloDecision); ok {
					h.recordAutomaticApprovalAsync(ctx, opts.reply, prompt, choice, opts.actorUserID, yoloDecision)
				}
			}
			return yoloDecision, nil
		}
		prompt = approvalPromptWithTextFallback(prompt, pending)
		askErr := opts.reply.AskChoices(ctx, prompt, choices)
		pending.markDisplayReady()
		if askErr != nil {
			if opts.lease.isDetached() {
				return "", agent.ErrCodexObserverDetached
			}
			select {
			case choice := <-pending.choices:
				return strings.TrimSpace(choice), nil
			default:
			}
			return "", askErr
		}
		return h.waitForPendingApproval(ctx, opts, req, pending)
	}
}

func (h *Handler) waitForPendingApproval(ctx context.Context, opts agentInteractionContextOptions, req agent.ApprovalRequest, pending *pendingApproval) (string, error) {
	for {
		wait := time.Until(pending.deadline())
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case choice := <-pending.choices:
			timer.Stop()
			return strings.TrimSpace(choice), nil
		case state := <-pending.stateChanges:
			timer.Stop()
			switch state {
			case agent.ApprovalRequestStateResolvedExternally:
				return "", agent.ErrApprovalResolvedExternally
			case agent.ApprovalRequestStateTurnTerminal:
				return "", agent.ErrApprovalTurnTerminal
			default:
				pending.renewDeadline()
				continue
			}
		case <-timer.C:
			if pending.stateProbe == nil {
				h.auditDefaultDenyApproval(opts, "timeout")
				return defaultDenyApprovalOption(req.Options), nil
			}
			state, err := pending.stateProbe(ctx)
			switch {
			case err != nil || state == agent.ApprovalRequestStateUnknown:
				pending.renewDeadline()
				h.auditRecord(auditEntry{User: opts.actorUserID, Agent: strings.TrimSpace(opts.agentName), Action: "approval_state_review_failed", Summary: "decision=pending reason=state_unavailable"})
				continue
			case state == agent.ApprovalRequestStatePending:
				pending.renewDeadline()
				h.auditRecord(auditEntry{User: opts.actorUserID, Agent: strings.TrimSpace(opts.agentName), Action: "approval_state_reviewed", Summary: "decision=pending"})
				continue
			case state == agent.ApprovalRequestStateResolvedExternally:
				if pending.resolved.CompareAndSwap(false, true) {
					h.recordPendingApprovalStateAsync(ctx, pending, state, opts.actorUserID)
					h.auditRecord(auditEntry{User: opts.actorUserID, Agent: strings.TrimSpace(opts.agentName), Action: "approval_resolved_externally", Summary: "decision=handled_elsewhere"})
				}
				return "", agent.ErrApprovalResolvedExternally
			case state == agent.ApprovalRequestStateTurnTerminal:
				if pending.resolved.CompareAndSwap(false, true) {
					h.recordPendingApprovalStateAsync(ctx, pending, state, opts.actorUserID)
					h.auditRecord(auditEntry{User: opts.actorUserID, Agent: strings.TrimSpace(opts.agentName), Action: "approval_turn_terminal", Summary: "decision=not_sent"})
				}
				return "", agent.ErrApprovalTurnTerminal
			default:
				pending.renewDeadline()
			}
		case <-opts.lease.done():
			timer.Stop()
			return "", agent.ErrCodexObserverDetached
		case <-ctx.Done():
			timer.Stop()
			if opts.lease.isDetached() {
				return "", agent.ErrCodexObserverDetached
			}
			if pending.stateProbe != nil {
				return "", ctx.Err()
			}
			h.auditDefaultDenyApproval(opts, "context_cancelled")
			return defaultDenyApprovalOption(req.Options), ctx.Err()
		}
	}
}

func (h *Handler) auditDefaultDenyApproval(opts agentInteractionContextOptions, reason string) {
	h.auditRecord(auditEntry{
		User: opts.actorUserID, Agent: strings.TrimSpace(opts.agentName), Action: "approval_default_deny",
		Summary: "decision=deny reason=" + strings.TrimSpace(reason),
	})
}

func (h *Handler) registerPendingApproval(userID string, approvalKey string, options []agent.ApprovalOption) (*pendingApproval, error) {
	return h.registerPendingApprovalForRoute(userID, "", approvalKey, options, "", "")
}

func (h *Handler) registerPendingApprovalForRoute(userID string, routeUserID string, approvalKey string, options []agent.ApprovalOption, yoloDecision string, interactionKind string) (*pendingApproval, error) {
	return h.registerPendingApprovalForRouteWithDisplay(
		userID, routeUserID, approvalKey, options, yoloDecision, interactionKind, pendingApprovalDisplay{},
	)
}

func (h *Handler) registerPendingApprovalForRouteWithDisplay(userID string, routeUserID string, approvalKey string, options []agent.ApprovalOption, yoloDecision string, interactionKind string, display pendingApprovalDisplay) (*pendingApproval, error) {
	pending := &pendingApproval{
		choices:           make(chan string, 1),
		stateChanges:      make(chan agent.ApprovalRequestState, 1),
		allowed:           approvalOptionSet(options),
		aliases:           approvalOptionAliases(options),
		key:               pendingApprovalMapKey(userID, routeUserID, interactionKind, approvalKey),
		userID:            strings.TrimSpace(userID),
		route:             strings.TrimSpace(routeUserID),
		kind:              strings.TrimSpace(interactionKind),
		yolo:              strings.TrimSpace(yoloDecision),
		deny:              defaultDenyApprovalOption(options),
		expiresAt:         time.Now().Add(pendingApprovalTimeout),
		stateProbe:        nil,
		automaticRecorder: display.recorder,
		automaticPrompt:   strings.TrimSpace(display.prompt),
		automaticChoice:   display.choice,
	}
	if pending.automaticRecorder != nil && strings.TrimSpace(pending.automaticChoice.ID) != "" {
		pending.displayReady = make(chan struct{})
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

type pendingApprovalRouteState uint8

const (
	pendingApprovalRouteNone pendingApprovalRouteState = iota
	pendingApprovalRouteWaiting
	pendingApprovalRouteResolvedExternally
	pendingApprovalRouteTerminal
	pendingApprovalRouteUnavailable
)

func (h *Handler) reviewPendingApprovalForRoute(ctx context.Context, userID string, routeUserID string) pendingApprovalRouteState {
	userID = strings.TrimSpace(userID)
	routeUserID = strings.TrimSpace(routeUserID)
	h.pendingApprovalsMu.Lock()
	pendingItems := make([]*pendingApproval, 0)
	for _, pending := range h.pendingApprovals {
		if pending == nil || pending.userID != userID || pending.route != routeUserID ||
			pending.kind != platform.ChoiceInteractionApproval || pending.resolved.Load() || pending.stateProbe == nil {
			continue
		}
		pendingItems = append(pendingItems, pending)
	}
	h.pendingApprovalsMu.Unlock()
	result := pendingApprovalRouteNone
	for _, pending := range pendingItems {
		state, err := pending.stateProbe(ctx)
		switch {
		case err != nil || state == agent.ApprovalRequestStateUnknown:
			return pendingApprovalRouteUnavailable
		case state == agent.ApprovalRequestStatePending:
			pending.renewDeadline()
			if result != pendingApprovalRouteTerminal {
				result = pendingApprovalRouteWaiting
			}
		case state == agent.ApprovalRequestStateResolvedExternally:
			if deliverPendingApprovalState(pending, state) {
				h.recordPendingApprovalStateAsync(ctx, pending, state, userID)
			}
			if result == pendingApprovalRouteNone {
				result = pendingApprovalRouteResolvedExternally
			}
		case state == agent.ApprovalRequestStateTurnTerminal:
			if deliverPendingApprovalState(pending, state) {
				h.recordPendingApprovalStateAsync(ctx, pending, state, userID)
			}
			result = pendingApprovalRouteTerminal
		}
	}
	return result
}

func (h *Handler) recordPendingApprovalStateAsync(ctx context.Context, pending *pendingApproval, state agent.ApprovalRequestState, actorUserID string) {
	if pending == nil || pending.stateRecorder == nil {
		return
	}
	go func() {
		displayCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeContext(ctx)), automaticApprovalDisplayTimeout)
		defer cancel()
		if err := pending.stateRecorder.RecordApprovalState(displayCtx, pending.automaticPrompt, pending.stateChoices, state); err != nil && !errors.Is(err, platform.ErrUnsupported) {
			log.Printf("[handler] approval state display update failed for %s: %v", actorUserID, err)
			h.auditRecord(auditEntry{User: actorUserID, Action: "approval_state_display_failed", Summary: "reason=card_update_failed"})
		}
	}()
}

func optionalApprovalStateRecorder(reply platform.Replier) (approvalStateRecorder, bool) {
	if serialized, ok := reply.(*serializedReplier); ok {
		recorder, supported := optionalApprovalStateRecorder(serialized.inner)
		if !supported {
			return nil, false
		}
		return serializedApprovalStateRecorder{reply: serialized, recorder: recorder}, true
	}
	recorder, ok := reply.(approvalStateRecorder)
	return recorder, ok
}

type serializedApprovalStateRecorder struct {
	reply    *serializedReplier
	recorder approvalStateRecorder
}

func (s serializedApprovalStateRecorder) RecordApprovalState(ctx context.Context, prompt string, choices []platform.Choice, state agent.ApprovalRequestState) error {
	s.reply.mu.Lock()
	defer s.reply.mu.Unlock()
	return s.recorder.RecordApprovalState(ctx, prompt, choices, state)
}

func deliverPendingApprovalState(pending *pendingApproval, state agent.ApprovalRequestState) bool {
	if pending == nil || !pending.resolved.CompareAndSwap(false, true) {
		return false
	}
	select {
	case pending.stateChanges <- state:
		return true
	default:
		return false
	}
}

func (p *pendingApproval) deadline() time.Time {
	p.deadlineMu.Lock()
	defer p.deadlineMu.Unlock()
	return p.expiresAt
}

func (p *pendingApproval) renewDeadline() {
	p.deadlineMu.Lock()
	p.expiresAt = time.Now().Add(pendingApprovalTimeout)
	p.deadlineMu.Unlock()
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

func (h *Handler) hasPendingInteractionForRoute(userID string, routeUserID string) bool {
	userID = strings.TrimSpace(userID)
	routeUserID = strings.TrimSpace(routeUserID)
	now := time.Now()
	h.pendingApprovalsMu.Lock()
	defer h.pendingApprovalsMu.Unlock()
	for _, pending := range h.pendingApprovals {
		if pending == nil || pending.userID != userID || pending.route != routeUserID ||
			pending.resolved.Load() || !pending.deadline().After(now) {
			continue
		}
		return true
	}
	return false
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
			if !pending.deadline().After(now) {
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

func (p *pendingApproval) markDisplayReady() {
	if p != nil && p.displayReady != nil {
		close(p.displayReady)
	}
}

func (p *pendingApproval) recordAutomaticApproval(ctx context.Context) error {
	if p == nil || p.automaticRecorder == nil || strings.TrimSpace(p.automaticChoice.ID) == "" {
		return platform.ErrUnsupported
	}
	if p.displayReady != nil {
		select {
		case <-p.displayReady:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.automaticRecorder.RecordAutomaticApproval(ctx, p.automaticPrompt, p.automaticChoice)
}

// resolvePendingApprovalsForYolo 只放行当前操作者在当前窗口切换前已经弹出的授权请求。
func (h *Handler) resolvePendingApprovalsForYolo(ctx context.Context, actorUserID string, routeUserID string) (int, int) {
	actorUserID = strings.TrimSpace(actorUserID)
	routeUserID = strings.TrimSpace(routeUserID)
	if actorUserID == "" || routeUserID == "" {
		return 0, 0
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
	displayFailures := 0
	for _, item := range pending {
		if !deliverPendingApprovalChoice(item, item.yolo) {
			continue
		}
		resolved++
		log.Printf("[handler] yolo mode resolved pending sensitive operation for %s -> %q", item.userID, item.yolo)
		h.auditRecord(auditEntry{User: item.userID, Action: "approval_auto_yolo", Summary: item.yolo})
		if err := item.recordAutomaticApproval(ctx); err != nil && !errors.Is(err, platform.ErrUnsupported) {
			displayFailures++
			log.Printf("[handler] yolo approval display update failed for %s: %v", item.userID, err)
			h.auditRecord(auditEntry{User: item.userID, Action: "approval_auto_yolo_display_failed", Summary: automaticApprovalDisplayFailureSummary(item.yolo, err)})
		}
	}
	return resolved, displayFailures
}

func recordAutomaticApproval(ctx context.Context, reply platform.Replier, prompt string, choice platform.Choice) error {
	recorder, ok := optionalAutomaticApprovalRecorder(reply)
	if !ok {
		return platform.ErrUnsupported
	}
	return recorder.RecordAutomaticApproval(ctx, prompt, choice)
}

func (h *Handler) recordAutomaticApprovalAsync(ctx context.Context, reply platform.Replier, prompt string, choice platform.Choice, actorUserID string, decision string) {
	go func() {
		displayCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeContext(ctx)), automaticApprovalDisplayTimeout)
		defer cancel()
		if err := recordAutomaticApproval(displayCtx, reply, prompt, choice); err != nil && !errors.Is(err, platform.ErrUnsupported) {
			log.Printf("[handler] yolo approval display update failed for %s: %v", actorUserID, err)
			h.auditRecord(auditEntry{User: actorUserID, Action: "approval_auto_yolo_display_failed", Summary: automaticApprovalDisplayFailureSummary(decision, err)})
		}
	}()
}

func automaticApprovalDisplayFailureSummary(decision string, err error) string {
	reason := "card_update_failed"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "card_update_timeout"
	case errors.Is(err, context.Canceled):
		reason = "card_update_cancelled"
	}
	return "decision=" + strings.TrimSpace(decision) + " reason=" + reason
}

func optionalAutomaticApprovalRecorder(reply platform.Replier) (automaticApprovalRecorder, bool) {
	if serialized, ok := reply.(*serializedReplier); ok {
		recorder, supported := optionalAutomaticApprovalRecorder(serialized.inner)
		if !supported {
			return nil, false
		}
		return serializedAutomaticApprovalRecorder{reply: serialized, recorder: recorder}, true
	}
	recorder, ok := reply.(automaticApprovalRecorder)
	return recorder, ok
}

type serializedAutomaticApprovalRecorder struct {
	reply    *serializedReplier
	recorder automaticApprovalRecorder
}

func (s serializedAutomaticApprovalRecorder) RecordAutomaticApproval(ctx context.Context, prompt string, choice platform.Choice) error {
	s.reply.mu.Lock()
	defer s.reply.mu.Unlock()
	return s.recorder.RecordAutomaticApproval(ctx, prompt, choice)
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

func isPendingApprovalChoiceCommand(cmd *platform.CardAction) bool {
	return isPendingInteractionChoiceCommand(cmd) &&
		strings.TrimSpace(cmd.Value[platform.ChoiceMetadataInteractionKind]) == platform.ChoiceInteractionApproval
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
