package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
)

// ErrCodexArchiveOutcomeUnknown 表示归档请求已发出，但没有拿到可验证终态。
// 调用方必须解除本地绑定并重新读取可见会话，不能继续自动写入旧 thread。
var ErrCodexArchiveOutcomeUnknown = errors.New("Codex 会话归档结果无法确认")

// ArchiveCodexThread 通过官方 app-server 协议归档空闲 thread。
func (a *ACPAgent) ArchiveCodexThread(ctx context.Context, threadID string) error {
	if a.protocol != protocolCodexAppServer {
		return fmt.Errorf("agent is not codex app-server")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return fmt.Errorf("empty thread id")
	}
	if err := a.requireCodexSharedHostCapability("归档会话"); err != nil {
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
		return fmt.Errorf("start Codex app-server before archive: %w", err)
	}
	state, err := a.readCodexThreadArchiveState(ctx, threadID)
	if err != nil {
		return fmt.Errorf("read Codex thread before archive: %w", err)
	}
	if state.Active {
		return ErrCodexWriterBusy
	}

	result, archiveErr := a.rpc(ctx, "thread/archive", map[string]interface{}{"threadId": threadID})
	if archiveErr == nil {
		archiveErr = validateACPObjectResult(result, "thread/archive")
	}
	if archiveErr != nil {
		forgetErr := a.forgetCodexThread(threadID)
		return fmt.Errorf(
			"%w: thread=%s: %v",
			ErrCodexArchiveOutcomeUnknown,
			threadID,
			errors.Join(archiveErr, forgetErr),
		)
	}
	if err := a.forgetCodexThread(threadID); err != nil {
		return fmt.Errorf("%w: 清理本地 thread 绑定: %v", ErrCodexArchiveOutcomeUnknown, err)
	}
	return nil
}

func (a *ACPAgent) readCodexThreadArchiveState(ctx context.Context, threadID string) (CodexThreadState, error) {
	result, err := a.rpc(ctx, "thread/read", map[string]interface{}{"threadId": threadID})
	if err != nil {
		return CodexThreadState{}, err
	}
	if err := validateACPObjectResult(result, "thread/read"); err != nil {
		return CodexThreadState{}, err
	}
	var response codexThreadReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return CodexThreadState{}, fmt.Errorf("parse thread/read result: %w", err)
	}
	returnedThreadID := strings.TrimSpace(response.Thread.ID)
	if returnedThreadID == "" || returnedThreadID != threadID {
		return CodexThreadState{}, fmt.Errorf(
			"thread/read returned mismatched thread id %q for %q",
			returnedThreadID,
			threadID,
		)
	}
	if strings.TrimSpace(response.Thread.Status.Type) == "" {
		return CodexThreadState{}, fmt.Errorf("thread/read returned empty thread status")
	}
	return codexThreadStateFromSnapshot(response.Thread), nil
}

// forgetCodexThread 删除所有指向归档 thread 的进程内和持久化路由。
func (a *ACPAgent) forgetCodexThread(threadID string) error {
	threadID = strings.TrimSpace(threadID)
	var ownerErr error
	if a.codexOwners != nil {
		ownerErr = a.codexOwners.forgetThread(threadID)
	}
	a.mu.Lock()
	for conversationID, currentThreadID := range a.threads {
		if strings.TrimSpace(currentThreadID) != threadID {
			continue
		}
		delete(a.threads, conversationID)
		delete(a.resumeOnFirstUse, conversationID)
	}
	delete(a.codexThreadConfigs, threadID)
	delete(a.codexThreadConfigRevisions, threadID)
	a.mu.Unlock()
	a.persistState()
	return ownerErr
}

// forgetThread 只在没有 writer lease 时删除 thread 及其全部 conversation 路由。
func (r *codexRuntimeOwnerRegistry) forgetThread(threadID string) error {
	threadID = strings.TrimSpace(threadID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leases[threadID] != nil {
		return ErrCodexWriterBusy
	}
	for conversationID, currentThreadID := range r.conversations {
		if strings.TrimSpace(currentThreadID) == threadID {
			delete(r.conversations, conversationID)
		}
	}
	delete(r.threads, threadID)
	return nil
}

func (a *ACPAgent) handleCodexThreadArchivedNotification(params json.RawMessage) {
	var notification struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(params, &notification); err != nil || strings.TrimSpace(notification.ThreadID) == "" {
		log.Printf("[acp] ignored invalid thread/archived notification")
		return
	}
	threadID := strings.TrimSpace(notification.ThreadID)
	if err := a.forgetCodexThread(threadID); err != nil {
		log.Printf("[acp] archived thread local binding cleanup failed (thread=%s): %v", threadID, err)
	}
	a.codexThreadArchiveHandlerMu.RLock()
	handler := a.codexThreadArchivedHandler
	a.codexThreadArchiveHandlerMu.RUnlock()
	if handler != nil {
		handler(threadID)
	}
}

// SetCodexThreadArchivedHandler 注册外部归档事件处理器；设置 nil 可解除注册。
func (a *ACPAgent) SetCodexThreadArchivedHandler(handler func(threadID string)) {
	a.codexThreadArchiveHandlerMu.Lock()
	a.codexThreadArchivedHandler = handler
	a.codexThreadArchiveHandlerMu.Unlock()
}
