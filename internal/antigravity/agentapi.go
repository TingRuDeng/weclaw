package antigravity

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecAgentRunner invokes the official agentapi binary with an argument array;
// no shell is involved.
type ExecAgentRunner struct {
	path string
}

// NewAgentRunner resolves agentapi from PATH first, then the Antigravity
// application data bin directory and the standard installation path.
func NewAgentRunner(appDataDir string) (*ExecAgentRunner, error) {
	if path, err := exec.LookPath("agentapi"); err == nil && strings.TrimSpace(path) != "" {
		return &ExecAgentRunner{path: path}, nil
	}
	candidates := make([]string, 0, 2)
	if strings.TrimSpace(appDataDir) != "" {
		candidates = append(candidates, filepath.Join(appDataDir, "bin", "agentapi"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".gemini", "antigravity", "bin", "agentapi"))
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return &ExecAgentRunner{path: candidate}, nil
		}
	}
	return nil, fmt.Errorf("agentapi not found in PATH or the Antigravity application directory")
}

// Path returns the resolved binary path for diagnostics.
func (r *ExecAgentRunner) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

func (r *ExecAgentRunner) SendMessage(ctx context.Context, conversationID string, content string) error {
	if r == nil || strings.TrimSpace(r.path) == "" {
		return fmt.Errorf("agentapi runner is not configured")
	}
	if err := validateConversationID(conversationID); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("antigravity message content is empty")
	}
	command := exec.CommandContext(ctx, r.path, "send-message", conversationID, content)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agentapi send-message failed: %w: %s", err, sanitizeTranscriptText(string(output), 200))
	}
	return nil
}
