package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type codexThreadReadResponse struct {
	Thread codexThreadSnapshot `json:"thread"`
}

type codexThreadSnapshot struct {
	ID     string              `json:"id"`
	Status codexThreadStatus   `json:"status"`
	Turns  []codexTurnSnapshot `json:"turns"`
}

type codexThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags"`
}

type codexTurnSnapshot struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Items  []codexThreadItem `json:"items"`
}

type codexThreadItem struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
}

// ReadCodexThreadState 读取 Codex app-server thread 当前状态，用于接管本地 App 运行中任务。
func (a *ACPAgent) ReadCodexThreadState(ctx context.Context, conversationID string, threadID string) (CodexThreadState, error) {
	if a.protocol != protocolCodexAppServer {
		return CodexThreadState{}, fmt.Errorf("agent is not codex app-server")
	}
	if binding, ok := a.desktopBindingForThread(conversationID, threadID); ok {
		switch binding.Owner {
		case CodexOwnerDesktopLive:
			return a.desktopRuntime.threadState(threadID)
		case CodexOwnerDesktopDisconnected:
			return CodexThreadState{}, ErrCodexDesktopDisconnected
		case CodexOwnerUnknown:
			return CodexThreadState{}, ErrCodexDesktopOwnershipUnknown
		}
	}
	params := map[string]interface{}{"threadId": strings.TrimSpace(threadID), "includeTurns": true}
	result, err := a.rpc(ctx, "thread/read", params)
	if err != nil {
		return CodexThreadState{}, err
	}
	var response codexThreadReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return CodexThreadState{}, fmt.Errorf("parse thread/read result: %w", err)
	}
	return codexThreadStateFromSnapshot(response.Thread), nil
}

// SteerCodexThread 把用户补充输入追加到当前 active turn。
func (a *ACPAgent) SteerCodexThread(ctx context.Context, conversationID string, threadID string, turnID string, message string) error {
	if a.protocol != protocolCodexAppServer {
		return fmt.Errorf("agent is not codex app-server")
	}
	if binding, ok := a.desktopBindingForThread(conversationID, threadID); ok {
		switch binding.Owner {
		case CodexOwnerDesktopDisconnected:
			return ErrCodexDesktopDisconnected
		case CodexOwnerUnknown:
			return ErrCodexDesktopOwnershipUnknown
		case CodexOwnerPersistedOnly:
			return fmt.Errorf("Codex thread 必须先恢复再引导")
		case CodexOwnerDesktopLive:
			return a.desktopRuntime.steerTurn(ctx, codexDesktopSteerTurnSpec{
				ConversationID: threadID, ExpectedTurnID: turnID, Message: message,
			})
		}
	}
	params := map[string]interface{}{
		"threadId":       strings.TrimSpace(threadID),
		"expectedTurnId": strings.TrimSpace(turnID),
		"input":          []codexUserInput{{Type: "text", Text: message}},
	}
	_, err := a.rpc(ctx, "turn/steer", params)
	return err
}

// InterruptCodexThread 中断当前 active turn，用于远程 /stop 接管本地 App 任务。
func (a *ACPAgent) InterruptCodexThread(ctx context.Context, conversationID string, threadID string, turnID string) error {
	if a.protocol != protocolCodexAppServer {
		return fmt.Errorf("agent is not codex app-server")
	}
	if binding, ok := a.desktopBindingForThread(conversationID, threadID); ok {
		switch binding.Owner {
		case CodexOwnerDesktopDisconnected:
			return ErrCodexDesktopDisconnected
		case CodexOwnerUnknown:
			return ErrCodexDesktopOwnershipUnknown
		case CodexOwnerPersistedOnly:
			return fmt.Errorf("Codex thread 必须先恢复再停止")
		case CodexOwnerDesktopLive:
			return a.desktopRuntime.interruptTurn(ctx, threadID, turnID)
		}
	}
	params := map[string]interface{}{
		"threadId": strings.TrimSpace(threadID),
		"turnId":   strings.TrimSpace(turnID),
	}
	_, err := a.rpc(ctx, "turn/interrupt", params)
	return err
}

func codexThreadStateFromSnapshot(thread codexThreadSnapshot) CodexThreadState {
	state := CodexThreadState{ThreadID: strings.TrimSpace(thread.ID)}
	state.Active = thread.Status.Type == "active"
	state.WaitingOnApproval = codexStatusHasFlag(thread.Status.ActiveFlags, "waitingOnApproval")
	state.WaitingOnUserInput = codexStatusHasFlag(thread.Status.ActiveFlags, "waitingOnUserInput")
	state.ActiveTurnID = activeCodexTurnID(thread.Turns)
	state.LastTurnID, state.LastTurnStatus = latestCodexTurnState(thread.Turns)
	state.Preview = latestCodexUserPreview(thread.Turns)
	state.LastAgentMessageText = latestCodexAgentText(thread.Turns)
	return state
}

// latestCodexTurnState 返回 thread/read 中最近 turn 的身份和权威状态。
func latestCodexTurnState(turns []codexTurnSnapshot) (string, string) {
	if len(turns) == 0 {
		return "", ""
	}
	latest := turns[len(turns)-1]
	return strings.TrimSpace(latest.ID), strings.TrimSpace(latest.Status)
}

func activeCodexTurnID(turns []codexTurnSnapshot) string {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Status == "inProgress" {
			return strings.TrimSpace(turns[i].ID)
		}
	}
	return ""
}

func latestCodexUserPreview(turns []codexTurnSnapshot) string {
	for i := len(turns) - 1; i >= 0; i-- {
		for j := len(turns[i].Items) - 1; j >= 0; j-- {
			if turns[i].Items[j].Type == "userMessage" {
				return strings.TrimSpace(codexItemText(turns[i].Items[j]))
			}
		}
	}
	return ""
}

func latestCodexAgentText(turns []codexTurnSnapshot) string {
	for i := len(turns) - 1; i >= 0; i-- {
		for j := len(turns[i].Items) - 1; j >= 0; j-- {
			if turns[i].Items[j].Type == "agentMessage" {
				return strings.TrimSpace(codexItemText(turns[i].Items[j]))
			}
		}
	}
	return ""
}

func codexStatusHasFlag(flags []string, target string) bool {
	for _, flag := range flags {
		if flag == target {
			return true
		}
	}
	return false
}

func codexItemText(item codexThreadItem) string {
	if text := strings.TrimSpace(item.Text); text != "" {
		return text
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(item.Content, &content); err != nil {
		return ""
	}
	var parts []string
	for _, part := range content {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, "\n")
}
