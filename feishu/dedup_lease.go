package feishu

import (
	"context"
	"time"
)

const minimumFeishuDedupLeaseInterval = time.Millisecond

type feishuDedupLease struct {
	cancel context.CancelFunc
	done   <-chan bool
}

type feishuDedupLeaseRuntime struct {
	cancel   context.CancelFunc
	interval time.Duration
	done     chan<- bool
}

// renew 仅刷新当前 owner 仍有效的处理权，过期或换主后拒绝续租。
func (r feishuDedupReservation) renew() bool {
	if r.deduper == nil || r.owner == nil {
		return true
	}
	now := r.deduper.now()
	r.deduper.mu.Lock()
	defer r.deduper.mu.Unlock()
	for _, key := range r.keys {
		claim, ok := r.deduper.processing[key]
		if !ok || claim.owner != r.owner || now.Sub(claim.at) > r.deduper.ttl {
			return false
		}
	}
	for _, key := range r.keys {
		claim := r.deduper.processing[key]
		claim.at = now
		r.deduper.processing[key] = claim
	}
	return true
}

// startLease 在耗时处理期间续租；失去所有权时取消派生 context。
func (r feishuDedupReservation) startLease(ctx context.Context) (context.Context, feishuDedupLease) {
	leaseCtx, cancel := context.WithCancel(ctx)
	done := make(chan bool, 1)
	interval := r.deduper.ttl / 3
	if interval < minimumFeishuDedupLeaseInterval {
		interval = minimumFeishuDedupLeaseInterval
	}
	go r.runLease(leaseCtx, feishuDedupLeaseRuntime{
		cancel: cancel, interval: interval, done: done,
	})
	return leaseCtx, feishuDedupLease{cancel: cancel, done: done}
}

func (r feishuDedupReservation) runLease(ctx context.Context, runtime feishuDedupLeaseRuntime) {
	ticker := time.NewTicker(runtime.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			runtime.done <- true
			return
		case <-ticker.C:
			if !r.renew() {
				runtime.cancel()
				runtime.done <- false
				return
			}
		}
	}
}

// stop 停止续租并等待 goroutine 退出，返回结束时是否仍持有处理权。
func (l feishuDedupLease) stop() bool {
	l.cancel()
	return <-l.done
}
