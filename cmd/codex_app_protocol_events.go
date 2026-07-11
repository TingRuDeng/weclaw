package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// handleCodexAppMessage 归并单个 app-server 事件，并在 turn 终态到达时返回结果。
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
	return fmt.Errorf("codex app-server 错误: %s", strings.TrimSpace(string(params)))
}
