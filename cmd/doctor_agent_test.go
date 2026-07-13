package cmd

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

type fakeDoctorACPClient struct {
	started  int
	stopped  int
	startErr error
}

func (f *fakeDoctorACPClient) Start(context.Context) error {
	f.started++
	return f.startErr
}

// TestProbeClaudeACPStopsAfterStartFailure 验证握手失败也不会遗留子进程。
func TestProbeClaudeACPStopsAfterStartFailure(t *testing.T) {
	fake := &fakeDoctorACPClient{startErr: context.DeadlineExceeded}
	err := probeClaudeACP(context.Background(), "claude", config.AgentConfig{}, func(string, config.AgentConfig) doctorACPClient {
		return fake
	})
	if err == nil || fake.started != 1 || fake.stopped != 1 {
		t.Fatalf("error=%v started=%d stopped=%d", err, fake.started, fake.stopped)
	}
}

func TestProbeClaudeACPRejectsNilClient(t *testing.T) {
	err := probeClaudeACP(context.Background(), "claude", config.AgentConfig{}, func(string, config.AgentConfig) doctorACPClient {
		return nil
	})
	if err == nil {
		t.Fatal("nil ACP client must fail")
	}
}

func (f *fakeDoctorACPClient) Stop() {
	f.stopped++
}

// TestProbeClaudeACPStartsAndStopsHandshake 验证诊断只握手并立即释放进程。
func TestProbeClaudeACPStartsAndStopsHandshake(t *testing.T) {
	fake := &fakeDoctorACPClient{}
	err := probeClaudeACP(context.Background(), "claude", config.AgentConfig{Type: "acp"}, func(string, config.AgentConfig) doctorACPClient {
		return fake
	})
	if err != nil {
		t.Fatalf("probeClaudeACP error: %v", err)
	}
	if fake.started != 1 || fake.stopped != 1 {
		t.Fatalf("started=%d stopped=%d, want 1/1", fake.started, fake.stopped)
	}
}

// TestDoctorReportsClaudeACPProbeFailure 验证能力握手失败属于阻断问题。
func TestDoctorReportsClaudeACPProbeFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "acp", Command: "claude-agent-acp"}
	deps := testDoctorDeps()
	deps.claudeACPProbe = func(context.Context, string, config.AgentConfig) error {
		return context.DeadlineExceeded
	}

	result, ok := findResult(runDoctorChecks(cfg, deps), `agent "claude" ACP capabilities`)
	if !ok || result.Status != doctorFail {
		t.Fatalf("result=%+v found=%v, want blocking capability failure", result, ok)
	}
}

// TestDoctorReportsLegacyClaudeMigration 验证 Doctor 对旧后端给出迁移入口。
func TestDoctorReportsLegacyClaudeMigration(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "cli", Command: "claude"}

	result, ok := findResult(runDoctorChecks(cfg, testDoctorDeps()), `agent "claude" ACP capabilities`)
	if !ok || result.Status != doctorFail || result.Detail == "" {
		t.Fatalf("result=%+v found=%v, want legacy migration failure", result, ok)
	}
}
