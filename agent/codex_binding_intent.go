package agent

import "strings"

// codexThreadBindingIntent correlates one asynchronous mapping operation with
// both the persisted ACP selection and the runtime owner registry. Revisions
// are process-local and deliberately never reused, including after Clear.
type codexThreadBindingIntent struct {
	ref                   CodexThreadRef
	localRevision         uint64
	ownerRevision         uint64
	threadRevision        uint64
	expectedLocalThreadID string
	expectedThreadID      string
}

type codexThreadLifecycle struct {
	revision uint64
	archived bool
}

func (a *ACPAgent) beginCodexThreadBindingIntent(conversationID string, threadID string) codexThreadBindingIntent {
	a.codexBindingMu.Lock()
	defer a.codexBindingMu.Unlock()

	ref := CodexThreadRef{
		ConversationID: strings.TrimSpace(conversationID),
		ThreadID:       strings.TrimSpace(threadID),
	}
	a.mu.Lock()
	localRevision := a.advanceCodexBindingRevisionLocked(ref.ConversationID)
	threadRevision := a.codexThreadLifecycles[ref.ThreadID].revision
	a.mu.Unlock()
	ownerRevision := uint64(0)
	if a.codexOwners != nil {
		ownerRevision = a.codexOwners.beginConversationBindingIntent(ref.ConversationID)
	}
	return codexThreadBindingIntent{
		ref: ref, localRevision: localRevision, ownerRevision: ownerRevision,
		threadRevision: threadRevision,
	}
}

// invalidateCodexThreadBinding starts a new binding generation while clearing
// both route stores. Reset may reuse the returned intent to bind the replacement
// thread; Clear discards it so no older asynchronous result can commit.
func (a *ACPAgent) invalidateCodexThreadBinding(conversationID string) (codexThreadBindingIntent, string) {
	a.codexBindingMu.Lock()
	defer a.codexBindingMu.Unlock()

	conversationID = strings.TrimSpace(conversationID)
	a.mu.Lock()
	localRevision := a.advanceCodexBindingRevisionLocked(conversationID)
	oldThreadID := a.threads[conversationID]
	delete(a.threads, conversationID)
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()

	ownerRevision := uint64(0)
	if a.codexOwners != nil {
		ownerRevision = a.codexOwners.invalidateConversationBinding(conversationID)
	}
	return codexThreadBindingIntent{
		ref:           CodexThreadRef{ConversationID: conversationID},
		localRevision: localRevision,
		ownerRevision: ownerRevision,
	}, oldThreadID
}

// currentCodexThreadBindingIntent snapshots an already committed mapping. It
// does not create a new operation, so Clear or Use can invalidate a blocking
// resume/Desktop IPC by advancing either revision.
func (a *ACPAgent) currentCodexThreadBindingIntent(ref CodexThreadRef) (codexThreadBindingIntent, bool) {
	ref.ConversationID = strings.TrimSpace(ref.ConversationID)
	ref.ThreadID = strings.TrimSpace(ref.ThreadID)
	if ref.ConversationID == "" || ref.ThreadID == "" || a.codexOwners == nil {
		return codexThreadBindingIntent{}, false
	}

	a.codexBindingMu.Lock()
	defer a.codexBindingMu.Unlock()
	a.mu.Lock()
	if strings.TrimSpace(a.threads[ref.ConversationID]) != ref.ThreadID {
		a.mu.Unlock()
		return codexThreadBindingIntent{}, false
	}
	localRevision := a.ensureCodexBindingRevisionLocked(ref.ConversationID)
	threadLifecycle := a.codexThreadLifecycles[ref.ThreadID]
	if threadLifecycle.archived {
		a.mu.Unlock()
		return codexThreadBindingIntent{}, false
	}
	a.mu.Unlock()
	ownerRevision, ok := a.codexOwners.currentConversationBindingRevision(ref)
	if !ok {
		return codexThreadBindingIntent{}, false
	}
	return codexThreadBindingIntent{
		ref: ref, localRevision: localRevision, ownerRevision: ownerRevision,
		threadRevision:        threadLifecycle.revision,
		expectedLocalThreadID: ref.ThreadID,
		expectedThreadID:      ref.ThreadID,
	}, true
}

// observedCodexRuntimeBindingIntent accepts either an existing runtime route or
// the first observation for a conversation. A cleared conversation already has
// a local revision tombstone, so a delayed observer cannot treat it as new and
// recreate the route.
func (a *ACPAgent) observedCodexRuntimeBindingIntent(ref CodexThreadRef) (codexThreadBindingIntent, bool) {
	ref.ConversationID = strings.TrimSpace(ref.ConversationID)
	ref.ThreadID = strings.TrimSpace(ref.ThreadID)
	if ref.ConversationID == "" || ref.ThreadID == "" || a.codexOwners == nil {
		return codexThreadBindingIntent{}, false
	}

	a.codexBindingMu.Lock()
	defer a.codexBindingMu.Unlock()

	a.mu.Lock()
	localThreadID := strings.TrimSpace(a.threads[ref.ConversationID])
	localRevision := a.codexBindingRevisions[ref.ConversationID]
	threadLifecycle := a.codexThreadLifecycles[ref.ThreadID]
	if threadLifecycle.archived || localThreadID != "" && localThreadID != ref.ThreadID {
		a.mu.Unlock()
		return codexThreadBindingIntent{}, false
	}
	a.mu.Unlock()

	if ownerRevision, ok := a.codexOwners.currentConversationBindingRevision(ref); ok {
		a.mu.Lock()
		if localRevision == 0 {
			localRevision = a.advanceCodexBindingRevisionLocked(ref.ConversationID)
		}
		a.mu.Unlock()
		return codexThreadBindingIntent{
			ref: ref, localRevision: localRevision, ownerRevision: ownerRevision,
			threadRevision:        threadLifecycle.revision,
			expectedLocalThreadID: localThreadID,
			expectedThreadID:      ref.ThreadID,
		}, true
	}
	if localRevision != 0 {
		return codexThreadBindingIntent{}, false
	}

	a.mu.Lock()
	localRevision = a.advanceCodexBindingRevisionLocked(ref.ConversationID)
	a.mu.Unlock()
	ownerRevision := a.codexOwners.beginConversationBindingIntent(ref.ConversationID)
	return codexThreadBindingIntent{
		ref: ref, localRevision: localRevision, ownerRevision: ownerRevision,
		threadRevision: threadLifecycle.revision,
	}, true
}

type codexOwnerBindingCommit func(ownerRevision uint64) (CodexThreadBinding, error)

// commitCodexRuntimeBindingIntent conditionally updates only the owner
// registry. This is used by private Desktop routes and observed-turn callbacks,
// which must not manufacture a shared app-server mapping in a.threads.
func (a *ACPAgent) commitCodexRuntimeBindingIntent(
	intent codexThreadBindingIntent,
	commitOwner codexOwnerBindingCommit,
) (CodexThreadBinding, error) {
	a.codexBindingMu.Lock()
	defer a.codexBindingMu.Unlock()
	if !a.codexBindingIntentCurrentLocked(intent) {
		return CodexThreadBinding{}, ErrCodexControlChanged
	}
	if commitOwner == nil || a.codexOwners == nil {
		return CodexThreadBinding{Ref: intent.ref}, nil
	}
	return commitOwner(intent.ownerRevision)
}

func (a *ACPAgent) commitCodexThreadBindingIntent(
	intent codexThreadBindingIntent,
	needsResume bool,
	subscribed bool,
	commitOwner codexOwnerBindingCommit,
) (CodexThreadBinding, error) {
	a.codexBindingMu.Lock()
	defer a.codexBindingMu.Unlock()
	if !a.codexBindingIntentCurrentLocked(intent) {
		return CodexThreadBinding{}, ErrCodexControlChanged
	}

	binding := CodexThreadBinding{Ref: intent.ref}
	var err error
	if commitOwner != nil && a.codexOwners != nil {
		binding, err = commitOwner(intent.ownerRevision)
		if err != nil {
			return binding, err
		}
	}

	a.mu.Lock()
	if a.codexBindingRevisions[intent.ref.ConversationID] != intent.localRevision {
		a.mu.Unlock()
		return binding, ErrCodexControlChanged
	}
	a.threads[intent.ref.ConversationID] = intent.ref.ThreadID
	if needsResume {
		a.resumeOnFirstUse[intent.ref.ConversationID] = true
	} else {
		delete(a.resumeOnFirstUse, intent.ref.ConversationID)
	}
	if subscribed {
		if a.codexThreadSubscriptions == nil {
			a.codexThreadSubscriptions = make(map[string]uint64)
		}
		a.codexThreadSubscriptions[intent.ref.ThreadID] = a.wireEpoch
	}
	a.mu.Unlock()
	return binding, nil
}

func (a *ACPAgent) codexBindingIntentCurrentLocked(intent codexThreadBindingIntent) bool {
	if !a.codexLocalBindingIntentCurrent(intent) {
		return false
	}
	return a.codexOwners == nil || a.codexOwners.conversationBindingRevisionMatches(
		intent.ref, intent.ownerRevision, intent.expectedThreadID,
	)
}

func (a *ACPAgent) codexLocalBindingIntentCurrent(intent codexThreadBindingIntent) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if intent.localRevision == 0 || a.codexBindingRevisions[intent.ref.ConversationID] != intent.localRevision {
		return false
	}
	if threadID := strings.TrimSpace(intent.ref.ThreadID); threadID != "" {
		lifecycle := a.codexThreadLifecycles[threadID]
		if lifecycle.archived || lifecycle.revision != intent.threadRevision {
			return false
		}
	}
	return intent.expectedLocalThreadID == "" ||
		strings.TrimSpace(a.threads[intent.ref.ConversationID]) == intent.expectedLocalThreadID
}

func (a *ACPAgent) advanceCodexThreadLifecycleLocked(threadID string, archived bool) codexThreadLifecycle {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return codexThreadLifecycle{}
	}
	if a.codexThreadLifecycles == nil {
		a.codexThreadLifecycles = make(map[string]codexThreadLifecycle)
	}
	a.codexThreadLifecycleCounter++
	lifecycle := codexThreadLifecycle{
		revision: a.codexThreadLifecycleCounter,
		archived: archived,
	}
	a.codexThreadLifecycles[threadID] = lifecycle
	return lifecycle
}

func (a *ACPAgent) advanceCodexBindingRevisionLocked(conversationID string) uint64 {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0
	}
	a.codexBindingRevisionCounter++
	a.codexBindingRevisions[conversationID] = a.codexBindingRevisionCounter
	return a.codexBindingRevisionCounter
}

func (a *ACPAgent) ensureCodexBindingRevisionLocked(conversationID string) uint64 {
	if revision := a.codexBindingRevisions[conversationID]; revision != 0 {
		return revision
	}
	return a.advanceCodexBindingRevisionLocked(conversationID)
}
