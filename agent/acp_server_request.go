package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/observability"
)

const rpcMethodNotFound = -32601
const rpcInvalidParams = -32602

type rpcServerResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type dynamicToolCallResult struct {
	ContentItems []dynamicToolCallContentItem `json:"contentItems"`
	Success      bool                         `json:"success"`
}

type dynamicToolCallContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

const (
	unsupportedDynamicToolMessage = "WeClaw cannot execute this Codex dynamic tool. Continue with tools available to the current runtime."
	codexAppDynamicToolMessage    = "该动态工具由 Codex App 执行，请在 Codex App 中完成。WeClaw 新建共享会话请使用 /cx new。"
)

type codexDynamicToolCallParams struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	CallID    string `json:"callId"`
	Namespace string `json:"namespace"`
	Tool      string `json:"tool"`
}

func decodeCodexDynamicToolCall(params json.RawMessage) (codexDynamicToolCallParams, error) {
	var call codexDynamicToolCallParams
	raw := strings.TrimSpace(string(params))
	if raw == "" || raw == "null" {
		return call, fmt.Errorf("dynamic tool call params are missing")
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return call, fmt.Errorf("decode dynamic tool call params: %w", err)
	}
	if strings.TrimSpace(call.ThreadID) == "" ||
		strings.TrimSpace(call.TurnID) == "" ||
		strings.TrimSpace(call.CallID) == "" ||
		strings.TrimSpace(call.Tool) == "" {
		return call, fmt.Errorf("dynamic tool call is missing thread, turn, call, or tool identity")
	}
	return call, nil
}

func (call codexDynamicToolCallParams) isCodexAppTool() bool {
	namespace := strings.ToLower(strings.TrimSpace(call.Namespace))
	tool := strings.ToLower(strings.TrimSpace(call.Tool))
	return namespace == "codex_app" || strings.HasPrefix(tool, "codex_app__")
}

// codexAppFrontendPresent uses the existing local Desktop presence seams. It
// never opens an ACP/RPC connection and therefore cannot recursively wait on
// the request currently being dispatched.
func (a *ACPAgent) codexAppFrontendPresent() bool {
	var socketExists, processExists bool
	switch {
	case a.codexDesktopPresenceCall != nil:
		socketExists, processExists = a.codexDesktopPresenceCall()
	case a.desktopProbe != nil:
		socketExists, processExists = a.desktopProbe.Presence()
	default:
		socketExists, processExists = codexDesktopPresence()
	}
	return socketExists || processExists
}

func (a *ACPAgent) shouldDeferCodexAppDynamicToolCall() bool {
	sharedApp := a.codexHostMode == codexHostModeShared
	return (sharedApp || (a.usesOfficialCodexDaemon() && a.codexDesktopCoordination)) && a.codexAppFrontendPresent()
}

func (a *ACPAgent) logCodexDynamicToolCall(call codexDynamicToolCallParams, deferred bool) {
	decision := "rejected"
	if deferred {
		decision = "deferred-to-codex-app"
	}
	// Keep the rate-limit key finite even if a malformed peer sends arbitrary
	// namespace/tool strings repeatedly.
	key := "codex-dynamic-tool:" + decision
	if !a.shouldLogUnhandledMethod(key, time.Now()) {
		return
	}
	log.Printf(
		"[acp] Codex dynamic tool %s namespace=%q tool=%q thread=%q turn=%q call=%q",
		decision,
		observability.SanitizeText(call.Namespace),
		observability.SanitizeText(call.Tool),
		dynamicToolLogID(call.ThreadID),
		dynamicToolLogID(call.TurnID),
		dynamicToolLogID(call.CallID),
	)
}

func dynamicToolLogID(value string) string {
	value = observability.SanitizeText(value)
	runes := []rune(value)
	if len(runes) > 16 {
		return string(runes[:16]) + "…"
	}
	return value
}

// dispatchACPServerRequest routes requests owned by this frontend and leaves
// a Codex App-owned dynamic tool unanswered when another shared-daemon client
// is present. The daemon will broadcast serverRequest/resolved after that
// client replies.
func (a *ACPAgent) dispatchACPServerRequest(msg rpcResponse, line string) error {
	if msg.ID == nil {
		return fmt.Errorf("server request %q has no id", msg.Method)
	}
	switch msg.Method {
	case "session/request_permission", "turn/approval/request",
		"item/fileChange/requestApproval", "item/commandExecution/requestApproval",
		"item/permissions/requestApproval":
		a.handlePermissionRequestAt(line, msg.Sequence)
		return nil
	case "item/tool/call":
		call, decodeErr := decodeCodexDynamicToolCall(msg.Params)
		if decodeErr == nil && call.isCodexAppTool() {
			if a.shouldDeferCodexAppDynamicToolCall() {
				a.logCodexDynamicToolCall(call, true)
				return nil
			}
			a.logCodexDynamicToolCall(call, false)
			return a.writeDynamicToolFailure(*msg.ID, codexAppDynamicToolMessage)
		}
		if decodeErr != nil && a.shouldLogUnhandledMethod("codex-dynamic-tool:malformed", time.Now()) {
			log.Printf("[acp] rejecting malformed Codex dynamic tool request: %v", decodeErr)
		}
		return a.writeDynamicToolFailure(*msg.ID, unsupportedDynamicToolMessage)
	case "item/tool/requestUserInput":
		return a.handleToolUserInputRequest(msg)
	default:
		return a.writeRPCServerResponse(rpcServerResponse{
			JSONRPC: "2.0",
			ID:      *msg.ID,
			Error: &rpcError{
				Code:    rpcMethodNotFound,
				Message: "Method not found",
			},
		})
	}
}

func (a *ACPAgent) writeDynamicToolFailure(id int64, message string) error {
	return a.writeRPCServerResponse(rpcServerResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: dynamicToolCallResult{
			ContentItems: []dynamicToolCallContentItem{{
				Type: "inputText",
				Text: message,
			}},
			Success: false,
		},
	})
}

func (a *ACPAgent) writeRPCServerError(id int64, code int, message string) error {
	return a.writeRPCServerResponse(rpcServerResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	})
}

func (a *ACPAgent) writeRPCServerResponse(response rpcServerResponse) error {
	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal server request response: %w", err)
	}
	if err := a.writeJSONLine(data); err != nil {
		return fmt.Errorf("write server request response: %w", err)
	}
	return nil
}
