package antigravity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSocketPath resolves the sidecar socket below the WeClaw state
// directory. WECLAW_HOME is honored through the same rules as the rest of the
// service, and WECLAW_AG_SOCKET overrides the final path.
func DefaultSocketPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("WECLAW_AG_SOCKET")); override != "" {
		path, err := filepath.Abs(filepath.Clean(override))
		if err != nil {
			return "", fmt.Errorf("resolve WECLAW_AG_SOCKET: %w", err)
		}
		return path, nil
	}
	home := strings.TrimSpace(os.Getenv("WECLAW_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		home = filepath.Join(userHome, ".weclaw")
	}
	abs, err := filepath.Abs(filepath.Clean(home))
	if err != nil {
		return "", fmt.Errorf("resolve WECLAW_HOME: %w", err)
	}
	return filepath.Join(abs, "state", "ag-sidecar.sock"), nil
}

// ResolveSocketPath returns the explicit path when present, otherwise the
// environment- or home-derived default.
func ResolveSocketPath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) == "" {
		return DefaultSocketPath()
	}
	path, err := filepath.Abs(filepath.Clean(explicit))
	if err != nil {
		return "", fmt.Errorf("resolve antigravity socket: %w", err)
	}
	return path, nil
}

// DefaultAppDataDir returns the Antigravity application data directory.
func DefaultAppDataDir() string {
	if override := strings.TrimSpace(os.Getenv("ANTIGRAVITY_APP_DATA_DIR")); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity")
}

// DefaultProjectsDir returns the read-only Antigravity project metadata root.
func DefaultProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "config", "projects")
}

// TranscriptPath builds the transcript path for one conversation while
// rejecting IDs that could escape the brain directory.
func TranscriptPath(appDataDir, conversationID string) (string, error) {
	if err := validateConversationID(conversationID); err != nil {
		return "", err
	}
	return filepath.Join(appDataDir, "brain", conversationID, ".system_generated", "logs", "transcript.jsonl"), nil
}

func validateConversationID(conversationID string) error {
	if conversationID == "" || len(conversationID) > 128 {
		return fmt.Errorf("invalid antigravity conversation id")
	}
	for _, char := range conversationID {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_':
		default:
			return fmt.Errorf("invalid antigravity conversation id")
		}
	}
	return nil
}
