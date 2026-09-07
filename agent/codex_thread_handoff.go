package agent

import (
	"context"
	"fmt"
	"strings"
)

// SubscribeCodexThread establishes only the current client's observation
// subscription. Binding and write authorization have already been decided by
// the authoritative Host; a failure here is therefore a synchronization error.
func (a *ACPAgent) SubscribeCodexThread(ctx context.Context, conversationID string, threadID string) (attempted bool, err error) {
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	if conversationID == "" || threadID == "" || a.protocol != protocolCodexAppServer {
		return false, nil
	}
	// Keep topology changes and observer setup in the same admission order as
	// write preflight: admission first, then the per-connection subscription
	// lock. This prevents a handoff from racing a resume/fallback decision.
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	a.codexSubscriptionMu.Lock()
	defer a.codexSubscriptionMu.Unlock()
	binding, bindingOK := a.runtimeBindingForThread(conversationID, threadID)
	if bindingOK {
		switch binding.Runtime {
		case CodexRuntimeDesktop:
			if a.desktopRuntime == nil {
				return false, ErrCodexRuntimeUnavailable
			}
			return true, a.desktopRuntime.LoadHistory(ctx, CodexThreadRef{
				ConversationID: conversationID, ThreadID: threadID,
			})
		case CodexRuntimeConflict:
			return false, ErrCodexRuntimeConflict
		case CodexRuntimeUnknown:
			if !a.officialDaemonIsAuthoritativeForUnknownBinding() {
				return false, ErrCodexRuntimeUnavailable
			}
		}
	}
	a.mu.Lock()
	subscribedEpoch, subscribed := a.codexThreadSubscriptions[threadID]
	subscribed = subscribed && subscribedEpoch == a.wireEpoch
	a.mu.Unlock()
	if subscribed {
		a.markCodexThreadSubscribed(threadID)
		return false, nil
	}
	// The binding was sampled before taking the subscription lock. Re-read it
	// after serialization so a concurrent Subscribe that recovered to Desktop
	// is observed instead of issuing a second daemon resume.
	if current, ok := a.runtimeBindingForThread(conversationID, threadID); ok {
		binding, bindingOK = current, true
		switch binding.Runtime {
		case CodexRuntimeDesktop:
			return false, nil
		case CodexRuntimeConflict:
			return false, ErrCodexRuntimeConflict
		case CodexRuntimeUnknown:
			if !a.officialDaemonIsAuthoritativeForUnknownBinding() {
				return false, ErrCodexRuntimeUnavailable
			}
		}
	}
	intent, ok := a.currentCodexThreadBindingIntent(CodexThreadRef{
		ConversationID: conversationID, ThreadID: threadID,
	})
	if !ok {
		return false, ErrCodexControlChanged
	}
	if err := a.resumeThreadWithoutSubscription(ctx, conversationID, threadID); err != nil {
		if bindingOK && binding.Runtime == CodexRuntimeWeClaw {
			fallbackReq := CodexRuntimeRequest{Ref: binding.Ref, Intent: binding.Control}
			if _, recovered, recoverErr := a.recoverCodexDesktopActiveWriterExpected(
				ctx, fallbackReq, err, &binding, intent,
			); recoverErr != nil {
				return true, recoverErr
			} else if recovered {
				// Desktop IPC owns the observer lifecycle for this thread. Do not
				// mark this app-server connection as subscribed to the daemon.
				return true, nil
			}
		}
		return true, fmt.Errorf("订阅 Codex thread: %w", err)
	}
	if _, err := a.commitCodexThreadBindingIntent(intent, false, true, nil); err != nil {
		return true, err
	}
	a.persistState()
	return true, nil
}

// UnsubscribeCodexThread drops only this app-server connection's subscription.
// It never restarts or stops the authoritative Host and therefore cannot
// invalidate another frontend's active turn.
func (a *ACPAgent) UnsubscribeCodexThread(ctx context.Context, threadID string) (attempted bool, err error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || a.protocol != protocolCodexAppServer {
		return false, nil
	}
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	a.codexSubscriptionMu.Lock()
	defer a.codexSubscriptionMu.Unlock()
	if binding, ok := a.codexOwners.threadBinding(threadID); ok {
		switch binding.Runtime {
		case CodexRuntimeDesktop:
			// The private Desktop IPC bridge owns its own observation lifetime; it
			// must not start a second app-server merely to unsubscribe.
			return false, nil
		case CodexRuntimeConflict:
			return false, ErrCodexRuntimeConflict
		}
	}
	a.mu.Lock()
	subscribedEpoch, subscribed := a.codexThreadSubscriptions[threadID]
	currentEpoch := a.wireEpoch
	a.mu.Unlock()
	if !subscribed || subscribedEpoch != currentEpoch {
		return false, nil
	}
	_, err = a.rpc(ctx, "thread/unsubscribe", map[string]interface{}{"threadId": threadID})
	if err != nil {
		return true, fmt.Errorf("停止观察 Codex thread: %w", err)
	}
	a.markCodexThreadUnsubscribed(threadID)
	return true, nil
}

func (a *ACPAgent) markCodexThreadSubscribed(threadID string) {
	a.trackCodexThreadSubscription(threadID)
	a.persistState()
}

func (a *ACPAgent) trackCodexThreadSubscription(threadID string) {
	threadID = strings.TrimSpace(threadID)
	a.mu.Lock()
	if a.codexThreadSubscriptions == nil {
		a.codexThreadSubscriptions = make(map[string]uint64)
	}
	a.codexThreadSubscriptions[threadID] = a.wireEpoch
	for candidate, selected := range a.threads {
		if strings.TrimSpace(selected) == threadID {
			delete(a.resumeOnFirstUse, candidate)
		}
	}
	a.mu.Unlock()
}

func (a *ACPAgent) markCodexThreadUnsubscribed(threadID string) {
	threadID = strings.TrimSpace(threadID)
	a.mu.Lock()
	delete(a.codexThreadSubscriptions, threadID)
	a.mu.Unlock()
}

func (a *ACPAgent) codexThreadSubscriptionPending(conversationID string, threadID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	a.mu.Lock()
	defer a.mu.Unlock()
	subscribedEpoch, subscribed := a.codexThreadSubscriptions[threadID]
	return strings.TrimSpace(a.threads[conversationID]) == threadID &&
		(!subscribed || subscribedEpoch != a.wireEpoch)
}
