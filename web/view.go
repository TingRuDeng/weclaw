package web

import (
	"reflect"

	"github.com/fastclaw-ai/weclaw/config"
)

// secretMask 表示"该密钥保持不变"。读取时密钥被替换为该值，写回时该值表示沿用原密钥。
const secretMask = "__WECLAW_UNCHANGED__"

// configView 是面向前端的脱敏配置视图。
type configView struct {
	DefaultAgent          string                           `json:"default_agent"`
	APIAddr               string                           `json:"api_addr"`
	APIToken              string                           `json:"api_token"`
	SaveDir               string                           `json:"save_dir"`
	AllowedWorkspaceRoots []string                         `json:"allowed_workspace_roots"`
	AdminUsers            []string                         `json:"admin_users"`
	RateLimitPerMinute    int                              `json:"rate_limit_per_minute"`
	AuditLog              *bool                            `json:"audit_log"`
	AuditLogPath          string                           `json:"audit_log_path"`
	Progress              config.ProgressConfig            `json:"progress"`
	Agents                map[string]agentView             `json:"agents"`
	Platforms             map[string]config.PlatformConfig `json:"platforms"`
}

// agentView 是脱敏后的 agent 配置（密钥字段掩码）。
type agentView struct {
	Type             string            `json:"type"`
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	Aliases          []string          `json:"aliases,omitempty"`
	Cwd              string            `json:"cwd,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Model            string            `json:"model,omitempty"`
	Effort           string            `json:"effort,omitempty"`
	PermissionLevel  string            `json:"permission_level,omitempty"`
	ApprovalPolicy   string            `json:"approval_policy,omitempty"`
	ApprovalReviewer string            `json:"approval_reviewer,omitempty"`
	SandboxMode      string            `json:"sandbox_mode,omitempty"`
	SystemPrompt     string            `json:"system_prompt,omitempty"`
	Endpoint         string            `json:"endpoint,omitempty"`
	APIKey           string            `json:"api_key,omitempty"`
	RunAsUser        string            `json:"run_as_user,omitempty"`
	RunAsEnv         []string          `json:"run_as_env,omitempty"`
}

// redactConfig 把配置转为脱敏视图：所有密钥替换为掩码常量(非空时)，env 值掩码。
func redactConfig(cfg *config.Config) configView {
	v := configView{
		DefaultAgent:          cfg.DefaultAgent,
		APIAddr:               cfg.APIAddr,
		SaveDir:               cfg.SaveDir,
		AllowedWorkspaceRoots: cfg.AllowedWorkspaceRoots,
		AdminUsers:            cfg.AdminUsers,
		RateLimitPerMinute:    cfg.RateLimitPerMinute,
		AuditLog:              cfg.AuditLog,
		AuditLogPath:          cfg.AuditLogPath,
		Progress:              cfg.Progress,
		Agents:                make(map[string]agentView, len(cfg.Agents)),
		Platforms:             cfg.Platforms,
	}
	if cfg.APIToken != "" {
		v.APIToken = secretMask
	}
	for name, ag := range cfg.Agents {
		av := agentView{
			Type:             ag.Type,
			Command:          ag.Command,
			Args:             ag.Args,
			Aliases:          ag.Aliases,
			Cwd:              ag.Cwd,
			Model:            ag.Model,
			Effort:           ag.Effort,
			PermissionLevel:  ag.PermissionLevel,
			ApprovalPolicy:   ag.ApprovalPolicy,
			ApprovalReviewer: ag.ApprovalReviewer,
			SandboxMode:      ag.SandboxMode,
			SystemPrompt:     ag.SystemPrompt,
			Endpoint:         ag.Endpoint,
			RunAsUser:        ag.RunAsUser,
			RunAsEnv:         ag.RunAsEnv,
		}
		if ag.APIKey != "" {
			av.APIKey = secretMask
		}
		if len(ag.Env) > 0 {
			av.Env = make(map[string]string, len(ag.Env))
			for k := range ag.Env {
				av.Env[k] = secretMask // 键保留、值掩码
			}
		}
		v.Agents[name] = av
	}
	return v
}

// mergeView 把脱敏视图合并回 current：掩码值沿用 current 的密钥，非掩码值覆盖。
func mergeView(current *config.Config, v configView) *config.Config {
	merged := *current // 浅拷贝顶层标量
	merged.DefaultAgent = v.DefaultAgent
	merged.APIAddr = v.APIAddr
	merged.SaveDir = v.SaveDir
	merged.AllowedWorkspaceRoots = v.AllowedWorkspaceRoots
	merged.AdminUsers = v.AdminUsers
	merged.RateLimitPerMinute = v.RateLimitPerMinute
	merged.AuditLog = v.AuditLog
	merged.AuditLogPath = v.AuditLogPath
	merged.Progress = v.Progress
	merged.Platforms = v.Platforms

	merged.APIToken = mergeSecret(v.APIToken, current.APIToken)

	merged.Agents = make(map[string]config.AgentConfig, len(v.Agents))
	for name, av := range v.Agents {
		prev := current.Agents[name]
		ac := config.AgentConfig{
			Type:             av.Type,
			Command:          av.Command,
			Args:             av.Args,
			Aliases:          av.Aliases,
			Cwd:              av.Cwd,
			Model:            av.Model,
			Effort:           av.Effort,
			PermissionLevel:  av.PermissionLevel,
			ApprovalPolicy:   av.ApprovalPolicy,
			ApprovalReviewer: av.ApprovalReviewer,
			SandboxMode:      av.SandboxMode,
			SystemPrompt:     av.SystemPrompt,
			Endpoint:         av.Endpoint,
			RunAsUser:        av.RunAsUser,
			RunAsEnv:         av.RunAsEnv,
			Progress:         prev.Progress,
			MaxHistory:       prev.MaxHistory,
			Headers:          prev.Headers,
			AutoLaunch:       prev.AutoLaunch,
		}
		ac.APIKey = mergeSecret(av.APIKey, prev.APIKey)
		ac.Env = mergeEnv(av.Env, prev.Env)
		merged.Agents[name] = ac
	}
	return &merged
}

func mergeSecret(incoming, existing string) string {
	if incoming == secretMask {
		return existing
	}
	return incoming
}

// mergeEnv 合并 env：键沿用视图(允许增删键)，值为掩码时沿用原值。
func mergeEnv(incoming, existing map[string]string) map[string]string {
	if len(incoming) == 0 {
		return nil
	}
	result := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if v == secretMask {
			result[k] = existing[k]
		} else {
			result[k] = v
		}
	}
	return result
}

type restartConfigProjection struct {
	APIAddr      string
	APIToken     string
	SaveDir      string
	AuditLog     *bool
	AuditLogPath string
	Agents       map[string]config.AgentConfig
	Platforms    map[string]config.PlatformConfig
}

// restartRequiredConfigChanged 判断是否修改了无法热重载的运行配置。
func restartRequiredConfigChanged(current, next *config.Config) bool {
	return !reflect.DeepEqual(restartProjection(current), restartProjection(next))
}

func restartProjection(cfg *config.Config) restartConfigProjection {
	if cfg == nil {
		return restartConfigProjection{}
	}
	agents := make(map[string]config.AgentConfig, len(cfg.Agents))
	for name, agentCfg := range cfg.Agents {
		agentCfg.Progress = nil
		agents[name] = agentCfg
	}
	platforms := make(map[string]config.PlatformConfig, len(cfg.Platforms))
	for name, platformCfg := range cfg.Platforms {
		platforms[name] = restartPlatformProjection(platformCfg)
	}
	return restartConfigProjection{
		APIAddr: cfg.APIAddr, APIToken: cfg.APIToken, SaveDir: cfg.SaveDir,
		AuditLog: cfg.AuditLog, AuditLogPath: cfg.AuditLogPath,
		Agents: agents, Platforms: platforms,
	}
}

func restartPlatformProjection(platformCfg config.PlatformConfig) config.PlatformConfig {
	platformCfg.AllowedUsers = nil
	platformCfg.DefaultAgent = ""
	platformCfg.Progress = nil
	bots := make([]config.FeishuBotConfig, len(platformCfg.Bots))
	for index, bot := range platformCfg.Bots {
		bot.AllowedUsers = nil
		bot.DefaultAgent = ""
		bot.Progress = nil
		bots[index] = bot
	}
	platformCfg.Bots = bots
	return platformCfg
}
