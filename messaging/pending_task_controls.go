package messaging

import (
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const (
	pendingTaskControlTTL         = 2 * time.Hour
	pendingTaskControlTokenPrefix = "@task_"
)

type pendingTaskControlScope struct {
	AccountID   string
	ActorUserID string
	RouteUserID string
}

type pendingTaskControlExpectation struct {
	taskID             string
	pendingRevision    string
	pendingFingerprint string
}

func (e pendingTaskControlExpectation) empty() bool {
	return strings.TrimSpace(e.taskID) == "" &&
		strings.TrimSpace(e.pendingRevision) == "" &&
		strings.TrimSpace(e.pendingFingerprint) == ""
}

type pendingTaskControlRecord struct {
	scope          pendingTaskControlScope
	executionKey   string
	agentName      string
	guideSupported bool
	expectation    pendingTaskControlExpectation
	expiresAt      time.Time
}

type pendingTaskControlStore struct {
	mu      sync.Mutex
	records map[string]pendingTaskControlRecord
	now     func() time.Time
}

func (s *pendingTaskControlStore) issue(record pendingTaskControlRecord) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowOrDefault()
	s.purgeExpiredLocked(now)
	if s.records == nil {
		s.records = make(map[string]pendingTaskControlRecord)
	}
	token := pendingTaskControlTokenPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	record.scope = normalizePendingTaskControlScope(record.scope)
	record.executionKey = strings.TrimSpace(record.executionKey)
	record.agentName = strings.TrimSpace(record.agentName)
	record.expiresAt = now.Add(pendingTaskControlTTL)
	s.records[token] = record
	return token
}

func (s *pendingTaskControlStore) load(token string, scope pendingTaskControlScope) (pendingTaskControlRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowOrDefault()
	s.purgeExpiredLocked(now)
	record, ok := s.records[strings.TrimSpace(token)]
	if !ok || record.scope != normalizePendingTaskControlScope(scope) {
		return pendingTaskControlRecord{}, false
	}
	return record, true
}

func (s *pendingTaskControlStore) purgeExpiredLocked(now time.Time) {
	for token, record := range s.records {
		if !record.expiresAt.After(now) {
			delete(s.records, token)
		}
	}
}

func (s *pendingTaskControlStore) nowOrDefault() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func normalizePendingTaskControlScope(scope pendingTaskControlScope) pendingTaskControlScope {
	scope.AccountID = strings.TrimSpace(scope.AccountID)
	scope.ActorUserID = strings.TrimSpace(scope.ActorUserID)
	scope.RouteUserID = strings.TrimSpace(scope.RouteUserID)
	return scope
}

func (h *Handler) sendPendingTaskControlCard(notice agentTaskAdmissionNotice) bool {
	if notice.platformName != platform.PlatformFeishu || notice.reply == nil ||
		!notice.reply.Capabilities().Buttons || notice.task == nil {
		return false
	}
	notice.task.mu.Lock()
	if notice.task.pending.message == "" ||
		notice.task.owner != strings.TrimSpace(notice.userID) ||
		notice.task.routeUserID != strings.TrimSpace(notice.routeUserID) {
		notice.task.mu.Unlock()
		return false
	}
	record := pendingTaskControlRecord{
		scope: pendingTaskControlScope{
			AccountID: notice.accountID, ActorUserID: notice.userID, RouteUserID: notice.routeUserID,
		},
		executionKey:   notice.executionKey,
		agentName:      notice.agentName,
		guideSupported: notice.guideSupported,
		expectation: pendingTaskControlExpectation{
			taskID:             notice.task.taskID,
			pendingRevision:    notice.task.pending.controlRevision,
			pendingFingerprint: normalizedTextFingerprint(notice.task.pending.message),
		},
	}
	preview := previewPendingCodexMessage(notice.task.pending.message)
	notice.task.mu.Unlock()

	token := h.pendingTaskControls.issue(record)
	metadata := map[string]string{
		platform.ChoiceMetadataInteractionKind:  platform.ChoiceInteractionTaskControl,
		platform.ChoiceMetadataAgentName:        agentDisplayName(notice.agentName),
		platform.ChoiceMetadataTaskControlToken: token,
		feishuSessionMetadataKey:                notice.routeUserID,
	}
	choices := make([]platform.Choice, 0, 3)
	if notice.guideSupported {
		choices = append(choices, platform.Choice{
			ID: "/guide", Label: "作为引导发送", Metadata: metadata,
		})
	}
	choices = append(choices,
		platform.Choice{
			ID: "/cancel", Label: "撤回暂存消息",
			Metadata: mergeChoiceMetadata(metadata, map[string]string{
				platform.ChoiceMetadataButtonType: platform.ChoiceButtonTypeDefault,
			}),
		},
		platform.Choice{
			ID: "/stop", Label: "停止当前任务",
			Metadata: mergeChoiceMetadata(metadata, map[string]string{
				platform.ChoiceMetadataButtonType: platform.ChoiceButtonTypeDefault,
			}),
		},
	)
	prompt := "新消息已暂存。无需操作，当前任务结束后会自动执行。"
	if preview != "" {
		prompt += "\n\n消息：" + preview
	}
	prompt += "\n\n如需改变默认处理方式，可选择以下操作："
	return notice.reply.AskChoices(notice.ctx, prompt, choices) == nil
}

func (h *Handler) handlePendingTaskControlChoice(runtime platformMessageRuntime) bool {
	command := runtime.msg.RawCommand
	if command == nil || command.Action != "choice" ||
		command.Value[platform.ChoiceMetadataInteractionKind] != platform.ChoiceInteractionTaskControl {
		return false
	}
	record, ok := h.pendingTaskControls.load(
		command.Value[platform.ChoiceMetadataTaskControlToken],
		pendingTaskControlScope{
			AccountID: runtime.msg.AccountID, ActorUserID: runtime.msg.UserID, RouteUserID: runtime.routeUserID,
		},
	)
	if !ok || !h.matchesPendingTaskControl(record) {
		runtime.sendText("该暂存消息已处理，或操作卡片已经过期。")
		return true
	}
	req := taskCommandRequest{
		ctx: runtime.ctx, platformName: runtime.msg.Platform, accountID: runtime.msg.AccountID,
		actorUserID: runtime.msg.UserID, routeUserID: runtime.routeUserID, reply: runtime.reply,
		targetKey: record.executionKey, targetAgentName: record.agentName, expectation: record.expectation,
	}
	switch strings.TrimSpace(command.Value["choice"]) {
	case "/guide":
		if !record.guideSupported {
			runtime.sendText("当前任务不支持引导操作。")
			return true
		}
		h.handleGuideCommand(req)
	case "/cancel":
		runtime.sendText(h.handleCancelPendingGuide(req))
	case "/stop":
		runtime.sendText(h.handleStopActiveTask(req))
	default:
		runtime.sendText("无法识别该任务操作。")
	}
	return true
}

func (h *Handler) matchesPendingTaskControl(record pendingTaskControlRecord) bool {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	task := h.tasks.active[record.executionKey]
	if task == nil {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.matchesPendingTaskControlLocked(record.expectation)
}

func (t *activeAgentTask) matchesPendingTaskControlLocked(expectation pendingTaskControlExpectation) bool {
	if expectation.empty() {
		return true
	}
	return strings.TrimSpace(t.taskID) == strings.TrimSpace(expectation.taskID) &&
		t.pending.message != "" &&
		strings.TrimSpace(t.pending.controlRevision) == strings.TrimSpace(expectation.pendingRevision) &&
		normalizedTextFingerprint(t.pending.message) == strings.TrimSpace(expectation.pendingFingerprint)
}

func ensurePendingTaskControlRevision(pending pendingAgentTask) pendingAgentTask {
	if strings.TrimSpace(pending.message) != "" && strings.TrimSpace(pending.controlRevision) == "" {
		pending.controlRevision = uuid.NewString()
	}
	return pending
}
