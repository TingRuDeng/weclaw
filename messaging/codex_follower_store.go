package messaging

import (
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func codexFollowerFromAcquire(req codexSessionAcquireRequest) (*codexFrontendFollower, bool) {
	if req.platform != platform.PlatformFeishu || req.reply == nil {
		return nil, false
	}
	reporter, ok := optionalDeliveryRouteReporter(req.reply)
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
		WorkspaceRoot: req.route.workspaceRoot,
		ThreadID:      req.route.threadID,
		ActorUserID:   firstNonBlank(req.actorUserID, req.routeUserID),
		DeliveryRoute: route,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
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
	follower.DeliveryRoute.AccountID = strings.TrimSpace(follower.DeliveryRoute.AccountID)
	follower.DeliveryRoute.ChatID = strings.TrimSpace(follower.DeliveryRoute.ChatID)
	follower.DeliveryRoute.ReplyToID = strings.TrimSpace(follower.DeliveryRoute.ReplyToID)
	follower.UpdatedAt = strings.TrimSpace(follower.UpdatedAt)
	if follower.WorkspaceRoot == "" || follower.ThreadID == "" ||
		follower.ActorUserID == "" || !follower.DeliveryRoute.Valid() {
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
	binding := s.bindings[snapshot.BindingKey]
	session := binding.Workspaces[snapshot.Target.WorkspaceRoot]
	return binding.FollowRevision == snapshot.Revision &&
		sameCodexFrontendFollower(binding.Follower, &snapshot.Target) &&
		strings.TrimSpace(session.FirstTurnRecoveryThreadID) == strings.TrimSpace(snapshot.RecoveryThreadID) &&
		strings.TrimSpace(session.FirstTurnRecoveryReservationID) == strings.TrimSpace(snapshot.RecoveryReservationID)
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
