package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

const codexAppEventBufferSize = 256

type codexAppTurnResult struct {
	text string
	err  error
}

type codexAppTurnState struct {
	builder   strings.Builder
	finalText string
}

type codexAppClient struct {
	url    string
	conn   *websocket.Conn
	nextID atomic.Int64

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan codexAppRPCResponse
	events    chan []byte
	failures  chan error
}

type codexAppRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func newCodexAppClient(url string) *codexAppClient {
	return &codexAppClient{
		url:      url,
		pending:  make(map[string]chan codexAppRPCResponse),
		events:   make(chan []byte, codexAppEventBufferSize),
		failures: make(chan error, 1),
	}
}

func (c *codexAppClient) Connect(context.Context) error {
	conn, _, err := websocket.DefaultDialer.Dial(c.url, nil)
	if err != nil {
		return fmt.Errorf("连接 Codex app-server 失败: %w", err)
	}
	c.conn = conn
	go c.readLoop()
	return nil
}

func (c *codexAppClient) Initialize(ctx context.Context) error {
	params := map[string]any{
		"clientInfo":   map[string]string{"name": "weclaw-companion", "version": "0.0.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}
	return c.rpc(ctx, "initialize", params, nil)
}

func (c *codexAppClient) StartThread(ctx context.Context, cwd string) (string, error) {
	params := map[string]any{"cwd": cwd, "experimentalRawEvents": false, "persistExtendedHistory": false}
	var out struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := c.rpc(ctx, "thread/start", params, &out); err != nil {
		return "", err
	}
	if out.Thread.ID == "" {
		return "", errors.New("Codex app-server 返回空 thread id")
	}
	return out.Thread.ID, nil
}

func (c *codexAppClient) StartTurn(ctx context.Context, threadID string, text string) (string, error) {
	params := map[string]any{"threadId": threadID, "input": []map[string]string{{"type": "text", "text": text}}}
	var out struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := c.rpc(ctx, "turn/start", params, &out); err != nil {
		return "", err
	}
	if out.Turn.ID == "" {
		return "", errors.New("Codex app-server 返回空 turn id")
	}
	return out.Turn.ID, nil
}

func (c *codexAppClient) WaitTurn(ctx context.Context, threadID string, turnID string, progress func(string)) (string, error) {
	state := &codexAppTurnState{}
	resultCh := make(chan codexAppTurnResult, 1)
	handleRaw := func(raw []byte) (string, error, bool) {
		if !handleCodexAppMessage(raw, threadID, turnID, state, progress, resultCh) {
			return "", nil, false
		}
		result := <-resultCh
		return result.text, result.err, true
	}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-c.failures:
			for {
				select {
				case raw := <-c.events:
					if text, resultErr, ok := handleRaw(raw); ok {
						return text, resultErr
					}
				default:
					return "", fmt.Errorf("Codex app-server 连接已断开: %w", err)
				}
			}
		case raw := <-c.events:
			if text, err, ok := handleRaw(raw); ok {
				return text, err
			}
		}
	}
}

func (c *codexAppClient) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *codexAppClient) rpc(ctx context.Context, method string, params any, out any) error {
	id := fmt.Sprintf("%d", c.nextID.Add(1))
	ch := c.storePending(id)
	if err := c.sendRPC(id, method, params); err != nil {
		c.takePending(id)
		return err
	}
	select {
	case <-ctx.Done():
		c.takePending(id)
		return ctx.Err()
	case response := <-ch:
		if response.Error != nil {
			return errors.New(response.Error.Message)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(response.Result, out)
	}
}

func (c *codexAppClient) sendRPC(id string, method string, params any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	data, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *codexAppClient) readLoop() {
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			c.failPending(err)
			c.failActiveTurn(err)
			return
		}
		if c.deliverRPCResponse(raw) {
			continue
		}
		c.events <- raw
	}
}

// failActiveTurn 唤醒正在等待 turn 结果的调用方，避免 websocket 断开后只靠超时返回。
func (c *codexAppClient) failActiveTurn(err error) {
	select {
	case c.failures <- err:
	default:
	}
}

func (c *codexAppClient) storePending(id string) chan codexAppRPCResponse {
	ch := make(chan codexAppRPCResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	return ch
}

func (c *codexAppClient) takePending(id string) chan codexAppRPCResponse {
	c.pendingMu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	return ch
}

func (c *codexAppClient) deliverRPCResponse(raw []byte) bool {
	var message struct {
		ID string `json:"id"`
		codexAppRPCResponse
	}
	if json.Unmarshal(raw, &message) != nil || message.ID == "" {
		return false
	}
	ch := c.takePending(message.ID)
	if ch == nil {
		return false
	}
	ch <- message.codexAppRPCResponse
	return true
}

func (c *codexAppClient) failPending(err error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan codexAppRPCResponse)
	c.pendingMu.Unlock()
	for _, ch := range pending {
		ch <- codexAppRPCResponse{Error: &struct {
			Message string `json:"message"`
		}{Message: err.Error()}}
	}
}

func handleCodexAppMessage(raw []byte, threadID string, turnID string, state *codexAppTurnState, progress func(string), resultCh chan<- codexAppTurnResult) bool {
	var message struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return false
	}
	if !codexAppMessageMatches(message.Params, threadID, turnID) {
		return false
	}
	switch message.Method {
	case "item/agentMessage/delta":
		handleCodexAppDelta(message.Params, state, progress)
	case "item/completed":
		handleCodexAppItemCompleted(message.Params, state)
	case "turn/completed":
		resultCh <- codexAppTurnResult{text: firstOpenCodeText(state.finalText, state.builder.String())}
		return true
	case "error":
		resultCh <- codexAppTurnResult{err: codexAppError(message.Params)}
		return true
	}
	return false
}

func codexAppMessageMatches(params json.RawMessage, threadID string, turnID string) bool {
	var meta struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &meta); err != nil {
		return false
	}
	if meta.ThreadID != "" && meta.ThreadID != threadID {
		return false
	}
	actualTurnID := meta.TurnID
	if actualTurnID == "" {
		actualTurnID = meta.Turn.ID
	}
	return actualTurnID == "" || actualTurnID == turnID
}

func handleCodexAppDelta(params json.RawMessage, state *codexAppTurnState, progress func(string)) {
	var payload struct {
		Delta string `json:"delta"`
	}
	if json.Unmarshal(params, &payload) == nil && payload.Delta != "" {
		state.builder.WriteString(payload.Delta)
		if progress != nil {
			progress(payload.Delta)
		}
	}
}

func handleCodexAppItemCompleted(params json.RawMessage, state *codexAppTurnState) {
	var payload struct {
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &payload) == nil && payload.Item.Type == "agentMessage" {
		state.finalText = payload.Item.Text
	}
}

func codexAppError(params json.RawMessage) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(params, &payload) == nil && strings.TrimSpace(payload.Error.Message) != "" {
		return errors.New(payload.Error.Message)
	}
	return fmt.Errorf("Codex app-server 错误: %s", strings.TrimSpace(string(params)))
}
