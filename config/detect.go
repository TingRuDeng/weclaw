package config

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// agentCandidate defines one way to run an agent.
// Multiple candidates can map to the same agent name; the first detected wins.
type agentCandidate struct {
	Name      string   // agent name (e.g. "claude", "codex")
	Binary    string   // binary to look up in PATH
	Args      []string // extra args (e.g. ["acp"] for cursor)
	CheckArgs []string // optional capability probe args (must exit 0)
	Type      string   // "acp", "cli"
	Model     string   // default model
}

// agentCandidates is ordered by priority: for each agent name, earlier entries
// are preferred. E.g. claude ACP is tried before claude CLI.
var agentCandidates = []agentCandidate{
	// claude: prefer ACP, fallback to CLI
	{Name: "claude", Binary: "claude-agent-acp", Type: "acp", Model: "sonnet"},
	{Name: "claude", Binary: "claude", Type: "cli", Model: "sonnet"},
	// codex: prefer ACP, fallback to CLI
	{Name: "codex", Binary: "codex-acp", Type: "acp", Model: ""},
	{Name: "codex", Binary: "codex", Args: []string{"app-server", "--listen", "stdio://"}, CheckArgs: []string{"app-server", "--help"}, Type: "acp", Model: ""},
	{Name: "codex", Binary: "codex", Type: "cli", Model: ""},
	// ACP-only agents
	{Name: "cursor", Binary: "agent", Args: []string{"acp"}, Type: "acp", Model: ""},
	{Name: "kimi", Binary: "kimi", Args: []string{"acp"}, Type: "acp", Model: ""},
	{Name: "gemini", Binary: "gemini", Args: []string{"--acp"}, Type: "acp", Model: ""},
	{Name: "opencode", Binary: "opencode", Type: "companion", Model: ""},
	{Name: "openclaw", Binary: "openclaw", Type: "acp", Model: "openclaw:main"}, // args built dynamically
	{Name: "pi", Binary: "pi-acp", Type: "acp", Model: ""},
	{Name: "copilot", Binary: "copilot", Args: []string{"--acp", "--stdio"}, Type: "acp", Model: ""},
	{Name: "droid", Binary: "droid", Args: []string{"exec", "--output-format", "acp"}, Type: "acp", Model: ""},
	{Name: "iflow", Binary: "iflow", Args: []string{"--experimental-acp"}, Type: "acp", Model: ""},
	{Name: "kiro", Binary: "kiro-cli", Args: []string{"acp"}, Type: "acp", Model: ""},
	{Name: "qwen", Binary: "qwen", Args: []string{"--acp"}, Type: "acp", Model: ""},
}

// defaultOrder defines the priority for choosing the default agent.
// Lower index = higher priority.
var defaultOrder = []string{
	"claude", "codex", "cursor", "kimi", "gemini", "opencode", "openclaw",
	"pi", "copilot", "droid", "iflow", "kiro", "qwen",
}

var (
	detectLookPath     = lookPath
	detectCommandProbe = commandProbe
)

// DetectAndConfigure auto-detects local agents and populates the config.
// For each agent name, it picks the highest-priority candidate (acp > cli).
// Returns true if the config was modified.
func DetectAndConfigure(cfg *Config) bool {
	modified := false

	for _, candidate := range agentCandidates {
		// Skip if this agent name is already configured
		if _, exists := cfg.Agents[candidate.Name]; exists {
			continue
		}

		path, err := detectLookPath(candidate.Binary)
		if err != nil {
			continue
		}

		// Run capability probe if specified
		if len(candidate.CheckArgs) > 0 && !detectCommandProbe(path, candidate.CheckArgs) {
			log.Printf("[config] skipping %s at %s (type=%s): probe failed (%v)", candidate.Name, path, candidate.Type, candidate.CheckArgs)
			continue
		}

		log.Printf("[config] auto-detected %s at %s (type=%s)", candidate.Name, path, candidate.Type)
		cfg.Agents[candidate.Name] = AgentConfig{
			Type:    candidate.Type,
			Command: path,
			Args:    candidate.Args,
			Model:   candidate.Model,
		}
		modified = true
	}

	// Special handling for openclaw: prefer HTTP mode over ACP to avoid
	// session routing conflicts with openclaw-weixin plugin (see #9).
	// Priority: HTTP (gateway) > ACP (with user-configured --session) > skip.
	if agCfg, exists := cfg.Agents["openclaw"]; exists && agCfg.Type == "acp" && len(agCfg.Args) == 0 {
		gwURL, gwToken, gwPassword := loadOpenclawGateway()
		if gwURL != "" {
			// Prefer HTTP mode — no session routing issues
			httpURL := gwURL
			httpURL = strings.Replace(httpURL, "wss://", "https://", 1)
			httpURL = strings.Replace(httpURL, "ws://", "http://", 1)
			endpoint := strings.TrimRight(httpURL, "/") + "/v1/chat/completions"
			log.Printf("[config] openclaw using HTTP mode: %s", endpoint)
			cfg.Agents["openclaw"] = AgentConfig{
				Type:     "http",
				Endpoint: endpoint,
				APIKey:   gwToken,
				Headers:  map[string]string{"x-openclaw-scopes": "operator.write"},
				Model:    "openclaw:main",
			}
			modified = true

			// Also register openclaw-acp as a separate agent for users who want ACP
			if _, apcExists := cfg.Agents["openclaw-acp"]; !apcExists {
				args := []string{"acp", "--url", gwURL}
				if gwToken != "" {
					args = append(args, "--token", gwToken)
				} else if gwPassword != "" {
					args = append(args, "--password", gwPassword)
				}
				cfg.Agents["openclaw-acp"] = AgentConfig{
					Type:    "acp",
					Command: agCfg.Command,
					Args:    args,
					Model:   "openclaw:main",
				}
				log.Printf("[config] openclaw ACP also available as 'openclaw-acp' (use /openclaw-acp to switch)")
			}
		} else {
			log.Printf("[config] openclaw binary found but no gateway config, skipping")
			delete(cfg.Agents, "openclaw")
			modified = true
		}
	}

	// Fallback: if openclaw still not configured, try HTTP via gateway config.
	if _, exists := cfg.Agents["openclaw"]; !exists {
		gwURL, gwToken, _ := loadOpenclawGateway()
		if gwURL != "" {
			httpURL := gwURL
			httpURL = strings.Replace(httpURL, "wss://", "https://", 1)
			httpURL = strings.Replace(httpURL, "ws://", "http://", 1)
			endpoint := strings.TrimRight(httpURL, "/") + "/v1/chat/completions"
			log.Printf("[config] using openclaw HTTP: %s", endpoint)
			cfg.Agents["openclaw"] = AgentConfig{
				Type:     "http",
				Endpoint: endpoint,
				APIKey:   gwToken,
				Headers:  map[string]string{"x-openclaw-scopes": "operator.write"},
				Model:    "openclaw:main",
			}
			modified = true
		}
	}

	// Pick the highest-priority default agent.
	if cfg.DefaultAgent == "" || !agentExists(cfg, cfg.DefaultAgent) {
		for _, name := range defaultOrder {
			if _, ok := cfg.Agents[name]; ok {
				if cfg.DefaultAgent != name {
					log.Printf("[config] setting default agent: %s", name)
					cfg.DefaultAgent = name
					modified = true
				}
				break
			}
		}
	}

	if NormalizeCodexRemoteFirst(cfg) {
		modified = true
	}

	return modified
}

// NormalizeCodexRemoteFirst 将旧的 Codex Companion 默认配置迁移到 app-server remote-first。
func NormalizeCodexRemoteFirst(cfg *Config) bool {
	if cfg == nil || cfg.Agents == nil {
		return false
	}
	agCfg, ok := cfg.Agents["codex"]
	if !ok || agCfg.Type != "companion" || agCfg.Command == "" {
		return false
	}
	if agCfg.AutoLaunch != nil && *agCfg.AutoLaunch {
		return false
	}
	agCfg.Type = "acp"
	agCfg.Args = codexRemoteFirstArgs(agCfg.Args)
	agCfg.AutoLaunch = nil
	cfg.Agents["codex"] = agCfg
	log.Printf("[config] migrated codex companion to remote-first app-server")
	return true
}

func codexRemoteFirstArgs(args []string) []string {
	cleaned := stripCodexAppServerArgs(args)
	return append(cleaned, "app-server", "--listen", "stdio://")
}

func stripCodexAppServerArgs(args []string) []string {
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != "app-server" {
			cleaned = append(cleaned, args[i])
			continue
		}
		i = skipCodexListenArgs(args, i+1)
	}
	return cleaned
}

func skipCodexListenArgs(args []string, start int) int {
	i := start
	for i < len(args) {
		if args[i] == "--listen" && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(args[i], "--listen=") {
			i++
			continue
		}
		break
	}
	return i - 1
}

// commandProbe runs a binary with args and returns true if it exits 0.
func commandProbe(binary string, args []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func agentExists(cfg *Config, name string) bool {
	_, ok := cfg.Agents[name]
	return ok
}

// loginShellLookupTimeout 限制登录 shell 兜底探测耗时，避免 shell rc 卡住自动探测。
var loginShellLookupTimeout = 8 * time.Second

// loginShellWhichCommand 构造登录 shell 的 which 探测命令，测试中可替换为卡住的命令。
var loginShellWhichCommand = func(ctx context.Context, shell, binary string) *exec.Cmd {
	return exec.CommandContext(ctx, shell, "-lic", "which "+binary)
}

// lookPath finds a binary by name. It first tries exec.LookPath (fast, uses
// current PATH). If that fails, it falls back to resolving via a login shell
// which sources the user's profile (~/.zshrc, ~/.bashrc) — this picks up
// binaries installed through version managers like nvm, mise, etc. that only
// add their paths in interactive shells.
func lookPath(binary string) (string, error) {
	// Fast path: binary is in current PATH
	if p, err := exec.LookPath(binary); err == nil {
		return p, nil
	}

	// 通过登录交互 shell 兜底解析，并用超时避免 shell rc 卡住自动探测和 weclaw start。
	shell := "zsh"
	if runtime.GOOS != "darwin" {
		shell = "bash"
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginShellLookupTimeout)
	defer cancel()
	out, err := loginShellWhichCommand(ctx, shell, binary).Output()
	if err != nil {
		return "", fmt.Errorf("not found: %s", binary)
	}
	p := strings.TrimSpace(string(out))
	if p == "" || strings.Contains(p, "not found") {
		return "", fmt.Errorf("not found: %s", binary)
	}
	log.Printf("[config] resolved %s via login shell: %s", binary, p)
	return p, nil
}
