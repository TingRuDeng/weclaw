package agent

type acpModelConfig struct {
	model       string
	effort      string
	serviceTier string
}

// modelConfigSnapshot 原子读取模型配置，调用方不得在 RPC 期间持有 Agent 锁。
func (a *ACPAgent) modelConfigSnapshot() acpModelConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return acpModelConfig{model: a.model, effort: a.effort, serviceTier: a.serviceTier}
}
