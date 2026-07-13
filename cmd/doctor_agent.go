package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

const doctorACPProbeTimeout = 35 * time.Second

type doctorACPClient interface {
	Start(context.Context) error
	Stop()
}

type doctorACPFactory func(string, config.AgentConfig) doctorACPClient

type doctorACPProbeRequest struct {
	ctx      context.Context
	name     string
	agentCfg config.AgentConfig
	factory  doctorACPFactory
}

// probeClaudeACP 只执行 initialize 能力握手，始终释放子进程且不创建 session。
func probeClaudeACP(req doctorACPProbeRequest) error {
	client := req.factory(req.name, req.agentCfg)
	if client == nil {
		return fmt.Errorf("ACP 探针创建失败")
	}
	defer client.Stop()
	probeCtx, cancel := context.WithTimeout(req.ctx, doctorACPProbeTimeout)
	defer cancel()
	return client.Start(probeCtx)
}

// defaultClaudeACPProbe 使用正式 Agent 工厂验证 adapter 的 initialize 能力契约。
func defaultClaudeACPProbe(ctx context.Context, name string, agentCfg config.AgentConfig) error {
	return probeClaudeACP(doctorACPProbeRequest{
		ctx: ctx, name: name, agentCfg: agentCfg,
		factory: func(agentName string, cfg config.AgentConfig) doctorACPClient {
			return newACPAgentFromConfig(agentName, cfg)
		},
	})
}

// checkClaudeACP 将旧后端或能力握手失败转换为 Doctor 阻断结果。
func checkClaudeACP(name string, agentCfg config.AgentConfig, deps doctorDeps) doctorResult {
	result := doctorResult{Name: fmt.Sprintf("agent %q ACP capabilities", name)}
	if agentCfg.Type != "acp" {
		result.Status = doctorFail
		result.Detail = "Claude 远程后端仅支持 ACP；请运行 weclaw config agent"
		return result
	}
	if deps.claudeACPProbe == nil {
		result.Status = doctorFail
		result.Detail = "ACP capability probe unavailable"
		return result
	}
	if err := deps.claudeACPProbe(context.Background(), name, agentCfg); err != nil {
		result.Status = doctorFail
		result.Detail = err.Error()
		return result
	}
	result.Status = doctorOK
	result.Detail = "session list/resume verified"
	return result
}
