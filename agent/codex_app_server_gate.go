package agent

import (
	"context"
	"sync"
)

type codexAppServerGateState string

const (
	codexAppServerRunning    codexAppServerGateState = "running"
	codexAppServerDraining   codexAppServerGateState = "draining"
	codexAppServerRestarting codexAppServerGateState = "restarting"
	codexAppServerFailed     codexAppServerGateState = "failed"
)

type codexAppServerGate struct {
	mu           sync.Mutex
	state        codexAppServerGateState
	generationID uint64
	activeTurns  int
	changed      chan struct{}
}

type codexAppServerPermit struct {
	gate       *codexAppServerGate
	generation uint64
	once       sync.Once
}

func newCodexAppServerGate() *codexAppServerGate {
	return &codexAppServerGate{
		state: codexAppServerRunning, generationID: 1, changed: make(chan struct{}),
	}
}

// acquire 等待刷新结束后登记 turn，draining 期间不会放入新的工作。
func (g *codexAppServerGate) acquire(ctx context.Context) (*codexAppServerPermit, error) {
	for {
		g.mu.Lock()
		if g.state == codexAppServerRunning {
			g.activeTurns++
			permit := &codexAppServerPermit{gate: g, generation: g.generationID}
			g.mu.Unlock()
			return permit, nil
		}
		if g.state == codexAppServerFailed {
			g.mu.Unlock()
			return nil, ErrCodexRuntimeUnavailable
		}
		changed := g.changed
		g.mu.Unlock()
		if err := waitCodexGateChange(ctx, changed); err != nil {
			return nil, err
		}
	}
}

// beginExclusive 为账号切换提供非等待式独占门禁。已有 turn 或另一项维护操作时
// 立即拒绝，避免账号命令看似成功却中断正在执行的任务。
func (g *codexAppServerGate) beginExclusive() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == codexAppServerFailed {
		return ErrCodexRuntimeUnavailable
	}
	if g.state != codexAppServerRunning || g.activeTurns != 0 {
		return ErrCodexWriterBusy
	}
	g.state = codexAppServerDraining
	g.notifyLocked()
	return nil
}

// finishExclusive 收敛账号切换。committed 只在新账号已验证时增加 generation；
// available=false 用于回滚也失败的情形，后续 turn 必须 fail-closed。
func (g *codexAppServerGate) finishExclusive(committed bool, available bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !available {
		g.state = codexAppServerFailed
		g.notifyLocked()
		return
	}
	if committed {
		g.generationID++
	}
	g.state = codexAppServerRunning
	g.notifyLocked()
}

// drain 阻止新 turn，等待已有 turn 结束，再执行一次共享运行时刷新。
func (g *codexAppServerGate) drain(ctx context.Context, restart func(context.Context) error) error {
	if err := g.beginDrain(ctx); err != nil {
		return err
	}
	if err := g.waitUntilIdle(ctx); err != nil {
		g.abortDrain()
		return err
	}
	g.markRestarting()
	err := restart(ctx)
	g.finishRestart(err == nil)
	return err
}

func (g *codexAppServerGate) beginDrain(ctx context.Context) error {
	for {
		g.mu.Lock()
		if g.state == codexAppServerRunning {
			g.state = codexAppServerDraining
			g.notifyLocked()
			g.mu.Unlock()
			return nil
		}
		if g.state == codexAppServerFailed {
			g.mu.Unlock()
			return ErrCodexRuntimeUnavailable
		}
		changed := g.changed
		g.mu.Unlock()
		if err := waitCodexGateChange(ctx, changed); err != nil {
			return err
		}
	}
}

func (g *codexAppServerGate) waitUntilIdle(ctx context.Context) error {
	for {
		g.mu.Lock()
		if g.activeTurns == 0 {
			g.mu.Unlock()
			return nil
		}
		changed := g.changed
		g.mu.Unlock()
		if err := waitCodexGateChange(ctx, changed); err != nil {
			return err
		}
	}
}

func (g *codexAppServerGate) markRestarting() {
	g.mu.Lock()
	g.state = codexAppServerRestarting
	g.notifyLocked()
	g.mu.Unlock()
}

// abortDrain 只用于尚未触碰运行时的等待失败；原 Host 仍然可写。
func (g *codexAppServerGate) abortDrain() {
	g.mu.Lock()
	g.state = codexAppServerRunning
	g.notifyLocked()
	g.mu.Unlock()
}

// finishRestart 在已经进入运行时重启后收敛 gate。失败意味着 Host 状态不再
// 可证明，必须禁止后续 turn，不能退回 running 伪装可用。
func (g *codexAppServerGate) finishRestart(restarted bool) {
	g.mu.Lock()
	if restarted {
		g.generationID++
		g.state = codexAppServerRunning
	} else {
		g.state = codexAppServerFailed
	}
	g.notifyLocked()
	g.mu.Unlock()
}

func (g *codexAppServerGate) notifyLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

func (p *codexAppServerPermit) release() {
	if p == nil || p.gate == nil {
		return
	}
	p.once.Do(func() {
		p.gate.mu.Lock()
		p.gate.activeTurns--
		p.gate.notifyLocked()
		p.gate.mu.Unlock()
	})
}

func (g *codexAppServerGate) generation() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.generationID
}

func (g *codexAppServerGate) stateSnapshot() codexAppServerGateState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

func waitCodexGateChange(ctx context.Context, changed <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	}
}
