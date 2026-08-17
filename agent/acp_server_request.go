package agent

import (
	"encoding/json"
	"fmt"
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

// dispatchACPServerRequest guarantees that every server-initiated JSON-RPC
// request is either routed to its existing responder or answered immediately.
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
		return a.writeRPCServerResponse(rpcServerResponse{
			JSONRPC: "2.0",
			ID:      *msg.ID,
			Result: dynamicToolCallResult{
				ContentItems: []dynamicToolCallContentItem{{
					Type: "inputText",
					Text: "WeClaw cannot execute this Codex dynamic tool. Continue with tools available to the current runtime.",
				}},
				Success: false,
			},
		})
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
