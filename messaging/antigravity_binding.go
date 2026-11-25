package messaging

import (
	"context"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/antigravity"
	"github.com/fastclaw-ai/weclaw/platform"
)

// AntigravityClient is the private sidecar surface consumed by messaging.
type AntigravityClient interface {
	Health(ctx context.Context) (antigravity.Health, error)
	ListConversations(ctx context.Context) ([]antigravity.Conversation, error)
	GetConversation(ctx context.Context, conversationID string) (antigravity.Conversation, antigravity.Baseline, error)
	CreateConversation(ctx context.Context, req antigravity.CreateConversationRequest) (antigravity.Conversation, antigravity.Baseline, error)
	SendMessage(ctx context.Context, conversationID string, content string) error
	Observe(ctx context.Context, conversationID, fileID string, offset int64) (antigravity.ObserveResult, error)
}

const (
	antigravityAgentKind         = "antigravity"
	antigravityPendingNewTTL     = 5 * time.Minute
	antigravityObserveRunning    = time.Second
	antigravityObserveIdle       = 3 * time.Second
	antigravityDetachTimeout     = 2 * time.Second
	antigravityStopTimeout       = 3 * time.Second
	antigravityProgressTitleText = "Antigravity"
	antigravityFeishuPrefix      = "[WeClaw/飞书] "
)

type antigravityBinding struct {
	conversationID     string
	projectID          string
	projectName        string
	title              string
	generation         uint64
	authorizedIdentity string
	deliveryRoute      platform.DeliveryRoute
	fallbackReply      platform.Replier
	observer           *antigravityObserver
}

type antigravityPendingNew struct {
	token       string
	accountID   string
	actorUserID string
	routeUserID string
	body        string
	expiresAt   time.Time
}

// SetAntigravityClient injects the sidecar client; a nil client leaves /ag
// unavailable without affecting other agents.
func (h *Handler) SetAntigravityClient(client AntigravityClient) {
	h.antigravityMu.Lock()
	h.antigravityClient = client
	h.antigravityMu.Unlock()
}

func (h *Handler) antigravityClientSnapshot() (AntigravityClient, bool) {
	h.antigravityMu.Lock()
	defer h.antigravityMu.Unlock()
	if h.antigravityClient == nil {
		return nil, false
	}
	return h.antigravityClient, true
}

func antigravityBindingKey(accountID, routeUserID string) string {
	return strings.TrimSpace(accountID) + "\x00" + strings.TrimSpace(routeUserID)
}

// isFeishuPrivateMessage is the single gate for Antigravity commands and
// plain-text forwarding.
func isFeishuPrivateMessage(msg platform.IncomingMessage, routeUserID string) bool {
	return msg.Platform == platform.PlatformFeishu && isPrivatePlatformMessage(msg, routeUserID)
}

func (h *Handler) hasAntigravityBinding(accountID, routeUserID string) bool {
	h.antigravityMu.Lock()
	defer h.antigravityMu.Unlock()
	return h.antigravityBindings[antigravityBindingKey(accountID, routeUserID)] != nil
}

func (h *Handler) antigravityBindingSnapshot(accountID, routeUserID string) (antigravityBinding, bool) {
	h.antigravityMu.Lock()
	defer h.antigravityMu.Unlock()
	binding := h.antigravityBindings[antigravityBindingKey(accountID, routeUserID)]
	if binding == nil {
		return antigravityBinding{}, false
	}
	return *binding, true
}

func (h *Handler) nextAntigravityGenerationLocked(key string) uint64 {
	if h.antigravityGenerations == nil {
		h.antigravityGenerations = make(map[string]uint64)
	}
	h.antigravityGenerations[key]++
	return h.antigravityGenerations[key]
}

// clearAntigravityBinding mutes the window immediately. The old observer is
// invalidated by generation and detaches its progress card on exit.
func (h *Handler) clearAntigravityBinding(accountID, routeUserID string) {
	key := antigravityBindingKey(accountID, routeUserID)
	h.antigravityMu.Lock()
	binding := h.antigravityBindings[key]
	delete(h.antigravityBindings, key)
	h.nextAntigravityGenerationLocked(key)
	h.antigravityMu.Unlock()
	if binding != nil && binding.observer != nil {
		binding.observer.cancel()
	}
	// 屏障等待仍在途的旧投递退出，并在返回前收敛旧流式卡。
	h.antigravityDeliveryMu.Lock()
	if binding != nil && binding.observer != nil {
		binding.observer.detachStream()
	}
	h.antigravityDeliveryMu.Unlock()
}

// StopAntigravityObservers cancels every observer and waits a bounded time so
// service shutdown never blocks on sidecar delivery.
func (h *Handler) StopAntigravityObservers() {
	h.antigravityMu.Lock()
	observers := make([]*antigravityObserver, 0, len(h.antigravityBindings))
	for _, binding := range h.antigravityBindings {
		if binding.observer != nil {
			observers = append(observers, binding.observer)
		}
	}
	h.antigravityMu.Unlock()
	for _, observer := range observers {
		observer.cancel()
	}
	deadline := time.After(antigravityStopTimeout)
	for _, observer := range observers {
		select {
		case <-observer.done:
		case <-deadline:
			return
		}
	}
}

func antigravityDeliveryRoute(msg platform.IncomingMessage, reply platform.Replier) platform.DeliveryRoute {
	if reporter, ok := reply.(platform.DeliveryRouteReporter); ok {
		if route := reporter.DeliveryRoute(); route.Valid() {
			return route
		}
	}
	return platform.DeliveryRoute{
		Platform: msg.Platform, AccountID: msg.AccountID, ChatID: msg.ChatID, ReplyToID: msg.ReplyToID,
	}
}

func (h *Handler) installAntigravityBinding(
	msg platform.IncomingMessage,
	routeUserID string,
	reply platform.Replier,
	conversation antigravity.Conversation,
	baseline antigravity.Baseline,
) *antigravityObserver {
	key := antigravityBindingKey(msg.AccountID, routeUserID)
	identity, _ := msg.AuthorizedIdentity()
	h.antigravityMu.Lock()
	old := h.antigravityBindings[key]
	generation := h.nextAntigravityGenerationLocked(key)
	if h.antigravityBindings == nil {
		h.antigravityBindings = make(map[string]*antigravityBinding)
	}
	observer := newAntigravityObserver(h, key, generation, msg.AccountID, routeUserID, identity,
		antigravityDeliveryRoute(msg, reply), reply, conversation.ID, conversation.ProjectName, conversation.Title)
	binding := &antigravityBinding{
		conversationID: conversation.ID, projectID: conversation.ProjectID, projectName: conversation.ProjectName,
		title: conversation.Title, generation: generation, authorizedIdentity: identity,
		deliveryRoute: observer.deliveryRoute, fallbackReply: reply, observer: observer,
	}
	h.antigravityBindings[key] = binding
	h.antigravityMu.Unlock()
	if old != nil && old.observer != nil {
		old.observer.cancel()
	}
	// 屏障保证切换返回前旧观察器停止投递并收敛旧进度卡。
	h.antigravityDeliveryMu.Lock()
	if old != nil && old.observer != nil {
		old.observer.detachStream()
	}
	h.antigravityDeliveryMu.Unlock()
	go observer.run(baseline.FileID, baseline.Offset)
	return observer
}

// antigravityDelivery revalidates generation and, when a registry is present,
// the stored identity allowlist before every progress or terminal write.
func (h *Handler) antigravityDelivery(observer *antigravityObserver) (platform.Replier, bool) {
	h.antigravityMu.Lock()
	binding := h.antigravityBindings[observer.key]
	valid := binding != nil && binding.generation == observer.generation && binding.observer == observer
	h.antigravityMu.Unlock()
	if !valid {
		return nil, false
	}
	h.mu.RLock()
	registry := h.platformRegistry
	h.mu.RUnlock()
	if registry == nil {
		return observer.fallbackReply, observer.fallbackReply != nil
	}
	if !registry.AllowsStoredIdentity(platform.PlatformFeishu, observer.accountID, []string{observer.authorizedIdentity}) {
		return nil, false
	}
	reply, ok := registry.ReplierForRoute(observer.deliveryRoute)
	if !ok || reply == nil {
		return nil, false
	}
	return reply, true
}

func (h *Handler) storeAntigravityPending(record antigravityPendingNew) {
	now := time.Now()
	h.antigravityMu.Lock()
	if h.antigravityPending == nil {
		h.antigravityPending = make(map[string]antigravityPendingNew)
	}
	for token, pending := range h.antigravityPending {
		if !pending.expiresAt.After(now) {
			delete(h.antigravityPending, token)
		}
	}
	h.antigravityPending[record.token] = record
	h.antigravityMu.Unlock()
}

func (h *Handler) consumeAntigravityPending(token, accountID, actorUserID, routeUserID string) (antigravityPendingNew, bool) {
	h.antigravityMu.Lock()
	defer h.antigravityMu.Unlock()
	record, ok := h.antigravityPending[token]
	if !ok {
		return antigravityPendingNew{}, false
	}
	if record.accountID != accountID || record.actorUserID != actorUserID || record.routeUserID != routeUserID {
		return antigravityPendingNew{}, false
	}
	delete(h.antigravityPending, token)
	if !record.expiresAt.After(time.Now()) {
		return antigravityPendingNew{}, false
	}
	return record, true
}

func antigravityProjectToken(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "outside-of-project"
	}
	return projectID
}

func antigravityProjectIDFromToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "outside-of-project" {
		return ""
	}
	return token
}

func antigravityProjectLabel(project antigravity.Project) string {
	if strings.TrimSpace(project.Name) != "" {
		return project.Name
	}
	return antigravity.ProjectName(nil, project.ID)
}

func antigravityDisplayTitle(conversation antigravity.Conversation) string {
	if title := strings.TrimSpace(conversation.Title); title != "" {
		return title
	}
	return conversation.ID
}

func antigravityProgressTitle(projectName, title string) string {
	parts := []string{antigravityProgressTitleText}
	if strings.TrimSpace(projectName) != "" {
		parts = append(parts, projectName)
	}
	if strings.TrimSpace(title) != "" {
		parts = append(parts, title)
	}
	return strings.Join(parts, " · ")
}
