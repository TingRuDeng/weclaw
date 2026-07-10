package messaging

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

type reentryDetectingReplier struct {
	inFlight  atomic.Int32
	reentered atomic.Bool
}

func (r *reentryDetectingReplier) call() error {
	if r.inFlight.Add(1) != 1 {
		r.reentered.Store(true)
	}
	time.Sleep(time.Millisecond)
	r.inFlight.Add(-1)
	return nil
}

func (r *reentryDetectingReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
}
func (r *reentryDetectingReplier) SendText(context.Context, string) error  { return r.call() }
func (r *reentryDetectingReplier) SendImage(context.Context, string) error { return r.call() }
func (r *reentryDetectingReplier) SendFile(context.Context, string) error  { return r.call() }
func (r *reentryDetectingReplier) Typing(context.Context, bool) error      { return r.call() }
func (r *reentryDetectingReplier) OpenStream(context.Context, platform.StreamOptions) (platform.Stream, error) {
	return nil, platform.ErrUnsupported
}
func (r *reentryDetectingReplier) AskChoices(context.Context, string, []platform.Choice) error {
	return r.call()
}

func TestSerializedReplierPreventsConcurrentCalls(t *testing.T) {
	inner := &reentryDetectingReplier{}
	reply := newSerializedReplier(inner)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reply.SendText(context.Background(), "hello")
		}()
	}
	wg.Wait()
	if inner.reentered.Load() {
		t.Fatal("serialized replier allowed concurrent calls")
	}
}
