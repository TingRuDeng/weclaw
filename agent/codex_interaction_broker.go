package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
)

var ErrCodexInteractionResolvedExternally = errors.New("Codex 交互已由其他前端处理")

// CodexInteractionResolution lets every frontend stop waiting when another
// frontend resolves the same provider request.
type CodexInteractionResolution interface {
	Done() <-chan struct{}
	Err() error
}

type codexInteractionResolution struct {
	done <-chan struct{}
	err  func() error
}

func (r codexInteractionResolution) Done() <-chan struct{} {
	return r.done
}

func (r codexInteractionResolution) Err() error {
	if r.err == nil {
		return nil
	}
	return r.err()
}

type codexInteractionBrokerState uint8

const (
	codexInteractionPending codexInteractionBrokerState = iota
	codexInteractionSubmitting
	codexInteractionResolved
	codexInteractionTerminal
)

type codexInteractionBroker struct {
	mu          sync.Mutex
	threadID    string
	turnID      string
	requestKey  string
	state       codexInteractionBrokerState
	attemptDone chan struct{}
	resolved    chan struct{}
}

func newCodexInteractionBroker(threadID string, turnID string, requestKey string) *codexInteractionBroker {
	return &codexInteractionBroker{
		threadID: strings.TrimSpace(threadID), turnID: strings.TrimSpace(turnID),
		requestKey: requestKey, resolved: make(chan struct{}),
	}
}

func (b *codexInteractionBroker) resolution() CodexInteractionResolution {
	if b == nil {
		return nil
	}
	return codexInteractionResolution{done: b.resolved, err: b.resolutionError}
}

func (b *codexInteractionBroker) resolutionError() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case codexInteractionResolved:
		return ErrCodexInteractionResolvedExternally
	case codexInteractionTerminal:
		return ErrCodexTurnTerminal
	default:
		return nil
	}
}

func (b *codexInteractionBroker) submit(ctx context.Context, respond func() error) error {
	if b == nil {
		return respond()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		b.mu.Lock()
		switch b.state {
		case codexInteractionResolved:
			b.mu.Unlock()
			return ErrCodexInteractionResolvedExternally
		case codexInteractionTerminal:
			b.mu.Unlock()
			return ErrCodexTurnTerminal
		case codexInteractionSubmitting:
			attemptDone := b.attemptDone
			b.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-b.resolved:
				return b.resolutionError()
			case <-attemptDone:
				continue
			}
		default:
			b.state = codexInteractionSubmitting
			b.attemptDone = make(chan struct{})
			attemptDone := b.attemptDone
			b.mu.Unlock()

			err := respond()
			b.mu.Lock()
			resultErr := err
			if b.state == codexInteractionSubmitting {
				if err == nil {
					b.state = codexInteractionResolved
					close(b.resolved)
				} else {
					b.state = codexInteractionPending
				}
			} else if b.state == codexInteractionResolved {
				resultErr = ErrCodexInteractionResolvedExternally
			} else if b.state == codexInteractionTerminal {
				resultErr = ErrCodexTurnTerminal
			}
			if b.attemptDone == attemptDone {
				close(attemptDone)
				b.attemptDone = nil
			}
			b.mu.Unlock()
			return resultErr
		}
	}
}

func (b *codexInteractionBroker) resolve(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == codexInteractionResolved || b.state == codexInteractionTerminal {
		return
	}
	if errors.Is(err, ErrCodexTurnTerminal) || errors.Is(err, ErrApprovalTurnTerminal) {
		b.state = codexInteractionTerminal
	} else {
		b.state = codexInteractionResolved
	}
	close(b.resolved)
}

func codexInteractionBrokerKey(event *codexTurnEvent) string {
	requestID := codexInteractionID(event)
	if requestID == "" {
		return ""
	}
	kind := "approval"
	if event != nil && event.UserInput != nil {
		kind = "user_input"
	}
	return kind + "\x00" + strings.TrimSpace(event.TurnID) + "\x00" + requestID
}

func (a *ACPAgent) bindCodexInteractionBrokerLocked(threadID string, event *codexTurnEvent) {
	threadID = strings.TrimSpace(threadID)
	key := codexInteractionBrokerKey(event)
	if key == "" {
		return
	}
	if a.turnInteractionBrokers == nil {
		a.turnInteractionBrokers = make(map[string]map[string]*codexInteractionBroker)
	}
	brokers := a.turnInteractionBrokers[threadID]
	if brokers == nil {
		brokers = make(map[string]*codexInteractionBroker)
		a.turnInteractionBrokers[threadID] = brokers
	}
	broker := brokers[key]
	if broker == nil {
		broker = newCodexInteractionBroker(threadID, event.TurnID, key)
		brokers[key] = broker
	}
	event.interactionBroker = broker
	resolution := broker.resolution()
	if event.Approval != nil {
		event.Approval.Request.Resolution = resolution
	}
	if event.UserInput != nil {
		event.UserInput.Request.Resolution = resolution
	}
}

func (a *ACPAgent) submitCodexInteraction(ctx context.Context, event *codexTurnEvent, respond func() error) error {
	if event == nil || event.interactionBroker == nil {
		return respond()
	}
	err := event.interactionBroker.submit(ctx, respond)
	if err == nil {
		a.forgetPendingCodexInteraction(event.interactionBroker.threadID, event)
	}
	return err
}

func (a *ACPAgent) resolveCodexInteraction(event *codexTurnEvent, err error) {
	if event == nil || event.interactionBroker == nil {
		return
	}
	event.interactionBroker.resolve(err)
	a.forgetPendingCodexInteraction(event.interactionBroker.threadID, event)
}

type codexServerRequestResolvedParams struct {
	ThreadID  string          `json:"threadId"`
	RequestID json.RawMessage `json:"requestId"`
}

// handleCodexServerRequestResolved consumes the authoritative notification
// emitted when another app-server client answers the same server request.
func (a *ACPAgent) handleCodexServerRequestResolved(params json.RawMessage) {
	var resolved codexServerRequestResolvedParams
	if err := json.Unmarshal(params, &resolved); err != nil {
		log.Printf("[acp] failed to parse serverRequest/resolved: %v", err)
		return
	}
	threadID := strings.TrimSpace(resolved.ThreadID)
	requestID := codexServerRequestID(resolved.RequestID)
	if threadID == "" || requestID == "" {
		log.Printf("[acp] ignoring serverRequest/resolved without thread or request identity")
		return
	}
	a.resolveCodexInteractionRequest(threadID, requestID, ErrCodexInteractionResolvedExternally)
}

func codexServerRequestID(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.FormatInt(number, 10)
	}
	return ""
}

func (a *ACPAgent) resolveCodexInteractionRequest(threadID string, requestID string, err error) {
	threadID = strings.TrimSpace(threadID)
	requestID = strings.TrimSpace(requestID)
	if threadID == "" || requestID == "" {
		return
	}

	a.notifyMu.Lock()
	pending := a.pendingTurnInteractions[threadID]
	brokers := make(map[*codexInteractionBroker]struct{})
	for key, event := range pending {
		if codexInteractionID(event) != requestID {
			continue
		}
		if event != nil && event.interactionBroker != nil {
			brokers[event.interactionBroker] = struct{}{}
		}
		a.forgetCodexInteractionLocked(threadID, key, event)
	}
	a.notifyMu.Unlock()

	for broker := range brokers {
		broker.resolve(err)
	}
}

func (a *ACPAgent) forgetCodexInteractionLocked(threadID string, key string, event *codexTurnEvent) {
	delete(a.pendingTurnInteractions[threadID], key)
	if len(a.pendingTurnInteractions[threadID]) == 0 {
		delete(a.pendingTurnInteractions, threadID)
	}
	broker := (*codexInteractionBroker)(nil)
	if event != nil {
		broker = event.interactionBroker
	}
	if broker != nil && a.turnInteractionBrokers[threadID][key] == broker {
		delete(a.turnInteractionBrokers[threadID], key)
	}
	if len(a.turnInteractionBrokers[threadID]) == 0 {
		delete(a.turnInteractionBrokers, threadID)
	}
}

// settleCodexTurnInteractionsLocked closes every pending request owned by the
// terminal turn before observers consume its final event.
func (a *ACPAgent) settleCodexTurnInteractionsLocked(threadID string, turnID string) {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if threadID == "" {
		return
	}
	brokers := make(map[*codexInteractionBroker]struct{})
	for key, event := range a.pendingTurnInteractions[threadID] {
		if turnID != "" && (event == nil || strings.TrimSpace(event.TurnID) != turnID) {
			continue
		}
		if event != nil && event.interactionBroker != nil {
			brokers[event.interactionBroker] = struct{}{}
		}
		a.forgetCodexInteractionLocked(threadID, key, event)
	}
	for key, broker := range a.turnInteractionBrokers[threadID] {
		if broker == nil || turnID != "" && broker.turnID != turnID {
			continue
		}
		brokers[broker] = struct{}{}
		delete(a.turnInteractionBrokers[threadID], key)
	}
	if len(a.turnInteractionBrokers[threadID]) == 0 {
		delete(a.turnInteractionBrokers, threadID)
	}
	for broker := range brokers {
		broker.resolve(ErrCodexTurnTerminal)
	}
}
