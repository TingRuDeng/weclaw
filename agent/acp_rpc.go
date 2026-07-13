package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func (a *ACPAgent) rpc(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	result, _, err := a.rpcWithSequence(ctx, method, params)
	return result, err
}

func (a *ACPAgent) rpcWithSequence(ctx context.Context, method string, params interface{}) (json.RawMessage, uint64, error) {
	if a.rpcCall != nil {
		result, err := a.rpcCall(ctx, method, params)
		return result, 0, err
	}
	return a.callWithSequence(ctx, method, params)
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (a *ACPAgent) notify(method string, params interface{}) error {
	msg := struct {
		JSONRPC string      `json:"jsonrpc"`
		Method  string      `json:"method"`
		Params  interface{} `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	err = a.writeJSONLine(data)
	return err
}

// writeJSONLine 在写入 ACP stdin 前检查 runtime 状态，避免读循环退出后 nil stdin 触发 panic。
func (a *ACPAgent) writeJSONLine(data []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin == nil {
		return fmt.Errorf("ACP runtime is not running")
	}
	_, err := fmt.Fprintf(a.stdin, "%s\n", data)
	return err
}

// call sends a JSON-RPC request and waits for the response.
func (a *ACPAgent) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	result, _, err := a.callWithSequence(ctx, method, params)
	return result, err
}

func (a *ACPAgent) callWithSequence(ctx context.Context, method string, params interface{}) (json.RawMessage, uint64, error) {
	id := a.nextID.Add(1)

	ch := make(chan *rpcResponse, 1)
	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()

	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
	}()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	err = a.writeJSONLine(data)
	if err != nil {
		return nil, 0, fmt.Errorf("write to stdin: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			msg := formatRPCErrorMessage(resp.Error, a.stderr)
			return nil, resp.Sequence, fmt.Errorf("agent error: %s", msg)
		}
		return resp.Result, resp.Sequence, nil
	}
}

// formatRPCErrorMessage 保留 JSON-RPC error 的结构化信息，并避免 stderr 的残缺 JSON 片段覆盖主错误。
func formatRPCErrorMessage(rpcErr *rpcError, stderr *acpStderrWriter) string {
	var parts []string
	if rpcErr != nil {
		if message := strings.TrimSpace(rpcErr.Message); message != "" {
			parts = append(parts, message)
		}
		if data := formatRPCErrorData(rpcErr.Data); data != "" {
			parts = append(parts, data)
		}
	}
	if stderr != nil {
		if detail := normalizeStderrDetail(stderr.LastError()); detail != "" {
			parts = append(parts, detail)
		}
	}
	if len(parts) == 0 {
		return "未知 Agent 错误"
	}
	return strings.Join(dedupeStrings(parts), "；")
}

func formatRPCErrorData(data json.RawMessage) string {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" || text == "{}" {
		return ""
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	var asObject map[string]interface{}
	if err := json.Unmarshal(data, &asObject); err == nil {
		return flattenJSONMap(asObject)
	}
	return normalizeStderrDetail(text)
}

func flattenJSONMap(values map[string]interface{}) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(values[key]))
		if value != "" && value != "<nil>" {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, ", ")
}

func normalizeStderrDetail(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || text == "}" || text == "]" || text == "{" || text == "[" {
		return ""
	}
	return text
}

func dedupeStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
