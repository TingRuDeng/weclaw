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
		changed := g.changed
		g.mu.Unlock()
		if err := waitCodexGateChange(ctx, changed); err != nil {
			return nil, err
		}
	}
}

// drain 阻止新 turn，等待已有 turn 结束，再执行一次共享运行时刷新。
func (g *codexAppServerGate) drain(ctx context.Context, restart func(context.Context) error) error {
	if err := g.beginDrain(ctx); err != nil {
		return err
	}
	if err := g.waitUntilIdle(ctx); err != nil {
		g.finishDrain(false)
		return err
	}
	g.markRestarting()
	err := restart(ctx)
	g.finishDrain(err == nil)
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

func (g *codexAppServerGate) finishDrain(restarted bool) {
	g.mu.Lock()
	if restarted {
		g.generationID++
	}
	g.state = codexAppServerRunning
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
