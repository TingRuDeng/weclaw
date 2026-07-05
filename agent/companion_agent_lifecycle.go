package agent

import "context"

func (a *CompanionAgent) ResetSession(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (a *CompanionAgent) Info() AgentInfo {
	return AgentInfo{Name: a.name, Type: "companion", Model: a.model, Command: a.command}
}

func (a *CompanionAgent) SetCwd(cwd string) {
	a.mu.Lock()
	a.cwd = normalizeCompanionCwd(cwd)
	a.mu.Unlock()
}

func (a *CompanionAgent) Cwd() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cwd
}

func (a *CompanionAgent) Stop() {
	a.mu.Lock()
	listener := a.listener
	conn := a.conn
	a.listener = nil
	a.conn = nil
	a.encoder = nil
	cwd := a.cwd
	name := a.name
	a.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	removeCompanionEndpoint(name, cwd)
	a.failPending("Companion 已停止")
}

var _ Agent = (*CompanionAgent)(nil)
