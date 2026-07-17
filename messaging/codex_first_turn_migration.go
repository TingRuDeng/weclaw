package messaging

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const legacyCodexFirstTurnMigrationWindow = 10 * time.Minute

// migrateLegacyCodexPendingFirstTurns 只迁移 v2 中“thread 刚创建即提交首次 remote owner”的会话。
// v3 起必须依赖 /cx new 原子写入的明确 PendingFirstTurn，不能再从空 rollout 泛化推断。
func migrateLegacyCodexPendingFirstTurns(
	version int,
	bindings map[string]codexSessionBinding,
	controls map[string]codexControlIntent,
) bool {
	if version != 2 {
		return false
	}
	changed := false
	for bindingKey, binding := range bindings {
		for workspaceRoot, session := range binding.Workspaces {
			threadID := strings.TrimSpace(session.ThreadID)
			intent := controls[threadID]
			if session.PendingNewThread || session.PendingFirstTurn || intent.Owner != codexControlRemote ||
				intent.RouteBindingKey != bindingKey || intent.Revision != 1 ||
				!legacyCodexThreadCreatedWithSelection(threadID, session.UpdatedAt, intent.UpdatedAt) {
				continue
			}
			session.PendingFirstTurn = true
			binding.Workspaces[workspaceRoot] = session
			changed = true
		}
		bindings[bindingKey] = binding
	}
	return changed
}

func legacyCodexThreadCreatedWithSelection(threadID string, sessionUpdatedAt string, ownerUpdatedAt string) bool {
	createdAt, ok := codexUUIDv7Time(threadID)
	if !ok {
		return false
	}
	sessionAt, sessionErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(sessionUpdatedAt))
	ownerAt, ownerErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(ownerUpdatedAt))
	if sessionErr != nil || ownerErr != nil {
		return false
	}
	return timeDistance(createdAt, sessionAt) <= legacyCodexFirstTurnMigrationWindow &&
		timeDistance(createdAt, ownerAt) <= legacyCodexFirstTurnMigrationWindow
}

func codexUUIDv7Time(threadID string) (time.Time, bool) {
	compact := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(threadID), "-", ""))
	if len(compact) != 32 || compact[12] != '7' || !strings.ContainsRune("89ab", rune(compact[16])) {
		return time.Time{}, false
	}
	if _, err := hex.DecodeString(compact); err != nil {
		return time.Time{}, false
	}
	millis, err := strconv.ParseInt(compact[:12], 16, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(millis).UTC(), true
}

func timeDistance(left time.Time, right time.Time) time.Duration {
	distance := left.Sub(right)
	if distance < 0 {
		return -distance
	}
	return distance
}
