package messaging

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type codexControlOwner string

const (
	codexControlUnclaimed codexControlOwner = "unclaimed"
	codexControlDesktop   codexControlOwner = "desktop"
	codexControlRemote    codexControlOwner = "remote"
)

var (
	errCodexControlRevisionChanged = errors.New("Codex 控制权版本已变化")
	errCodexControlRouteRequired   = errors.New("远程控制权缺少消息窗口")
)

type codexControlIntent struct {
	Owner           codexControlOwner `json:"owner"`
	RouteBindingKey string            `json:"routeBindingKey,omitempty"`
	ConversationID  string            `json:"conversationId,omitempty"`
	Revision        uint64            `json:"revision"`
	UpdatedAt       string            `json:"updatedAt,omitempty"`
}

type codexControlIntentUpdate struct {
	ThreadID         string
	Owner            codexControlOwner
	RouteBindingKey  string
	ConversationID   string
	ExpectedRevision uint64
}

// controlIntent 返回 thread 当前控制意图；未配置时保持显式未认领。
func (s *codexSessionStore) controlIntent(threadID string) codexControlIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, ok := s.controls[strings.TrimSpace(threadID)]
	if !ok {
		return codexControlIntent{Owner: codexControlUnclaimed}
	}
	return intent
}

// updateControlIntent 通过 revision 原子更新控制方，避免并发窗口互相覆盖。
func (s *codexSessionStore) updateControlIntent(update codexControlIntentUpdate) (codexControlIntent, error) {
	threadID := strings.TrimSpace(update.ThreadID)
	intent, err := normalizeCodexControlIntent(update)
	if err != nil {
		return codexControlIntent{}, err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	current, existed := s.controls[threadID]
	if current.Revision != update.ExpectedRevision {
		s.mu.Unlock()
		return current, errCodexControlRevisionChanged
	}
	intent.Revision = current.Revision + 1
	intent.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.controls[threadID] = intent
	s.mu.Unlock()
	if err := s.persistStateLocked(); err != nil {
		s.rollbackControlIntent(threadID, current, existed)
		return current, fmt.Errorf("保存 Codex 控制权: %w", err)
	}
	return intent, nil
}

// rollbackControlIntent 仅在持有 saveMu 时回滚本次未落盘的控制意图。
func (s *codexSessionStore) rollbackControlIntent(threadID string, current codexControlIntent, existed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existed {
		s.controls[threadID] = current
		return
	}
	delete(s.controls, threadID)
}

// normalizeCodexControlIntent 校验来自命令或状态文件的控制意图边界。
func normalizeCodexControlIntent(update codexControlIntentUpdate) (codexControlIntent, error) {
	if strings.TrimSpace(update.ThreadID) == "" {
		return codexControlIntent{}, fmt.Errorf("Codex thread 不能为空")
	}
	intent := codexControlIntent{Owner: update.Owner}
	switch update.Owner {
	case codexControlUnclaimed, codexControlDesktop:
		return intent, nil
	case codexControlRemote:
		intent.RouteBindingKey = strings.TrimSpace(update.RouteBindingKey)
		intent.ConversationID = strings.TrimSpace(update.ConversationID)
		if intent.RouteBindingKey == "" || intent.ConversationID == "" {
			return codexControlIntent{}, errCodexControlRouteRequired
		}
		return intent, nil
	default:
		return codexControlIntent{}, fmt.Errorf("无效的 Codex 控制方 %q", update.Owner)
	}
}
