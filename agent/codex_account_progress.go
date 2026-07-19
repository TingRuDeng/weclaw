package agent

import "context"

type CodexAccountSwitchPhase string

const (
	CodexAccountSwitchChecking  CodexAccountSwitchPhase = "checking"
	CodexAccountSwitchSwitching CodexAccountSwitchPhase = "switching"
	CodexAccountSwitchVerifying CodexAccountSwitchPhase = "verifying"
	CodexAccountSwitchRollback  CodexAccountSwitchPhase = "rollback"
)

type codexAccountSwitchProgressKey struct{}

// WithCodexAccountSwitchProgress 为 UI 注入非阻塞进度观察器；账号事务不依赖其成功。
func WithCodexAccountSwitchProgress(ctx context.Context, report func(CodexAccountSwitchPhase)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, codexAccountSwitchProgressKey{}, report)
}

func reportCodexAccountSwitchProgress(ctx context.Context, phase CodexAccountSwitchPhase) {
	if ctx == nil {
		return
	}
	report, _ := ctx.Value(codexAccountSwitchProgressKey{}).(func(CodexAccountSwitchPhase))
	if report != nil {
		report(phase)
	}
}
