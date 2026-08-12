package messaging

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func codexFollowerFromAcquire(req codexSessionAcquireRequest) (*codexFrontendFollower, bool) {
	if req.platform != platform.PlatformFeishu || req.reply == nil {
		return nil, false
	}
	authorizedIdentity := strings.TrimSpace(req.authorizedIdentity)
	if authorizedIdentity == "" {
		return nil, false
	}
	reporter, ok := optionalDeliveryRouteReporter(progressReplier(req.reply))
	if !ok {
		return nil, false
	}
	route := reporter.DeliveryRoute()
	if route.Platform == "" {
		route.Platform = req.platform
	}
	if strings.TrimSpace(route.AccountID) == "" {
		route.AccountID = strings.TrimSpace(req.accountID)
	}
	if route.Platform != platform.PlatformFeishu || !route.Valid() {
		return nil, false
	}
	follower := normalizeCodexFrontendFollower(&codexFrontendFollower{
		WorkspaceRoot:      req.route.workspaceRoot,
		ThreadID:           req.route.threadID,
		ActorUserID:        firstNonBlank(req.actorUserID, req.routeUserID),
		AuthorizedIdentity: authorizedIdentity,
		DeliveryRoute:      route,
		UpdatedAt:          time.Now().UTC().Format(time.RFC3339),
	})
	return follower, follower != nil
}

func cloneCodexFrontendFollower(source *codexFrontendFollower) *codexFrontendFollower {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func normalizeCodexFrontendFollower(source *codexFrontendFollower) *codexFrontendFollower {
	if source == nil {
		return nil
	}
	follower := *source
	follower.WorkspaceRoot = normalizeCodexWorkspaceRoot(follower.WorkspaceRoot)
	follower.ThreadID = strings.TrimSpace(follower.ThreadID)
	follower.ActorUserID = strings.TrimSpace(follower.ActorUserID)
	follower.AuthorizedIdentity = strings.TrimSpace(follower.AuthorizedIdentity)
	follower.DeliveryRoute.AccountID = strings.TrimSpace(follower.DeliveryRoute.AccountID)
	follower.DeliveryRoute.ChatID = strings.TrimSpace(follower.DeliveryRoute.ChatID)
	follower.DeliveryRoute.ReplyToID = strings.TrimSpace(follower.DeliveryRoute.ReplyToID)
	follower.UpdatedAt = strings.TrimSpace(follower.UpdatedAt)
	if follower.WorkspaceRoot == "" || follower.ThreadID == "" ||
		follower.ActorUserID == "" || follower.AuthorizedIdentity == "" || !follower.DeliveryRoute.Valid() {
		return nil
	}
	return &follower
}

func sameCodexFrontendFollower(left *codexFrontendFollower, right *codexFrontendFollower) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameCodexFrontendFollowerIdentity(left *codexFrontendFollower, right *codexFrontendFollower) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.WorkspaceRoot == right.WorkspaceRoot && left.ThreadID == right.ThreadID &&
		left.ActorUserID == right.ActorUserID && left.AuthorizedIdentity == right.AuthorizedIdentity &&
		sameDeliveryEndpoint(left.DeliveryRoute, right.DeliveryRoute)
}

func routeUserIDFromCodexBindingKey(bindingKey string) string {
	parts := strings.SplitN(strings.TrimSpace(bindingKey), "\x00", 2)
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func (s *codexSessionStore) followerSnapshots() []codexFollowerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.followerSnapshotsLocked()
}

func (s *codexSessionStore) followerSnapshot(bindingKey string) (codexFollowerSnapshot, bool) {
	bindingKey = strings.TrimSpace(bindingKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, snapshot := range s.followerSnapshotsLocked() {
		if snapshot.BindingKey == bindingKey {
			return snapshot, true
		}
	}
	return codexFollowerSnapshot{}, false
}

type codexFollowerTurnDeliveryClaim struct {
	snapshot    codexFollowerSnapshot
	deliveryKey string
}

func (h *Handler) codexFollowerTurnClaim(
	bindingKey string,
	conversationID string,
	threadID string,
	turnID string,
) (codexFollowerTurnDeliveryClaim, bool) {
	snapshot, ok := h.ensureCodexSessions().followerSnapshot(bindingKey)
	if !ok || snapshot.ConversationID != strings.TrimSpace(conversationID) ||
		strings.TrimSpace(snapshot.Target.ThreadID) != strings.TrimSpace(threadID) ||
		strings.TrimSpace(turnID) == "" {
		return codexFollowerTurnDeliveryClaim{}, false
	}
	return codexFollowerTurnDeliveryClaim{
		snapshot: snapshot, deliveryKey: codexFollowerTerminalOutboxID(snapshot, turnID),
	}, true
}

func (h *Handler) claimCodexFollowerTurnForTask(
	bindingKey string,
	conversationID string,
	threadID string,
	turnID string,
	task *activeAgentTask,
) error {
	claim, ok := h.codexFollowerTurnClaim(bindingKey, conversationID, threadID, turnID)
	if !ok {
		return nil
	}
	if task != nil {
		task.setTerminalDeliveryKey(claim.deliveryKey)
		task.setTerminalDeliveryGuard(terminalDeliveryGuardFromFollower(claim.snapshot))
	}
	if err := h.ensureCodexSessions().commitFollowerTurnPending(claim.snapshot, turnID); err != nil {
		if errors.Is(err, errCodexRemoteSelectionChanged) {
			return nil
		}
		return err
	}
	return nil
}

func terminalDeliveryGuardFromFollower(snapshot codexFollowerSnapshot) terminalDeliveryGuard {
	return terminalDeliveryGuard{
		AuthorizedIdentity: snapshot.Target.AuthorizedIdentity,
		FollowerBindingKey: snapshot.BindingKey,
		FollowerRevision:   snapshot.Revision,
		FollowerThreadID:   snapshot.Target.ThreadID,
	}
}

func (h *Handler) codexFollowerDeliveryGuardForTask(task *activeAgentTask) terminalDeliveryGuard {
	if h == nil || task == nil {
		return terminalDeliveryGuard{}
	}
	task.mu.Lock()
	bindingKey := codexBindingKey(task.routeUserID, task.agentName)
	threadID := strings.TrimSpace(task.codexThreadID)
	task.mu.Unlock()
	snapshot, ok := h.ensureCodexSessions().followerSnapshot(bindingKey)
	if !ok || strings.TrimSpace(snapshot.Target.ThreadID) != threadID {
		return terminalDeliveryGuard{}
	}
	return terminalDeliveryGuardFromFollower(snapshot)
}

func (s *codexSessionStore) followerSnapshotsLocked() []codexFollowerSnapshot {
	snapshots := make([]codexFollowerSnapshot, 0)
	for bindingKey, binding := range s.bindings {
		follower := normalizeCodexFrontendFollower(binding.Follower)
		if follower == nil {
			continue
		}
		session := binding.Workspaces[follower.WorkspaceRoot]
		if codexWorkspaceReleaseIntent(session) || strings.TrimSpace(session.ThreadID) != follower.ThreadID {
			continue
		}
		if _, archived := s.archived[follower.ThreadID]; archived {
			continue
		}
		routeUserID := routeUserIDFromCodexBindingKey(bindingKey)
		agentName := agentNameFromBindingKey(bindingKey)
		snapshots = append(snapshots, codexFollowerSnapshot{
			BindingKey: bindingKey, RouteUserID: routeUserID, AgentName: agentName,
			ConversationID:        buildCodexConversationID(routeUserID, agentName, follower.WorkspaceRoot),
			Revision:              binding.FollowRevision,
			FollowTurnID:          strings.TrimSpace(binding.FollowTurnID),
			FollowTurnInitialized: binding.FollowTurnInitialized,
			FollowTurnPending:     binding.FollowTurnPending,
			RecoveryThreadID:      strings.TrimSpace(session.FirstTurnRecoveryThreadID),
			RecoveryReservationID: strings.TrimSpace(session.FirstTurnRecoveryReservationID),
			Target:                *follower,
		})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].BindingKey < snapshots[j].BindingKey })
	return snapshots
}

func (s *codexSessionStore) followerMatches(snapshot codexFollowerSnapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.followerMatchesLocked(snapshot)
}

func (h *Handler) terminalOutboxDeliveryAllowed(registry *platform.Registry, entry *terminalOutboxEntry) bool {
	allowed, _ := h.terminalOutboxDeliveryDecision(registry, entry)
	return allowed
}

func (h *Handler) terminalOutboxDeliveryDecision(registry *platform.Registry, entry *terminalOutboxEntry) (bool, string) {
	if entry == nil {
		return false, "entry_missing"
	}
	guard := terminalDeliveryGuardFromEntry(entry)
	if guard.empty() {
		return true, ""
	}
	if !guard.complete() {
		return false, "guard_incomplete"
	}
	if registry == nil {
		return false, "registry_unavailable"
	}
	if !registry.AllowsStoredIdentity(entry.Route.Platform, entry.Route.AccountID, []string{guard.AuthorizedIdentity}) {
		return false, "identity_not_allowed"
	}
	snapshot, ok := h.ensureCodexSessions().followerSnapshot(guard.FollowerBindingKey)
	if !ok {
		return false, "follower_missing"
	}
	if snapshot.Revision != guard.FollowerRevision {
		return false, "revision_changed"
	}
	if strings.TrimSpace(snapshot.Target.ThreadID) != guard.FollowerThreadID {
		return false, "thread_changed"
	}
	if strings.TrimSpace(snapshot.Target.AuthorizedIdentity) != guard.AuthorizedIdentity {
		return false, "identity_changed"
	}
	if !sameDeliveryEndpoint(snapshot.Target.DeliveryRoute, entry.Route) {
		return false, "route_changed"
	}
	return true, ""
}

func (s *codexSessionStore) followerMatchesLocked(snapshot codexFollowerSnapshot) bool {
	binding := s.bindings[snapshot.BindingKey]
	session := binding.Workspaces[snapshot.Target.WorkspaceRoot]
	return binding.FollowRevision == snapshot.Revision &&
		sameCodexFrontendFollower(binding.Follower, &snapshot.Target) &&
		strings.TrimSpace(binding.FollowTurnID) == strings.TrimSpace(snapshot.FollowTurnID) &&
		binding.FollowTurnInitialized == snapshot.FollowTurnInitialized &&
		binding.FollowTurnPending == snapshot.FollowTurnPending &&
		strings.TrimSpace(session.FirstTurnRecoveryThreadID) == strings.TrimSpace(snapshot.RecoveryThreadID) &&
		strings.TrimSpace(session.FirstTurnRecoveryReservationID) == strings.TrimSpace(snapshot.RecoveryReservationID)
}

// clearFollowerIfMatches 按 revision 和投递身份 CAS 清除一个已撤权的主动观察端点。
// workspace/thread 选择仍保留；重新授权后必须由用户再次选择才会重新建立同步。
func (s *codexSessionStore) clearFollowerIfMatches(snapshot codexFollowerSnapshot) (bool, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[snapshot.BindingKey]
	if !ok || binding.FollowRevision != snapshot.Revision ||
		!sameCodexFrontendFollower(binding.Follower, &snapshot.Target) {
		return false, nil
	}
	nextBindings := cloneCodexSessionBindings(s.bindings)
	binding = nextBindings[snapshot.BindingKey]
	binding.Follower = nil
	binding.FollowRevision++
	clearCodexFollowerTurnState(&binding)
	nextBindings[snapshot.BindingKey] = binding
	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: now,
	}); err != nil {
		return false, fmt.Errorf("保存 Codex follower 撤权: %w", err)
	}
	s.bindings = nextBindings
	return true, nil
}

// commitFollowerTurnClaim 在 outbox 或观察任务取得可靠投递责任后，原子推进 durable follower 游标。
func (s *codexSessionStore) commitFollowerTurnClaim(snapshot codexFollowerSnapshot, turnID string) error {
	return s.commitFollowerTurnState(snapshot, turnID, true, false)
}

// commitFollowerTurnPending 记录该 route 已发现 active turn，但尚未取得持久终态投递责任。
func (s *codexSessionStore) commitFollowerTurnPending(snapshot codexFollowerSnapshot, turnID string) error {
	return s.commitFollowerTurnState(snapshot, turnID, true, true)
}

func (s *codexSessionStore) commitFollowerTurnState(
	snapshot codexFollowerSnapshot,
	turnID string,
	initialized bool,
	pending bool,
) error {
	turnID = strings.TrimSpace(turnID)
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.followerMatchesLocked(snapshot) {
		return errCodexRemoteSelectionChanged
	}
	nextBindings := cloneCodexSessionBindings(s.bindings)
	binding := nextBindings[snapshot.BindingKey]
	binding.FollowTurnID = turnID
	binding.FollowTurnInitialized = initialized
	binding.FollowTurnPending = pending
	nextBindings[snapshot.BindingKey] = binding
	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: now,
	}); err != nil {
		return err
	}
	s.bindings = nextBindings
	return nil
}

type codexReleasedFollowerSnapshot struct {
	AgentName             string
	ConversationID        string
	ThreadID              string
	RecoveryThreadID      string
	RecoveryReservationID string
	Committed             bool
}

// releasedFollowerSnapshots 为崩溃恢复提供解绑墓碑；不包含平台凭据或消息正文。
func (s *codexSessionStore) releasedFollowerSnapshots() []codexReleasedFollowerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releasedFollowerSnapshotsLocked(false)
}

// followerRecoverySnapshots 在同一锁快照中返回活动 follower 与所有解绑意图，
// 避免 reconciler 在两次读取之间把正在提交的解绑 recovery 错误放行。
func (s *codexSessionStore) followerRecoverySnapshots() ([]codexFollowerSnapshot, []codexReleasedFollowerSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.followerSnapshotsLocked(), s.releasedFollowerSnapshotsLocked(true)
}

func (s *codexSessionStore) releasedFollowerSnapshotsLocked(includePending bool) []codexReleasedFollowerSnapshot {
	var snapshots []codexReleasedFollowerSnapshot
	for bindingKey, binding := range s.bindings {
		routeUserID := routeUserIDFromCodexBindingKey(bindingKey)
		agentName := agentNameFromBindingKey(bindingKey)
		if routeUserID == "" || agentName == "" {
			continue
		}
		for workspaceRoot, session := range binding.Workspaces {
			threadID := strings.TrimSpace(session.ReleasedThreadID)
			if (!session.Released && !(includePending && session.ReleasePending)) || threadID == "" {
				continue
			}
			snapshots = append(snapshots, codexReleasedFollowerSnapshot{
				AgentName:             agentName,
				ConversationID:        buildCodexConversationID(routeUserID, agentName, workspaceRoot),
				ThreadID:              threadID,
				RecoveryThreadID:      strings.TrimSpace(session.ReleasedRecoveryThreadID),
				RecoveryReservationID: strings.TrimSpace(session.ReleasedRecoveryReservationID),
				Committed:             session.Released,
			})
		}
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].ConversationID != snapshots[j].ConversationID {
			return snapshots[i].ConversationID < snapshots[j].ConversationID
		}
		return snapshots[i].ThreadID < snapshots[j].ThreadID
	})
	return snapshots
}
