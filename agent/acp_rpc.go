package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/observability"
)

const acpStdinWriteTimeout = 10 * time.Second

type acpWriteDeadlineSetter interface {
	SetWriteDeadline(time.Time) error
}

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
	data, err := marshalRPCNotification(method, params)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	err = a.writeJSONLine(data)
	return err
}

// writeJSONLine 在写入 ACP stdin 前检查 runtime 状态，避免读循环退出后 nil stdin 触发 panic。
func (a *ACPAgent) writeJSONLine(data []byte) error {
	return a.writeJSONLineWithContext(context.Background(), data, observability.TraceContext{})
}

// writeJSONLineWithTrace 在显式诊断开启时把出站请求与当前消息 Trace 关联。
func (a *ACPAgent) writeJSONLineWithTrace(data []byte, trace observability.TraceContext) error {
	return a.writeJSONLineWithContext(context.Background(), data, trace)
}

func (a *ACPAgent) writeJSONLineWithContext(ctx context.Context, data []byte, trace observability.TraceContext) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	stdin := a.stdin
	epoch := a.wireEpoch
	a.mu.Unlock()
	if stdin == nil {
		return fmt.Errorf("ACP runtime is not running")
	}

	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	current := a.stdin != nil && a.wireEpoch == epoch
	a.mu.Unlock()
	if !current {
		return fmt.Errorf("ACP runtime changed while waiting to write")
	}

	deadline := time.Now().Add(acpStdinWriteTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	deadlineWriter, supportsDeadline := stdin.(acpWriteDeadlineSetter)
	deadlineArmed := false
	if supportsDeadline {
		if err := deadlineWriter.SetWriteDeadline(deadline); err != nil {
			if !errors.Is(err, os.ErrNoDeadline) {
				return fmt.Errorf("set ACP stdin write deadline: %w", err)
			}
		} else {
			deadlineArmed = true
		}
	}

	_, err := fmt.Fprintf(stdin, "%s\n", data)
	if deadlineArmed {
		if resetErr := deadlineWriter.SetWriteDeadline(time.Time{}); err == nil && resetErr != nil {
			err = fmt.Errorf("clear ACP stdin write deadline: %w", resetErr)
		}
	}
	if err == nil {
		a.recordProtocolTrace("outbound", epoch, 0, trace, data)
	}
	return err
}

// call sends a JSON-RPC request and waits for the response.
func (a *ACPAgent) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	result, _, err := a.callWithSequence(ctx, method, params)
	return result, err
}

func (a *ACPAgent) callWithSequence(ctx context.Context, method string, params interface{}) (json.RawMessage, uint64, error) {
	id := a.nextID.Add(1)
	ch := a.pending.register(id)
	defer a.pending.remove(id)

	data, err := marshalRPCRequest(id, method, params)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	trace, _ := observability.TraceFromContext(ctx)
	err = a.writeJSONLineWithContext(ctx, data, trace)
	if err != nil {
		return nil, 0, fmt.Errorf("write to stdin: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			msg := formatRPCErrorMessage(resp.Error, a.stderrSnapshot())
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
