package antigravity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ReadTranscript reads complete JSON lines from offset and reports whether the
// cursor is still continuous with the current file identity.
func ReadTranscript(path, requestedFileID string, requestedOffset int64) (ObserveResult, error) {
	if requestedOffset < 0 {
		return ObserveResult{}, fmt.Errorf("transcript offset must be non-negative")
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ObserveResult{NextFileID: requestedFileID, NextOffset: requestedOffset}, nil
		}
		return ObserveResult{}, fmt.Errorf("open antigravity transcript: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ObserveResult{}, fmt.Errorf("stat antigravity transcript: %w", err)
	}
	fileID := transcriptFileID(info)
	size := info.Size()
	result := ObserveResult{NextFileID: fileID, NextOffset: requestedOffset}
	if requestedFileID != "" && requestedFileID != fileID {
		result.Discontinuous = true
	}
	if requestedOffset > size {
		result.Discontinuous = true
	}
	if result.Discontinuous {
		result.NextOffset = 0
	}
	if result.NextOffset > size {
		result.NextOffset = 0
	}
	if result.NextOffset == size {
		return result, nil
	}
	chunk := make([]byte, maxTranscriptChunk)
	readSize := size - result.NextOffset
	if readSize > int64(len(chunk)) {
		readSize = int64(len(chunk))
	}
	n, readErr := file.ReadAt(chunk[:readSize], result.NextOffset)
	if readErr != nil && readErr != io.EOF {
		return ObserveResult{}, fmt.Errorf("read antigravity transcript: %w", readErr)
	}
	data := chunk[:n]
	complete := bytes.LastIndexByte(data, '\n')
	if complete < 0 {
		return result, nil
	}
	data = data[:complete+1]
	cursor := result.NextOffset
	for len(data) > 0 {
		lineEnd := bytes.IndexByte(data, '\n')
		if lineEnd < 0 {
			break
		}
		line := data[:lineEnd]
		if events, ok := parseTranscriptLine(line, cursor); ok {
			result.Events = append(result.Events, events...)
		}
		cursor += int64(lineEnd) + 1
		data = data[lineEnd+1:]
	}
	result.NextOffset = cursor
	return result, nil
}

// ReadBaseline returns the current complete-line frontier and latest candidate
// reply for a bound conversation.
func ReadBaseline(path string) (Baseline, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Baseline{}, nil
		}
		return Baseline{}, fmt.Errorf("open antigravity transcript: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Baseline{}, fmt.Errorf("stat antigravity transcript: %w", err)
	}
	baseline := Baseline{FileID: transcriptFileID(info)}
	chunk := make([]byte, maxTranscriptChunk)
	var offset int64
	for offset < info.Size() {
		readSize := info.Size() - offset
		if readSize > int64(len(chunk)) {
			readSize = int64(len(chunk))
		}
		n, readErr := file.ReadAt(chunk[:readSize], offset)
		if readErr != nil && readErr != io.EOF {
			return Baseline{}, fmt.Errorf("read antigravity transcript: %w", readErr)
		}
		data := chunk[:n]
		complete := bytes.LastIndexByte(data, '\n')
		if complete < 0 {
			break
		}
		data = data[:complete+1]
		for len(data) > 0 {
			lineEnd := bytes.IndexByte(data, '\n')
			if lineEnd < 0 {
				break
			}
			if events, ok := parseTranscriptLine(data[:lineEnd], offset); ok {
				baseline.HasHistory = true
				for _, event := range events {
					if event.Kind == EventReply {
						baseline.LatestReply = event.Text
					}
				}
			}
			offset += int64(lineEnd) + 1
			data = data[lineEnd+1:]
		}
		if n == 0 {
			break
		}
	}
	baseline.Offset = offset
	return baseline, nil
}

func parseTranscriptLine(line []byte, offset int64) ([]Event, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, false
	}
	kind := strings.ToUpper(firstJSONString(raw, "type", "kind", "event", "event_type"))
	content := transcriptContent(raw)
	switch kind {
	case "USER_INPUT", "SYSTEM_MESSAGE":
		text := sanitizeTranscriptText(content, 0)
		if text == "" {
			return nil, true
		}
		return []Event{{
			Kind:       EventTurnInput,
			Text:       text,
			FromFeishu: strings.Contains(text, "[WeClaw/飞书]"),
			Offset:     offset,
		}}, true
	case "ERROR_MESSAGE":
		text := sanitizeTranscriptText(content, 200)
		if text == "" {
			text = "Antigravity 报告了一个错误"
		}
		return []Event{{Kind: EventError, Text: text, Offset: offset}}, true
	case "PLANNER_RESPONSE":
		toolCalls := transcriptToolCalls(raw)
		events := make([]Event, 0, 1+len(toolCalls))
		if text := sanitizeTranscriptText(content, 0); text != "" {
			eventKind := EventProgress
			if len(toolCalls) == 0 {
				eventKind = EventReply
			}
			events = append(events, Event{Kind: eventKind, Text: text, Offset: offset})
		}
		for _, call := range toolCalls {
			name := strings.TrimSpace(call.name)
			switch name {
			case "update_focus":
				focus := sanitizeTranscriptText(call.focus, 160)
				if focus != "" {
					events = append(events, Event{Kind: EventFocus, Focus: focus, Text: focus, Offset: offset})
				}
			case "ask_question":
				events = append(events, Event{Kind: EventAwaitDesktop, Text: "等待桌面端回答", Offset: offset})
			default:
				if name != "" {
					events = append(events, Event{Kind: EventTool, Tool: name, Offset: offset})
				}
			}
		}
		return events, true
	default:
		return nil, true
	}
}

type transcriptCall struct {
	name  string
	focus string
}

func transcriptToolCalls(raw map[string]json.RawMessage) []transcriptCall {
	value := firstJSONRaw(raw, "tool_calls", "toolCalls", "toolCallsList")
	if len(value) == 0 {
		return nil
	}
	var calls []map[string]json.RawMessage
	if err := json.Unmarshal(value, &calls); err != nil {
		return nil
	}
	result := make([]transcriptCall, 0, len(calls))
	for _, call := range calls {
		name := firstJSONString(call, "name", "tool", "tool_name", "toolName")
		if name == "" {
			if function := firstJSONRaw(call, "function"); len(function) > 0 {
				var nested map[string]json.RawMessage
				if json.Unmarshal(function, &nested) == nil {
					name = firstJSONString(nested, "name")
				}
			}
		}
		focus := ""
		argsValue := firstJSONRaw(call, "args", "arguments", "input")
		if len(argsValue) > 0 {
			var args map[string]json.RawMessage
			if json.Unmarshal(argsValue, &args) == nil {
				focus = firstJSONString(args, "focus", "summary", "text", "title")
			}
		}
		result = append(result, transcriptCall{name: strings.ToLower(name), focus: focus})
	}
	return result
}

func transcriptContent(raw map[string]json.RawMessage) string {
	for _, key := range []string{"content", "text", "message", "response"} {
		value := firstJSONRaw(raw, key)
		if len(value) == 0 {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) == nil {
			return text
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(value, &nested) == nil {
			return firstJSONString(nested, "text", "content")
		}
	}
	return ""
}

func firstJSONRaw(raw map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if value, ok := raw[key]; ok && len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstJSONString(raw map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		value := firstJSONRaw(raw, key)
		if len(value) == 0 {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) == nil {
			return text
		}
	}
	return ""
}

var transcriptSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(token|secret|password|api[_-]?key|authorization)\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`),
}

func sanitizeTranscriptText(text string, limit int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Join(strings.Fields(text), " ")
	for _, pattern := range transcriptSecretPatterns {
		text = pattern.ReplaceAllString(text, "[redacted]")
	}
	if limit > 0 && utf8.RuneCountInString(text) > limit {
		runes := []rune(text)
		text = string(runes[:limit]) + "…"
	}
	return text
}
