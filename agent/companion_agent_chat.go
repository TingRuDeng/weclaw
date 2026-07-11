package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

func (a *CompanionAgent) Chat(ctx context.Context, conversationID string, message string) (string, error) {
	return a.ChatWithProgress(ctx, conversationID, message, nil)
}

func (a *CompanionAgent) ChatWithProgress(ctx context.Context, conversationID string, message string, onProgress func(string)) (string, error) {
	if err := a.waitConnected(ctx); err != nil {
		return "", err
	}
	id := fmt.Sprintf("%d", a.nextID.Add(1))
	call := &pendingCompanionCall{onProgress: onProgress, response: make(chan companionResponse, 1)}
	if err := a.sendRequest(id, conversationID, message, call); err != nil {
		return "", err
	}
	return a.waitResponse(ctx, id, call)
}

func (a *CompanionAgent) waitConnected(ctx context.Context) error {
	a.mu.Lock()
	if a.conn != nil {
		a.mu.Unlock()
		return nil
	}
	ch := a.connectedCh
	a.mu.Unlock()
	timer := time.NewTimer(companionConnectWait)
	defer timer.Stop()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("%s Companion 未连接，请在工作目录运行：weclaw companion --agent %s", a.name, a.name)
	}
}

func (a *CompanionAgent) sendRequest(id string, conversationID string, text string, call *pendingCompanionCall) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.encoder == nil || a.conn == nil {
		return fmt.Errorf("%s Companion 未连接", a.name)
	}
	call.connection = a.conn
	a.storePending(id, call)
	err := a.encoder.Encode(companionEnvelope{
		Type: companionMessageRequest,
		ID:   id,
		Request: &companionRequest{
			Command:        "send_input",
			ConversationID: conversationID,
			Text:           text,
		},
	})
	if err != nil {
		a.takePending(id)
	}
	return err
}

func (a *CompanionAgent) waitResponse(ctx context.Context, id string, call *pendingCompanionCall) (string, error) {
	select {
	case <-ctx.Done():
		a.takePending(id)
		return "", ctx.Err()
	case response := <-call.response:
		if !response.OK {
			if response.Error == "" {
				response.Error = "companion 返回未知错误"
			}
			return "", errors.New(response.Error)
		}
		if response.Text == "" {
			return "", errors.New("companion 返回空回复")
		}
		return response.Text, nil
	}
}

func (a *CompanionAgent) storePending(id string, call *pendingCompanionCall) {
	a.pendingMu.Lock()
	a.pending[id] = call
	a.pendingMu.Unlock()
}

func (a *CompanionAgent) takePending(id string) *pendingCompanionCall {
	a.pendingMu.Lock()
	call := a.pending[id]
	delete(a.pending, id)
	a.pendingMu.Unlock()
	return call
}

func (a *CompanionAgent) deliverProgress(id string, text string) {
	a.pendingMu.Lock()
	call := a.pending[id]
	a.pendingMu.Unlock()
	if call != nil && call.onProgress != nil && text != "" {
		call.onProgress(text)
	}
}

func (a *CompanionAgent) failPending(reason string) {
	a.pendingMu.Lock()
	pending := a.pending
	a.pending = make(map[string]*pendingCompanionCall)
	a.pendingMu.Unlock()
	for _, call := range pending {
		select {
		case call.response <- companionResponse{OK: false, Error: reason}:
		default:
		}
	}
}

func (a *CompanionAgent) failPendingForConnection(conn net.Conn, reason string) {
	a.pendingMu.Lock()
	failed := make([]*pendingCompanionCall, 0)
	for id, call := range a.pending {
		if call.connection != conn {
			continue
		}
		delete(a.pending, id)
		failed = append(failed, call)
	}
	a.pendingMu.Unlock()
	for _, call := range failed {
		select {
		case call.response <- companionResponse{OK: false, Error: reason}:
		default:
		}
	}
}
