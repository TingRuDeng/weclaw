package agent

import (
	"encoding/json"
	"time"
)

const (
	acpScannerInitialBufferSize   = 4 * 1024 * 1024
	acpScannerMaxTokenSize        = 64 * 1024 * 1024
	acpUnhandledMethodLogInterval = 5 * time.Minute
)

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type initParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
}

type clientCapabilities struct {
	FS *fsCapabilities `json:"fs,omitempty"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type newSessionParams struct {
	Cwd        string        `json:"cwd"`
	McpServers []interface{} `json:"mcpServers"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

type promptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptEntry `json:"prompt"`
}

type promptEntry struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

type sessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    sessionUpdate `json:"update"`
}

type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`
	// For agent_message_chunk
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type permissionRequestParams struct {
	ThreadID                string              `json:"threadId,omitempty"`
	TurnID                  string              `json:"turnId,omitempty"`
	ToolCall                json.RawMessage     `json:"toolCall"`
	Command                 permissionCommand   `json:"command,omitempty"`
	Cwd                     string              `json:"cwd,omitempty"`
	Options                 []permissionOption  `json:"options"`
	AvailableDecisions      permissionDecisions `json:"availableDecisions,omitempty"`
	AvailableDecisionsSnake permissionDecisions `json:"available_decisions,omitempty"`
}

type permissionCommand []string

type permissionDecisions []string

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type permissionResponseFormat string

const (
	permissionResponseOutcome  permissionResponseFormat = "outcome"
	permissionResponseDecision permissionResponseFormat = "decision"
)

const (
	protocolLegacyACP      = "legacy_acp"
	protocolCodexAppServer = "codex_app_server"
)

const acpPersistedStateVersion = 1

type acpPersistedState struct {
	Version  int                            `json:"version"`
	Protocol string                         `json:"protocol"`
	Sessions map[string]string              `json:"sessions,omitempty"`
	Threads  map[string]string              `json:"threads,omitempty"`
	History  map[string][]acpHistoryMessage `json:"history,omitempty"`
	Updated  string                         `json:"updatedAt,omitempty"`
}

type acpHistoryMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

const (
	acpMaxHistoryMessages      = 20
	acpMaxRehydratePromptChars = 12000
)

type codexTurnStartParams struct {
	ThreadID       string           `json:"threadId"`
	ApprovalPolicy string           `json:"approvalPolicy,omitempty"`
	Input          []codexUserInput `json:"input"`
	SandboxPolicy  interface{}      `json:"sandboxPolicy,omitempty"`
	Model          string           `json:"model,omitempty"`
	Effort         string           `json:"effort,omitempty"`
	Cwd            string           `json:"cwd,omitempty"`
}

type codexUserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type codexTurnEvent struct {
	Kind     string
	ItemID   string
	Delta    string
	Text     string
	Approval *codexApprovalRequest
}

type codexApprovalRequest struct {
	ID             json.RawMessage
	ResponseFormat permissionResponseFormat
	Request        ApprovalRequest
}
