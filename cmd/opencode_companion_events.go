package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
)

type openCodeTurnResult struct {
	text string
	err  error
}

func (r *openCodeCompanionRuntime) collectTurnEvents(ctx context.Context, sessionID string, progress func(string), resultCh chan<- openCodeTurnResult) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.withDirectory("/event"), nil)
	if err != nil {
		resultCh <- openCodeTurnResult{err: err}
		return
	}
	resp, err := r.client.Do(req)
	if err != nil {
		resultCh <- openCodeTurnResult{err: err}
		return
	}
	defer resp.Body.Close()
	r.readEventStream(ctx, resp.Body, sessionID, progress, resultCh)
}

func (r *openCodeCompanionRuntime) readEventStream(ctx context.Context, body io.Reader, sessionID string, progress func(string), resultCh chan<- openCodeTurnResult) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	var builder strings.Builder
	var finalText string
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if handleOpenCodeEventLine(payload, sessionID, &builder, &finalText, progress, resultCh) {
			return
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		resultCh <- openCodeTurnResult{err: err}
		return
	}
	if ctx.Err() == nil {
		resultCh <- openCodeTurnResult{err: fmt.Errorf("OpenCode event stream ended before session idle")}
	}
}

func handleOpenCodeEventLine(line string, sessionID string, builder *strings.Builder, finalText *string, progress func(string), resultCh chan<- openCodeTurnResult) bool {
	event, ok := parseOpenCodeEvent(line, sessionID)
	if !ok {
		return false
	}
	switch event.Type {
	case "session.next.text.delta":
		builder.WriteString(event.Delta)
		if progress != nil && event.Delta != "" {
			progress(event.Delta)
		}
	case "session.next.text.ended":
		*finalText = event.Text
	case "session.idle":
		resultCh <- openCodeTurnResult{text: firstOpenCodeText(*finalText, builder.String())}
		return true
	case "session.error", "session.next.step.failed":
		resultCh <- openCodeTurnResult{err: fmt.Errorf("OpenCode 执行失败: %s", strings.TrimSpace(string(event.Error)))}
		return true
	}
	return false
}

type openCodeEvent struct {
	Type  string
	Delta string
	Text  string
	Error json.RawMessage
}

func parseOpenCodeEvent(line string, sessionID string) (openCodeEvent, bool) {
	var raw struct {
		Type       string `json:"type"`
		Properties struct {
			SessionID string          `json:"sessionID"`
			Delta     string          `json:"delta"`
			Text      string          `json:"text"`
			Error     json.RawMessage `json:"error"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return openCodeEvent{}, false
	}
	if raw.Properties.SessionID != "" && raw.Properties.SessionID != sessionID {
		return openCodeEvent{}, false
	}
	return openCodeEvent{
		Type:  raw.Type,
		Delta: raw.Properties.Delta,
		Text:  raw.Properties.Text,
		Error: raw.Properties.Error,
	}, true
}

func firstOpenCodeText(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

func reserveLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", openCodeHost+":0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func (r *openCodeCompanionRuntime) Close() error {
	r.mu.Lock()
	attach := r.attachCmd
	server := r.serverCmd
	r.attachCmd = nil
	r.serverCmd = nil
	r.mu.Unlock()
	stopCommand(attach)
	stopCommand(server)
	return nil
}

func stopCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
