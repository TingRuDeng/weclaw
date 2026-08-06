package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	claudeRenameCommand               = "rename"
	defaultClaudeRenameCapabilityWait = 2 * time.Second
	maximumClaudeRenameNameRunes      = 120
)

var (
	// ErrClaudeRenameUnsupported 表示当前 adapter 没有公布可安全调用的 rename 命令。
	ErrClaudeRenameUnsupported = errors.New("Claude adapter 未公布 rename 命令")
	// ErrClaudeRenameOutcomeUnknown 表示 /rename 已发送，但权威目录读回未能确认结果。
	ErrClaudeRenameOutcomeUnknown = errors.New("Claude 会话重命名结果无法确认")
	// ErrClaudeSessionWriterBusy 表示同一 session 已有 prompt writer。
	ErrClaudeSessionWriterBusy = errors.New("Claude session writer busy")
)

// RenameClaudeSession 通过当前共享 ClaudeHost 的已公布 /rename 命令修改 session 标题。
func (a *ACPAgent) RenameClaudeSession(ctx context.Context, sessionID string, name string) error {
	if a.protocol != protocolLegacyACP {
		return fmt.Errorf("agent is not Claude ACP")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId 不能为空")
	}
	if err := validateClaudeRenameName(name); err != nil {
		return err
	}
	sessions, err := a.ListClaudeSessions(ctx)
	if err != nil {
		return err
	}
	selected, found := findClaudeSession(sessions, sessionID)
	if !found {
		return fmt.Errorf("session/list 中不存在 sessionId %q", sessionID)
	}

	a.claudeHostControlMu.Lock()
	defer a.claudeHostControlMu.Unlock()
	if a.legacySessionChannelsInUse(sessionID) {
		return ErrClaudeSessionWriterBusy
	}
	if err := a.ensureClaudeHostSessionLoadedLocked(ctx, selected); err != nil {
		return err
	}
	if err := a.waitForClaudeRenameCommand(ctx, sessionID); err != nil {
		return err
	}
	if err := a.runClaudeRenamePrompt(ctx, sessionID, name); err != nil {
		return err
	}
	updated, err := a.ListClaudeSessions(ctx)
	if err != nil {
		return fmt.Errorf("%w: session/list readback: %v", ErrClaudeRenameOutcomeUnknown, err)
	}
	confirmed, found := findClaudeSession(updated, sessionID)
	if !found || confirmed.Title != name {
		return fmt.Errorf("%w: session/list title mismatch", ErrClaudeRenameOutcomeUnknown)
	}
	return nil
}

func validateClaudeRenameName(name string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("Claude 会话名称不能为空或包含首尾空白")
	}
	if utf8.RuneCountInString(name) > maximumClaudeRenameNameRunes {
		return fmt.Errorf("Claude 会话名称不能超过 %d 个字符", maximumClaudeRenameNameRunes)
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			return fmt.Errorf("Claude 会话名称不能包含换行或控制字符")
		}
	}
	return nil
}

// ensureClaudeHostSessionLoadedLocked 只更新 Host 级加载表，不创建或改变 frontend binding。
func (a *ACPAgent) ensureClaudeHostSessionLoadedLocked(ctx context.Context, session ClaudeSession) error {
	if reusable, err := a.reusableClaudeSession(session.ID, session.Cwd); err != nil {
		return err
	} else if reusable {
		return nil
	}
	result, sequence, err := a.rpcWithSequence(ctx, "session/resume", acpSessionResumeParams{
		SessionID:  session.ID,
		Cwd:        session.Cwd,
		McpServers: []interface{}{},
	})
	if err != nil {
		return fmt.Errorf("session/resume 失败: %w", err)
	}
	if err := validateACPObjectResult(result, "session/resume"); err != nil {
		return err
	}
	if err := a.cacheClaudeResumeConfig(session.ID, result, sequence); err != nil {
		return err
	}
	a.markClaudeSessionLoaded(session.ID, session.Cwd)
	return nil
}

func (a *ACPAgent) waitForClaudeRenameCommand(ctx context.Context, sessionID string) error {
	waitCtx, cancel := context.WithTimeout(ctx, defaultClaudeRenameCapabilityWait)
	defer cancel()
	for {
		known, supported, invalid, changed := a.claudeRenameCommandSnapshot(sessionID)
		if known {
			if supported && invalid == "" {
				return nil
			}
			if invalid != "" {
				return fmt.Errorf("%w: %s", ErrClaudeRenameUnsupported, invalid)
			}
			return ErrClaudeRenameUnsupported
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("%w: 等待 available_commands_update: %v", ErrClaudeRenameUnsupported, waitCtx.Err())
		case <-changed:
		}
	}
}

func (a *ACPAgent) claudeRenameCommandSnapshot(sessionID string) (bool, bool, string, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.claudeSessionCommands[sessionID]
	known := state.Known && state.Generation == a.legacyRuntimeGeneration
	_, supported := state.Names[claudeRenameCommand]
	changed := a.claudeCommandChanged
	if changed == nil {
		changed = make(chan struct{})
		a.claudeCommandChanged = changed
	}
	return known, known && supported, state.Err, changed
}

func (a *ACPAgent) legacySessionChannelsInUse(sessionID string) bool {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	_, notifyInUse := a.notifyCh[sessionID]
	_, turnInUse := a.turnCh[sessionID]
	return notifyInUse || turnInUse
}

func (a *ACPAgent) runClaudeRenamePrompt(ctx context.Context, sessionID string, name string) error {
	notifyCh := make(chan *sessionUpdate, 32)
	approvalCh := make(chan *codexTurnEvent, 4)
	if !a.registerLegacySessionChannels(sessionID, notifyCh, approvalCh) {
		return ErrClaudeSessionWriterBusy
	}
	defer a.unregisterLegacySessionChannels(sessionID, notifyCh, approvalCh)
	state := legacyPromptState{
		ctx:        ctx,
		sessionID:  sessionID,
		notifyCh:   notifyCh,
		approvalCh: approvalCh,
		promptDone: a.startLegacyPrompt(ctx, sessionID, "/rename "+name),
	}
	for {
		select {
		case <-ctx.Done():
			cancelErr := a.cancelLegacyPrompt(state)
			return fmt.Errorf("%w: %v", ErrClaudeRenameOutcomeUnknown, errors.Join(ctx.Err(), cancelErr))
		case <-notifyCh:
			// Metadata and command snapshots are cached before channel delivery.
		case event := <-approvalCh:
			if err := a.handleLegacyInteraction(ctx, event); err != nil {
				return fmt.Errorf("%w: control prompt interaction: %v", ErrClaudeRenameOutcomeUnknown, err)
			}
		case done := <-state.promptDone:
			if done.err != nil {
				return fmt.Errorf("%w: session/prompt: %v", ErrClaudeRenameOutcomeUnknown, done.err)
			}
			if err := validateACPObjectResult(done.result, "session/prompt"); err != nil {
				return fmt.Errorf("%w: %v", ErrClaudeRenameOutcomeUnknown, err)
			}
			return nil
		}
	}
}
