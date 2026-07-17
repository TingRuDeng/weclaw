package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	codexDesktopEnvelopeRequest           = "request"
	codexDesktopEnvelopeResponse          = "response"
	codexDesktopEnvelopeBroadcast         = "broadcast"
	codexDesktopEnvelopeDiscoveryRequest  = "client-discovery-request"
	codexDesktopEnvelopeDiscoveryResponse = "client-discovery-response"
	codexDesktopResultSuccess             = "success"
	codexDesktopResultError               = "error"
)

var (
	// ErrCodexDesktopIncompatible 供调用方识别必须停止使用当前协议的版本错误。
	ErrCodexDesktopIncompatible = errors.New("Codex Desktop 协议版本不兼容")
	// ErrCodexDesktopUnavailable 表示安全 endpoint 当前不可连接，请求确认未送达。
	ErrCodexDesktopUnavailable = errors.New("Codex Desktop 当前不可用")
	// ErrCodexDesktopDisconnected 表示连接已断开，请求确认未送达。
	ErrCodexDesktopDisconnected = errors.New("Codex Desktop 连接已断开")
	// ErrCodexDesktopDeliveryUnknown 表示请求已写入连接，但无法确认是否被处理。
	ErrCodexDesktopDeliveryUnknown = errors.New("Codex Desktop 请求交付状态未知")
	// ErrCodexDesktopNoClient 表示路由器确认没有客户端可以处理请求。
	ErrCodexDesktopNoClient = errors.New("没有 Codex Desktop 客户端可处理请求")

	codexDesktopMethodVersions = map[string]int{
		"initialize":                                            1,
		"thread-stream-state-changed":                           11,
		"thread-read-state-changed":                             1,
		"thread-queued-followups-changed":                       1,
		"thread-follower-load-complete-history":                 1,
		"thread-follower-start-turn":                            1,
		"thread-follower-steer-turn":                            1,
		"thread-follower-interrupt-turn":                        2,
		"thread-follower-command-approval-decision":             1,
		"thread-follower-file-approval-decision":                1,
		"thread-follower-submit-user-input":                     1,
		"thread-follower-permissions-request-approval-response": 1,
	}
	codexDesktopVersionlessBroadcasts = map[string]bool{
		"client-status-changed":  true,
		"query-cache-invalidate": true,
	}
)

type codexDesktopEnvelope struct {
	Type           string          `json:"type"`
	RequestID      string          `json:"requestId,omitempty"`
	SourceClientID string          `json:"sourceClientId,omitempty"`
	Version        int             `json:"version,omitempty"`
	Method         string          `json:"method,omitempty"`
	Params         json.RawMessage `json:"params,omitempty"`
	ResultType     string          `json:"resultType,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	Request        json.RawMessage `json:"request,omitempty"`
	Response       json.RawMessage `json:"response,omitempty"`
}

type codexDesktopRequestSpec struct {
	RequestID      string
	SourceClientID string
	Method         string
	Params         any
}

type codexDesktopDiscoverySpec struct {
	RequestID      string
	SourceClientID string
	Method         string
	Params         any
}

func decodeCodexDesktopEnvelope(payload []byte) (codexDesktopEnvelope, error) {
	var envelope codexDesktopEnvelope
	if len(strings.TrimSpace(string(payload))) == 0 || !utf8.Valid(payload) {
		return envelope, fmt.Errorf("Codex Desktop envelope 不是有效 JSON")
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return envelope, fmt.Errorf("解析 Codex Desktop envelope JSON: %w", err)
	}
	if err := validateCodexDesktopEnvelope(envelope); err != nil {
		return envelope, err
	}
	return envelope, nil
}

func encodeCodexDesktopEnvelope(envelope codexDesktopEnvelope) ([]byte, error) {
	if err := validateCodexDesktopEnvelope(envelope); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("编码 Codex Desktop envelope: %w", err)
	}
	return payload, nil
}

func validateCodexDesktopEnvelope(envelope codexDesktopEnvelope) error {
	if envelope.Type == "" {
		return fmt.Errorf("Codex Desktop envelope 缺少 type")
	}
	switch envelope.Type {
	case codexDesktopEnvelopeRequest:
		return validateCodexDesktopRequest(envelope)
	case codexDesktopEnvelopeBroadcast:
		return validateCodexDesktopBroadcast(envelope)
	case codexDesktopEnvelopeResponse:
		return validateCodexDesktopResponse(envelope)
	case codexDesktopEnvelopeDiscoveryRequest:
		return validateCodexDesktopDiscoveryRequest(envelope)
	case codexDesktopEnvelopeDiscoveryResponse:
		return validateCodexDesktopDiscoveryResponse(envelope)
	default:
		return fmt.Errorf("Codex Desktop envelope type %q 不受支持", envelope.Type)
	}
}

func validateCodexDesktopRequest(envelope codexDesktopEnvelope) error {
	if err := validateCodexDesktopMethodEnvelope(envelope, true, false); err != nil {
		return err
	}
	if isMissingOrNullCodexDesktopJSON(envelope.Params) {
		return fmt.Errorf("Codex Desktop request envelope 缺少非空 params 对象")
	}
	trimmed := bytes.TrimSpace(envelope.Params)
	if trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("Codex Desktop request envelope params 必须为 JSON 对象")
	}
	return nil
}

func validateCodexDesktopBroadcast(envelope codexDesktopEnvelope) error {
	if err := validateCodexDesktopMethodEnvelope(
		envelope, false, codexDesktopVersionlessBroadcasts[envelope.Method],
	); err != nil {
		return err
	}
	if isMissingOrNullCodexDesktopJSON(envelope.Params) {
		return fmt.Errorf("Codex Desktop broadcast envelope 缺少非空 params")
	}
	return nil
}

func validateCodexDesktopResponse(envelope codexDesktopEnvelope) error {
	if err := requireCodexDesktopRequestID(envelope); err != nil {
		return err
	}
	switch envelope.ResultType {
	case codexDesktopResultSuccess:
		// RawMessage 长度为零表示字段缺失；字面 null 长度非零，属于合法显式结果。
		if len(envelope.Result) == 0 {
			return fmt.Errorf("Codex Desktop success response 缺少 result")
		}
	case codexDesktopResultError:
		if strings.TrimSpace(envelope.Error) == "" {
			return fmt.Errorf("Codex Desktop error response 缺少非空 error")
		}
	default:
		return fmt.Errorf("Codex Desktop response resultType %q 无效", envelope.ResultType)
	}
	return nil
}

func validateCodexDesktopMethodEnvelope(envelope codexDesktopEnvelope, requestIDRequired bool, allowVersionless bool) error {
	if requestIDRequired {
		if err := requireCodexDesktopRequestID(envelope); err != nil {
			return err
		}
	}
	if envelope.Method == "" {
		return fmt.Errorf("Codex Desktop %s envelope 缺少 method", envelope.Type)
	}
	if envelope.Version <= 0 {
		if envelope.Version == 0 && allowVersionless {
			return nil
		}
		return fmt.Errorf("Codex Desktop method %q 缺少有效 version", envelope.Method)
	}
	return validateCodexDesktopMethodVersion(envelope.Method, envelope.Version)
}

func validateCodexDesktopMethodVersion(method string, version int) error {
	expected, known := codexDesktopMethodVersions[method]
	if !known || version == expected {
		return nil
	}
	return fmt.Errorf("%w: method %s@%d，要求 @%d", ErrCodexDesktopIncompatible, method, version, expected)
}

func validateCodexDesktopDiscoveryRequest(envelope codexDesktopEnvelope) error {
	if err := requireCodexDesktopRequestID(envelope); err != nil {
		return err
	}
	if len(envelope.Request) == 0 {
		return fmt.Errorf("Codex Desktop client-discovery-request 缺少 request")
	}
	trimmed := bytes.TrimSpace(envelope.Request)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("Codex Desktop discovery 嵌套 request 必须为 JSON 对象")
	}
	var nested struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &nested); err != nil {
		return fmt.Errorf("解析 Codex Desktop discovery 嵌套 request: %w", err)
	}
	if nested.Type != codexDesktopEnvelopeRequest {
		return fmt.Errorf("Codex Desktop discovery 嵌套 request type 为 %q", nested.Type)
	}
	return nil
}

func validateCodexDesktopDiscoveryResponse(envelope codexDesktopEnvelope) error {
	if err := requireCodexDesktopRequestID(envelope); err != nil {
		return err
	}
	if isMissingOrNullCodexDesktopJSON(envelope.Response) {
		return fmt.Errorf("Codex Desktop client-discovery-response 缺少非空 response 对象")
	}
	trimmed := bytes.TrimSpace(envelope.Response)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("Codex Desktop client-discovery-response response 必须为对象")
	}
	var response struct {
		CanHandle *bool `json:"canHandle"`
	}
	if err := json.Unmarshal(envelope.Response, &response); err != nil {
		return fmt.Errorf("Codex Desktop discovery response canHandle 必须为 bool: %w", err)
	}
	if response.CanHandle == nil {
		return fmt.Errorf("Codex Desktop discovery response 缺少 bool 类型 canHandle")
	}
	return nil
}

func isMissingOrNullCodexDesktopJSON(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func requireCodexDesktopRequestID(envelope codexDesktopEnvelope) error {
	if envelope.RequestID == "" {
		return fmt.Errorf("Codex Desktop %s envelope 缺少 requestId", envelope.Type)
	}
	return nil
}

func newCodexDesktopRequest(spec codexDesktopRequestSpec) (codexDesktopEnvelope, error) {
	params, err := marshalCodexDesktopProtocolValue("params", spec.Params)
	if err != nil {
		return codexDesktopEnvelope{}, err
	}
	envelope := codexDesktopEnvelope{
		Type:           codexDesktopEnvelopeRequest,
		RequestID:      spec.RequestID,
		SourceClientID: spec.SourceClientID,
		Version:        codexDesktopMethodVersions[spec.Method],
		Method:         spec.Method,
		Params:         params,
	}
	return envelope, validateCodexDesktopEnvelope(envelope)
}

func newCodexDesktopDiscoveryRequest(spec codexDesktopDiscoverySpec) (codexDesktopEnvelope, error) {
	nested, err := newCodexDesktopRequest(codexDesktopRequestSpec(spec))
	if err != nil {
		return codexDesktopEnvelope{}, err
	}
	request, err := marshalCodexDesktopProtocolValue("discovery request", nested)
	if err != nil {
		return codexDesktopEnvelope{}, err
	}
	envelope := codexDesktopEnvelope{
		Type:           codexDesktopEnvelopeDiscoveryRequest,
		RequestID:      spec.RequestID,
		SourceClientID: spec.SourceClientID,
		Request:        request,
	}
	return envelope, validateCodexDesktopEnvelope(envelope)
}

func marshalCodexDesktopProtocolValue(name string, value any) (json.RawMessage, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("编码 Codex Desktop %s: %w", name, err)
	}
	return payload, nil
}
