package agent

import (
	"context"
	"errors"
	"sync"
)

var ErrCodexObserverDetached = errors.New("Codex turn observer detached")

type codexObserverDetachKey struct{}

type codexObserverDetachSignal struct {
	once sync.Once
	done chan struct{}
}

// ContextWithCodexObserverDetach 为共享 turn 增加“仅停止当前观察者”的控制信号。
// 它与 context 取消不同：触发后不得调用 turn/interrupt。
func ContextWithCodexObserverDetach(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	signal := &codexObserverDetachSignal{done: make(chan struct{})}
	return context.WithValue(ctx, codexObserverDetachKey{}, signal), func() {
		signal.once.Do(func() { close(signal.done) })
	}
}

func codexObserverDetachFromContext(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	signal, _ := ctx.Value(codexObserverDetachKey{}).(*codexObserverDetachSignal)
	if signal == nil {
		return nil
	}
	return signal.done
}
