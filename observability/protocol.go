package observability

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	maxProtocolIdentifierBytes = 512
	maxProtocolMethodBytes     = 256
	maxProtocolJSONDepth       = 64
)

// ProtocolRecorder 接收 Agent 线协议事件；Store 决定是否保留脱敏后的正文。
type ProtocolRecorder interface {
	RecordProtocol(ProtocolRecord) error
}

type ProtocolRecord struct {
	Trace     TraceContext
	Direction string
	AgentName string
	Protocol  string
	WireEpoch uint64
	Sequence  uint64
	Raw       []byte
}

func (store *Store) RecordProtocol(record ProtocolRecord) error {
	if store == nil || len(record.Raw) == 0 {
		return nil
	}
	metadata := protocolMetadata(record.Raw)
	trace := record.Trace
	requestKey := protocolRequestKey(record.AgentName, record.WireEpoch, metadata.RequestID)

	store.mu.Lock()
	if requestKey != "" {
		if strings.EqualFold(record.Direction, "outbound") {
			store.rememberProtocolRequestLocked(requestKey, trace)
			if metadata.Method != "" {
				store.requestMethods[requestKey] = metadata.Method
			}
		} else {
			if trace.TraceID == "" {
				trace = store.requestTraces[requestKey]
			}
			if metadata.Method == "" {
				metadata.Method = store.requestMethods[requestKey]
			}
			store.forgetProtocolRequestLocked(requestKey)
		}
	}
	if trace.TraceID == "" {
		trace = store.protocolConversationTraceLocked(record.AgentName, record.WireEpoch, metadata)
	}
	if trace.TraceID != "" {
		store.rememberProtocolConversationLocked(record.AgentName, record.WireEpoch, metadata, trace)
	}
	includePayload := store.includeProtocolPayload
	store.mu.Unlock()

	if trace.TraceID != "" && (metadata.ThreadID != "" || metadata.TurnID != "") {
		trace = trace.WithThreadTurn(firstNonEmpty(metadata.ThreadID, trace.ThreadID), firstNonEmpty(metadata.TurnID, trace.TurnID))
	}
	stage := "protocol." + strings.ToLower(strings.TrimSpace(record.Direction))
	event := Event{Stage: stage, State: "observed"}
	if trace.TraceID != "" {
		event = EventFor(trace, stage, "observed")
	}
	event.Source = strings.TrimSpace(record.Protocol)
	event.AgentName = firstNonEmpty(strings.TrimSpace(record.AgentName), event.AgentName)
	event.Direction = strings.ToLower(strings.TrimSpace(record.Direction))
	event.Method = metadata.Method
	event.RequestID = metadata.RequestID
	event.Sequence = record.Sequence
	event.WireEpoch = record.WireEpoch
	event.ThreadID = firstNonEmpty(metadata.ThreadID, event.ThreadID)
	event.TurnID = firstNonEmpty(metadata.TurnID, event.TurnID)
	event.Summary = protocolSummary(event.Direction, event.Method, event.RequestID)
	if includePayload {
		event.Payload = sanitizeProtocolJSON(record.Raw)
	}
	return store.Record(event)
}

func (store *Store) forgetProtocolRequestLocked(key string) {
	delete(store.requestTraces, key)
	delete(store.requestMethods, key)
	for index, queued := range store.requestOrder {
		if queued != key {
			continue
		}
		store.requestOrder = append(store.requestOrder[:index], store.requestOrder[index+1:]...)
		return
	}
}

type protocolCorrelationRef struct {
	kind string
	key  string
}

func (store *Store) rememberProtocolRequestLocked(key string, trace TraceContext) {
	if _, exists := store.requestTraces[key]; !exists {
		store.requestOrder = append(store.requestOrder, key)
	}
	store.requestTraces[key] = trace
	for len(store.requestTraces) > maxProtocolCorrelations && len(store.requestOrder) > 0 {
		oldest := store.requestOrder[0]
		store.requestOrder = store.requestOrder[1:]
		if _, exists := store.requestTraces[oldest]; !exists {
			continue
		}
		delete(store.requestTraces, oldest)
		delete(store.requestMethods, oldest)
	}
}

func (store *Store) protocolConversationTraceLocked(agentName string, epoch uint64, metadata parsedProtocolMetadata) TraceContext {
	if metadata.TurnID != "" {
		if trace := store.turnTraces[protocolConversationKey(agentName, epoch, metadata.TurnID)]; trace.TraceID != "" {
			return trace
		}
	}
	if metadata.ThreadID != "" {
		return store.threadTraces[protocolConversationKey(agentName, epoch, metadata.ThreadID)]
	}
	return TraceContext{}
}

func (store *Store) rememberProtocolConversationLocked(agentName string, epoch uint64, metadata parsedProtocolMetadata, trace TraceContext) {
	if metadata.ThreadID != "" {
		store.rememberProtocolConversationValueLocked("thread", protocolConversationKey(agentName, epoch, metadata.ThreadID), trace)
	}
	if metadata.TurnID != "" {
		store.rememberProtocolConversationValueLocked("turn", protocolConversationKey(agentName, epoch, metadata.TurnID), trace)
	}
}

func (store *Store) rememberProtocolConversationValueLocked(kind string, key string, trace TraceContext) {
	values := store.threadTraces
	if kind == "turn" {
		values = store.turnTraces
	}
	if _, exists := values[key]; !exists {
		store.conversationOrder = append(store.conversationOrder, protocolCorrelationRef{kind: kind, key: key})
	}
	values[key] = trace
	for len(store.threadTraces)+len(store.turnTraces) > maxProtocolCorrelations && len(store.conversationOrder) > 0 {
		oldest := store.conversationOrder[0]
		store.conversationOrder = store.conversationOrder[1:]
		oldValues := store.threadTraces
		if oldest.kind == "turn" {
			oldValues = store.turnTraces
		}
		delete(oldValues, oldest.key)
	}
}

func protocolConversationKey(agentName string, epoch uint64, id string) string {
	return boundedProtocolValue(agentName, maxProtocolIdentifierBytes) + "\x00" + strconv.FormatUint(epoch, 10) + "\x00" + boundedProtocolValue(id, maxProtocolIdentifierBytes)
}

type parsedProtocolMetadata struct {
	Method    string
	RequestID string
	ThreadID  string
	TurnID    string
}

func protocolMetadata(raw []byte) parsedProtocolMetadata {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return parsedProtocolMetadata{}
	}
	root, _ := value.(map[string]any)
	metadata := parsedProtocolMetadata{
		Method:    boundedProtocolValue(stringValue(root["method"]), maxProtocolMethodBytes),
		RequestID: boundedProtocolValue(stringValue(root["id"]), maxProtocolIdentifierBytes),
	}
	findProtocolIDs(value, "", 0, &metadata)
	return metadata
}

func findProtocolIDs(value any, parentKey string, depth int, metadata *parsedProtocolMetadata) {
	if depth > maxProtocolJSONDepth {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			switch normalizedKey {
			case "threadid":
				if metadata.ThreadID == "" {
					metadata.ThreadID = boundedProtocolValue(stringValue(child), maxProtocolIdentifierBytes)
				}
			case "turnid":
				if metadata.TurnID == "" {
					metadata.TurnID = boundedProtocolValue(stringValue(child), maxProtocolIdentifierBytes)
				}
			case "id":
				switch strings.ToLower(strings.TrimSpace(parentKey)) {
				case "thread":
					if metadata.ThreadID == "" {
						metadata.ThreadID = boundedProtocolValue(stringValue(child), maxProtocolIdentifierBytes)
					}
				case "turn":
					if metadata.TurnID == "" {
						metadata.TurnID = boundedProtocolValue(stringValue(child), maxProtocolIdentifierBytes)
					}
				}
			}
			findProtocolIDs(child, normalizedKey, depth+1, metadata)
		}
	case []any:
		for _, child := range typed {
			findProtocolIDs(child, parentKey, depth+1, metadata)
		}
	}
}

func boundedProtocolValue(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBytes {
		return ""
	}
	return value
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func protocolRequestKey(agentName string, epoch uint64, requestID string) string {
	requestID = boundedProtocolValue(requestID, maxProtocolIdentifierBytes)
	if requestID == "" {
		return ""
	}
	return boundedProtocolValue(agentName, maxProtocolIdentifierBytes) + "\x00" + strconv.FormatUint(epoch, 10) + "\x00" + requestID
}

func protocolSummary(direction string, method string, requestID string) string {
	parts := []string{strings.TrimSpace(direction)}
	if method != "" {
		parts = append(parts, method)
	} else {
		parts = append(parts, "response")
	}
	if requestID != "" {
		parts = append(parts, "id="+requestID)
	}
	return strings.Join(parts, " ")
}

var sensitiveProtocolKeys = map[string]struct{}{
	"accesstoken": {}, "refreshtoken": {}, "idtoken": {}, "apikey": {},
	"authorization": {}, "cookie": {}, "password": {}, "secret": {}, "token": {},
}

func sanitizeProtocolJSON(raw []byte) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return SanitizeText(string(raw))
	}
	value = redactProtocolValue(value)
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return truncateProtocolPayload(string(data))
}

func redactProtocolValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			if _, sensitive := sensitiveProtocolKeys[normalized]; sensitive || strings.HasSuffix(normalized, "token") {
				result[key] = "[REDACTED]"
				continue
			}
			result[key] = redactProtocolValue(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = redactProtocolValue(child)
		}
		return result
	case string:
		return SanitizeText(typed)
	default:
		return typed
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (record ProtocolRecord) String() string {
	metadata := protocolMetadata(record.Raw)
	return fmt.Sprintf("%s %s id=%s", record.Direction, metadata.Method, metadata.RequestID)
}
