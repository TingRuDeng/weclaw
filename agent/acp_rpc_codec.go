package agent

import "encoding/json"

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC  string          `json:"jsonrpc"`
	ID       *int64          `json:"id,omitempty"`
	Method   string          `json:"method,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *rpcError       `json:"error,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Sequence uint64          `json:"-"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcMessageKind uint8

const (
	rpcMessageNotification rpcMessageKind = iota
	rpcMessageResponse
)

func marshalRPCRequest(id int64, method string, params interface{}) ([]byte, error) {
	return json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
}

func marshalRPCNotification(method string, params interface{}) ([]byte, error) {
	return json.Marshal(rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func unmarshalRPCMessage(line string) (rpcResponse, rpcMessageKind, error) {
	var message rpcResponse
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		return rpcResponse{}, rpcMessageNotification, err
	}
	if message.ID != nil && message.Method == "" {
		return message, rpcMessageResponse, nil
	}
	return message, rpcMessageNotification, nil
}
