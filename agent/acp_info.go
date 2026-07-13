package agent

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
)

// Info returns metadata about this agent.
func (a *ACPAgent) Info() AgentInfo {
	config := a.modelConfigSnapshot()
	info := AgentInfo{
		Name:         a.command,
		Type:         "acp",
		Model:        config.model,
		Effort:       config.effort,
		Command:      a.command,
		LocalCommand: a.localCommand,
	}
	a.mu.Lock()
	if a.cmd != nil && a.cmd.Process != nil {
		info.PID = a.cmd.Process.Pid
	}
	a.mu.Unlock()
	return info
}

func extractChunkText(update *sessionUpdate) string {
	// The content field in agent_message_chunk can be a text content block
	if update.Text != "" {
		return update.Text
	}

	// Try to extract from content JSON
	if update.Content != nil {
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(update.Content, &content); err == nil && content.Type == "text" && content.Text != "" {
			return content.Text
		}
	}

	return ""
}

// extractPromptResultText tries to extract text from the session/prompt response.
// Some ACP agents include response content in the result alongside stopReason.
func extractPromptResultText(result json.RawMessage) string {
	if result == nil {
		return ""
	}

	// Try to extract content array from result
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// Some agents use a flat text field
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ""
	}

	if r.Text != "" {
		return r.Text
	}

	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "")
}

// acpStderrWriter forwards the ACP subprocess stderr to the application log
// and captures the last meaningful error line.
type acpStderrWriter struct {
	prefix string
	mu     sync.Mutex
	last   string // last non-empty, non-traceback line
}

func (w *acpStderrWriter) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimRight(string(p), "\n"), "\n")
	w.mu.Lock()
	for _, line := range lines {
		if line != "" {
			log.Printf("%s %s", w.prefix, line)
			// Capture lines that look like actual error messages (not traceback frames)
			if !strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "Traceback") && !strings.HasPrefix(line, "...") {
				w.last = line
			}
		}
	}
	w.mu.Unlock()
	return len(p), nil
}

// LastError returns the last captured error line and resets it.
func (w *acpStderrWriter) LastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.last
	w.last = ""
	return s
}
