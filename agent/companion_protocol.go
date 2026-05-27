package agent

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	companionProtocolVersion = 1

	companionMessageHello    = "hello"
	companionMessageRequest  = "request"
	companionMessageResponse = "response"
	companionMessageEvent    = "event"
)

var companionAgentNamePattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// CompanionEndpoint 是后台 WeClaw 暴露给本地可见 Companion 的连接入口。
type CompanionEndpoint struct {
	ProtocolVersion int       `json:"protocol_version"`
	Agent           string    `json:"agent"`
	Host            string    `json:"host"`
	Port            int       `json:"port"`
	Token           string    `json:"token"`
	Cwd             string    `json:"cwd"`
	Command         string    `json:"command"`
	Args            []string  `json:"args,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

func (e CompanionEndpoint) Address() string {
	return net.JoinHostPort(e.Host, fmt.Sprintf("%d", e.Port))
}

type companionEnvelope struct {
	Type     string             `json:"type"`
	ID       string             `json:"id,omitempty"`
	Token    string             `json:"token,omitempty"`
	PID      int                `json:"pid,omitempty"`
	Request  *companionRequest  `json:"request,omitempty"`
	Response *companionResponse `json:"response,omitempty"`
	Event    *companionEvent    `json:"event,omitempty"`
}

type companionRequest struct {
	Command        string `json:"command"`
	ConversationID string `json:"conversation_id"`
	Text           string `json:"text"`
}

type companionResponse struct {
	OK    bool   `json:"ok"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

type companionEvent struct {
	Name string `json:"name"`
	Text string `json:"text,omitempty"`
}

func companionEndpointPath(agentName string, cwd string) (string, error) {
	base, err := weclawHomeDir()
	if err != nil {
		return "", err
	}
	key := sha1.Sum([]byte(normalizeCompanionCwd(cwd)))
	fileName := sanitizeCompanionAgentName(agentName) + "-" + hex.EncodeToString(key[:]) + ".json"
	return filepath.Join(base, "companions", fileName), nil
}

func writeCompanionEndpoint(endpoint CompanionEndpoint) error {
	path, err := companionEndpointPath(endpoint.Agent, endpoint.Cwd)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create companion dir: %w", err)
	}
	data, err := json.MarshalIndent(endpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal companion endpoint: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write companion endpoint: %w", err)
	}
	return nil
}

// ReadCompanionEndpoint 读取指定 Agent 和工作目录对应的 Companion 入口。
func ReadCompanionEndpoint(agentName string, cwd string) (CompanionEndpoint, error) {
	path, err := companionEndpointPath(agentName, cwd)
	if err != nil {
		return CompanionEndpoint{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CompanionEndpoint{}, fmt.Errorf("read companion endpoint: %w", err)
	}
	var endpoint CompanionEndpoint
	if err := json.Unmarshal(data, &endpoint); err != nil {
		return CompanionEndpoint{}, fmt.Errorf("parse companion endpoint: %w", err)
	}
	return endpoint, nil
}

func removeCompanionEndpoint(agentName string, cwd string) {
	path, err := companionEndpointPath(agentName, cwd)
	if err == nil {
		_ = os.Remove(path)
	}
}

func buildCompanionToken() (string, error) {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate companion token: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}

func sanitizeCompanionAgentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "agent"
	}
	return companionAgentNamePattern.ReplaceAllString(name, "_")
}

func normalizeCompanionCwd(cwd string) string {
	if cwd == "" {
		return defaultWorkspace()
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return cwd
	}
	return abs
}

func weclawHomeDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("WECLAW_HOME")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw"), nil
}
