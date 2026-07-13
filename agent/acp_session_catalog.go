package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type acpSessionListParams struct {
	Cursor string `json:"cursor,omitempty"`
}

type acpSessionListResult struct {
	Sessions   []acpSessionListEntry `json:"sessions"`
	NextCursor json.RawMessage       `json:"nextCursor"`
}

type acpSessionListEntry struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type acpSessionResumeParams struct {
	SessionID  string        `json:"sessionId"`
	Cwd        string        `json:"cwd"`
	McpServers []interface{} `json:"mcpServers"`
}

// ListClaudeSessions 遍历标准 ACP session/list 的全部分页。
func (a *ACPAgent) ListClaudeSessions(ctx context.Context) ([]ClaudeSession, error) {
	if err := a.ensureClaudeSessionCatalogReady(ctx); err != nil {
		return nil, err
	}
	cursor := ""
	seen := make(map[string]struct{})
	seenSessions := make(map[string]struct{})
	sessions := make([]ClaudeSession, 0)
	for {
		page, err := a.listClaudeSessionPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if err := appendUniqueClaudeSessions(&sessions, page.Sessions, seenSessions); err != nil {
			return nil, err
		}
		if page.NextCursor == "" {
			return sessions, nil
		}
		if _, exists := seen[page.NextCursor]; exists {
			return nil, fmt.Errorf("session/list 返回重复游标 %q", page.NextCursor)
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

// appendUniqueClaudeSessions 拒绝跨页重复 ID，避免目录条目归属产生歧义。
func appendUniqueClaudeSessions(target *[]ClaudeSession, page []ClaudeSession, seen map[string]struct{}) error {
	for _, session := range page {
		if _, exists := seen[session.ID]; exists {
			return fmt.Errorf("session/list 返回重复 sessionId %q", session.ID)
		}
		seen[session.ID] = struct{}{}
		*target = append(*target, session)
	}
	return nil
}

type claudeSessionPage struct {
	Sessions   []ClaudeSession
	NextCursor string
}

func (a *ACPAgent) listClaudeSessionPage(ctx context.Context, cursor string) (claudeSessionPage, error) {
	result, err := a.rpc(ctx, "session/list", acpSessionListParams{Cursor: cursor})
	if err != nil {
		return claudeSessionPage{}, fmt.Errorf("session/list 请求失败: %w", err)
	}
	var page acpSessionListResult
	if err := json.Unmarshal(result, &page); err != nil {
		return claudeSessionPage{}, fmt.Errorf("解析 session/list 响应失败: %w", err)
	}
	if page.Sessions == nil {
		return claudeSessionPage{}, fmt.Errorf("session/list 响应缺少 sessions 数组")
	}
	sessions, err := validateClaudeSessionEntries(page.Sessions)
	if err != nil {
		return claudeSessionPage{}, err
	}
	nextCursor, err := validateNextCursor(page.NextCursor)
	if err != nil {
		return claudeSessionPage{}, err
	}
	return claudeSessionPage{Sessions: sessions, NextCursor: nextCursor}, nil
}

func validateClaudeSessionEntries(entries []acpSessionListEntry) ([]ClaudeSession, error) {
	sessions := make([]ClaudeSession, 0, len(entries))
	for index, entry := range entries {
		trimmedID := strings.TrimSpace(entry.SessionID)
		if trimmedID == "" {
			return nil, fmt.Errorf("session/list sessions[%d].sessionId 不能为空", index)
		}
		if trimmedID != entry.SessionID {
			return nil, fmt.Errorf("session/list sessions[%d].sessionId 不能包含首尾空白", index)
		}
		if err := validateACPSessionCwd(entry.Cwd); err != nil {
			return nil, fmt.Errorf("session/list sessions[%d].cwd 无效: %w", index, err)
		}
		sessions = append(sessions, ClaudeSession{
			ID: entry.SessionID, Cwd: entry.Cwd, Title: entry.Title, UpdatedAt: entry.UpdatedAt,
		})
	}
	return sessions, nil
}

func validateACPSessionCwd(cwd string) error {
	if strings.TrimSpace(cwd) == "" {
		return fmt.Errorf("cwd 不能为空")
	}
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("cwd 必须是当前平台绝对路径")
	}
	if strings.TrimSpace(cwd) != cwd || filepath.Clean(cwd) != cwd {
		return fmt.Errorf("cwd 必须是当前平台绝对干净路径")
	}
	return nil
}

func validateNextCursor(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor == "" {
		return "", fmt.Errorf("session/list nextCursor 存在时必须是非空 string")
	}
	return cursor, nil
}

func (a *ACPAgent) ensureClaudeSessionCatalogReady(ctx context.Context) error {
	if err := a.ensureStarted(ctx); err != nil {
		return err
	}
	if !a.isClaudeACP() {
		return fmt.Errorf("当前 Agent 不是 Claude ACP")
	}
	return nil
}

// UseClaudeSession 先从目录确认 session 与 cwd，再恢复并原子更新运行时绑定。
func (a *ACPAgent) UseClaudeSession(ctx context.Context, conversationID string, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("sessionId 不能为空")
	}
	revision := a.beginBindingIntent(conversationID)
	sessions, err := a.ListClaudeSessions(ctx)
	if err != nil {
		return err
	}
	selected, ok := findClaudeSession(sessions, sessionID)
	if !ok {
		return fmt.Errorf("session/list 中不存在 sessionId %q", sessionID)
	}
	params := acpSessionResumeParams{SessionID: selected.ID, Cwd: selected.Cwd, McpServers: []interface{}{}}
	result, sequence, err := a.rpcWithSequence(ctx, "session/resume", params)
	if err != nil {
		return fmt.Errorf("session/resume 失败: %w", err)
	}
	if err := validateACPObjectResult(result, "session/resume"); err != nil {
		return err
	}
	if err := a.cacheClaudeResumeConfig(selected.ID, result, sequence); err != nil {
		return err
	}
	commit := conversationBindingCommit{sessionID: selected.ID, cwd: selected.Cwd}
	return a.commitBindingIntent(conversationID, revision, commit)
}

// validateACPObjectResult 要求事务型 ACP 调用返回非 null JSON object。
func validateACPObjectResult(result json.RawMessage, method string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(result, &object); err != nil || object == nil {
		return fmt.Errorf("%s result 必须是非 null JSON object", method)
	}
	return nil
}

func findClaudeSession(sessions []ClaudeSession, sessionID string) (ClaudeSession, bool) {
	for _, session := range sessions {
		if session.ID == sessionID {
			return session, true
		}
	}
	return ClaudeSession{}, false
}
