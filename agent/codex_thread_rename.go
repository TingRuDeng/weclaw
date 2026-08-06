package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maximumCodexThreadNameRunes = 120

// ErrCodexRenameOutcomeUnknown 表示名称写请求可能已经生效，但读回无法确认。
var ErrCodexRenameOutcomeUnknown = errors.New("Codex 会话重命名结果无法确认")

// RenameCodexThread 通过共享 Codex app-server 修改空闲 thread 的权威名称。
func (a *ACPAgent) RenameCodexThread(ctx context.Context, threadID string, name string) error {
	if a.protocol != protocolCodexAppServer {
		return fmt.Errorf("agent is not codex app-server")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return fmt.Errorf("empty thread id")
	}
	if err := validateCodexThreadName(name); err != nil {
		return err
	}
	if err := a.requireCodexSharedHostCapability("重命名会话"); err != nil {
		return err
	}
	if !a.codexAdmissionMu.TryLock() {
		return ErrCodexWriterBusy
	}
	defer a.codexAdmissionMu.Unlock()

	if a.codexOwners != nil {
		if exists, _ := a.codexOwners.writerLeaseStatus(threadID); exists {
			return ErrCodexWriterBusy
		}
	}
	if err := a.ensureStarted(ctx); err != nil {
		return fmt.Errorf("start Codex app-server before rename: %w", err)
	}
	before, err := a.readCodexThreadForRename(ctx, threadID)
	if err != nil {
		return fmt.Errorf("read Codex thread before rename: %w", err)
	}
	if strings.TrimSpace(before.Status.Type) != "idle" {
		return ErrCodexWriterBusy
	}

	result, renameErr := a.rpc(ctx, "thread/name/set", map[string]interface{}{
		"threadId": threadID,
		"name":     name,
	})
	if renameErr == nil {
		renameErr = validateACPObjectResult(result, "thread/name/set")
	}
	if renameErr != nil {
		return fmt.Errorf("%w: %v", ErrCodexRenameOutcomeUnknown, renameErr)
	}
	after, err := a.readCodexThreadForRename(ctx, threadID)
	if err != nil {
		return fmt.Errorf("%w: read renamed thread: %v", ErrCodexRenameOutcomeUnknown, err)
	}
	if after.Name != name {
		return fmt.Errorf("%w: readback name mismatch", ErrCodexRenameOutcomeUnknown)
	}
	return nil
}

func validateCodexThreadName(name string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("Codex 会话名称不能为空或包含首尾空白")
	}
	if utf8.RuneCountInString(name) > maximumCodexThreadNameRunes {
		return fmt.Errorf("Codex 会话名称不能超过 %d 个字符", maximumCodexThreadNameRunes)
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			return fmt.Errorf("Codex 会话名称不能包含换行或控制字符")
		}
	}
	return nil
}

func (a *ACPAgent) readCodexThreadForRename(ctx context.Context, threadID string) (codexThreadSnapshot, error) {
	result, err := a.rpc(ctx, "thread/read", map[string]interface{}{"threadId": threadID})
	if err != nil {
		return codexThreadSnapshot{}, err
	}
	if err := validateACPObjectResult(result, "thread/read"); err != nil {
		return codexThreadSnapshot{}, err
	}
	var response codexThreadReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return codexThreadSnapshot{}, fmt.Errorf("parse thread/read result: %w", err)
	}
	returnedThreadID := strings.TrimSpace(response.Thread.ID)
	if returnedThreadID == "" || returnedThreadID != threadID {
		return codexThreadSnapshot{}, fmt.Errorf(
			"thread/read returned mismatched thread id %q for %q",
			returnedThreadID,
			threadID,
		)
	}
	if strings.TrimSpace(response.Thread.Status.Type) == "" {
		return codexThreadSnapshot{}, fmt.Errorf("thread/read returned empty thread status")
	}
	return response.Thread, nil
}
