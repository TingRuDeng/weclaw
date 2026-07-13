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
// are preferred. Claude 远程能力只允许 ACP，不提供 CLI 回退。
var agentCandidates = []agentCandidate{
	// claude: ACP-only，原生 CLI 只作为 local_command 使用
	{Name: "claude", Binary: "claude-agent-acp", Type: "acp", Model: "sonnet"},
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

// DetectAndConfigure 自动检测本地 Agent，并统一执行特殊迁移与默认项选择。
func DetectAndConfigure(cfg *Config) bool {
	modified := detectConfiguredAgents(cfg)
	modified = normalizeDetectedOpenclaw(cfg) || modified
	modified = detectOpenclawHTTPFallback(cfg) || modified
	modified = selectDetectedDefault(cfg) || modified
	return NormalizeCodexRemoteFirst(cfg) || modified
}

// detectConfiguredAgents 按候选优先级填充尚未配置的 Agent。
func detectConfiguredAgents(cfg *Config) bool {
	modified := false
	for _, candidate := range agentCandidates {
		if _, exists := cfg.Agents[candidate.Name]; exists {
			continue
		}
		path, err := detectLookPath(candidate.Binary)
		if err != nil {
			continue
		}
		if len(candidate.CheckArgs) > 0 && !detectCommandProbe(path, candidate.CheckArgs) {
			log.Printf("[config] skipping %s at %s (type=%s): probe failed (%v)", candidate.Name, path, candidate.Type, candidate.CheckArgs)
			continue
		}
		log.Printf("[config] auto-detected %s at %s (type=%s)", candidate.Name, path, candidate.Type)
		cfg.Agents[candidate.Name] = AgentConfig{
			Type:         candidate.Type,
			Command:      path,
			LocalCommand: detectedLocalCommand(candidate.Name),
			Args:         candidate.Args,
			Model:        candidate.Model,
		}
		modified = true
	}
	return modified
}

// normalizeDetectedOpenclaw 将自动检测到的 OpenClaw 优先配置为 HTTP，并保留显式 ACP 入口。
func normalizeDetectedOpenclaw(cfg *Config) bool {
	agentCfg, exists := cfg.Agents["openclaw"]
	if !exists || agentCfg.Type != "acp" || len(agentCfg.Args) != 0 {
		return false
	}
	gwURL, token, password := loadOpenclawGateway()
	if gwURL == "" {
		log.Printf("[config] openclaw binary found but no gateway config, skipping")
		delete(cfg.Agents, "openclaw")
		return true
	}
	setOpenclawHTTP(cfg, gwURL, token)
	if _, exists := cfg.Agents["openclaw-acp"]; !exists {
		cfg.Agents["openclaw-acp"] = openclawACPConfig(openclawACPConfigRequest{
			command: agentCfg.Command, gatewayURL: gwURL, token: token, password: password,
		})
		log.Printf("[config] openclaw ACP also available as 'openclaw-acp' (use /openclaw-acp to switch)")
	}
	return true
}

// detectOpenclawHTTPFallback 在未检测到二进制时从网关配置补充 HTTP Agent。
func detectOpenclawHTTPFallback(cfg *Config) bool {
	if _, exists := cfg.Agents["openclaw"]; exists {
		return false
	}
	gwURL, token, _ := loadOpenclawGateway()
	if gwURL == "" {
		return false
	}
	setOpenclawHTTP(cfg, gwURL, token)
	return true
}

// setOpenclawHTTP 统一生成 OpenClaw HTTP 配置，避免两条检测路径产生差异。
func setOpenclawHTTP(cfg *Config, gatewayURL string, token string) {
	httpURL := strings.Replace(gatewayURL, "wss://", "https://", 1)
	httpURL = strings.Replace(httpURL, "ws://", "http://", 1)
	endpoint := strings.TrimRight(httpURL, "/") + "/v1/chat/completions"
	log.Printf("[config] openclaw using HTTP mode: %s", endpoint)
	cfg.Agents["openclaw"] = AgentConfig{
		Type: "http", Endpoint: endpoint, APIKey: token,
		Headers: map[string]string{"x-openclaw-scopes": "operator.write"}, Model: "openclaw:main",
	}
}

// selectDetectedDefault 选择优先级最高且已检测到的默认 Agent。
func selectDetectedDefault(cfg *Config) bool {
	if cfg.DefaultAgent != "" && agentExists(cfg, cfg.DefaultAgent) {
		return false
	}
	for _, name := range defaultOrder {
		if _, exists := cfg.Agents[name]; !exists {
			continue
		}
		changed := cfg.DefaultAgent != name
		if changed {
			log.Printf("[config] setting default agent: %s", name)
			cfg.DefaultAgent = name
		}
		return changed
	}
	return false
}

type openclawACPConfigRequest struct {
	command    string
	gatewayURL string
	token      string
	password   string
}

// openclawACPConfig 构造显式使用网关的 OpenClaw ACP 配置。
func openclawACPConfig(req openclawACPConfigRequest) AgentConfig {
	env := make(map[string]string)
	if req.token != "" {
		env["OPENCLAW_GATEWAY_TOKEN"] = req.token
	} else if req.password != "" {
		env["OPENCLAW_GATEWAY_PASSWORD"] = req.password
	}
	return AgentConfig{
		Type: "acp", Command: req.command, Args: []string{"acp", "--url", req.gatewayURL},
		Env: env, Model: "openclaw:main",
	}
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
	return exec.CommandContext(ctx, shell, "-lic", `command -v -- "$1"`, "_", binary)
}

// LookPath 导出二进制解析能力，供 doctor 等命令复用（含 login shell 回退）。
func LookPath(binary string) (string, error) {
	return lookPath(binary)
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
