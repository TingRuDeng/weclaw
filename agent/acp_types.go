package agent

import (
	"context"
	"encoding/json"
	"time"
)

const (
	acpProtocolVersion            = 1
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

type acpInitializeResult struct {
	ProtocolVersion   int                    `json:"protocolVersion"`
	AgentInfo         acpInitializeAgentInfo `json:"agentInfo"`
	AgentCapabilities acpAgentCapabilities   `json:"agentCapabilities"`
}

type acpInitializeAgentInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

type acpAgentCapabilities struct {
	Session acpInitializeSessionCapabilities `json:"sessionCapabilities"`
}

type acpInitializeSessionCapabilities struct {
	List   json.RawMessage `json:"list"`
	Resume json.RawMessage `json:"resume"`
}

type newSessionParams struct {
	Cwd        string        `json:"cwd"`
	McpServers []interface{} `json:"mcpServers"`
}

type newSessionResult struct {
	SessionID     string                   `json:"sessionId"`
	ConfigOptions []acpSessionConfigOption `json:"configOptions,omitempty"`
}

type acpSessionConfigOption struct {
	ID           string                   `json:"id"`
	CurrentValue string                   `json:"currentValue,omitempty"`
	Options      []acpSessionConfigChoice `json:"options,omitempty"`
}

type acpSessionConfigChoice struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type sessionConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type sessionConfigOptionResult struct {
	ConfigOptions []acpSessionConfigOption `json:"configOptions,omitempty"`
}

type promptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptEntry `json:"prompt"`
}

type promptEntry struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
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
	SessionID               string              `json:"sessionId,omitempty"`
	ThreadID                string              `json:"threadId,omitempty"`
	TurnID                  string              `json:"turnId,omitempty"`
	ToolCall                json.RawMessage     `json:"toolCall"`
	Command                 permissionCommand   `json:"command,omitempty"`
	Cwd                     string              `json:"cwd,omitempty"`
	Reason                  string              `json:"reason,omitempty"`
	Permissions             json.RawMessage     `json:"permissions,omitempty"`
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
	permissionResponseOutcome     permissionResponseFormat = "outcome"
	permissionResponseDecision    permissionResponseFormat = "decision"
	permissionResponsePermissions permissionResponseFormat = "permissions"
)

const (
	protocolLegacyACP      = "legacy_acp"
	protocolCodexAppServer = "codex_app_server"
)

const acpPersistedStateVersion = 2

type acpPersistedState struct {
	Version      int                           `json:"version"`
	Protocol     string                        `json:"protocol"`
	Sessions     map[string]string             `json:"sessions,omitempty"`
	Threads      map[string]string             `json:"threads,omitempty"`
	LiveBindings map[string]CodexThreadBinding `json:"liveBindings,omitempty"`
	Updated      string                        `json:"updatedAt,omitempty"`
}

type codexTurnStartParams struct {
	ThreadID          string           `json:"threadId"`
	ApprovalPolicy    string           `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer string           `json:"approvalsReviewer,omitempty"`
	Input             []codexUserInput `json:"input"`
	SandboxPolicy     interface{}      `json:"sandboxPolicy,omitempty"`
	Model             string           `json:"model,omitempty"`
	Effort            string           `json:"effort,omitempty"`
	Cwd               string           `json:"cwd,omitempty"`
}

type codexUserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type codexTurnEvent struct {
	Kind      string
	TurnID    string
	ItemID    string
	Delta     string
	Text      string
	Progress  *codexProgressEvent
	Approval  *codexApprovalRequest
	UserInput *codexUserInputEvent
}

type codexProgressEvent struct {
	Kind     string
	Action   string
	Detail   string
	FilePath string
}

type codexApprovalRequest struct {
	ID                   json.RawMessage
	ResponseFormat       permissionResponseFormat
	RequestedPermissions json.RawMessage
	Request              ApprovalRequest
	Respond              func(context.Context, string) error
}
