package auththrottle

import (
	"fmt"
	"testing"
	"time"
)

func TestThrottleBoundsTrackedSources(t *testing.T) {
	now := time.Unix(100, 0)
	throttle := NewWithClock(func() time.Time { return now })
	for index := 0; index < DefaultMaxSources+25; index++ {
		now = now.Add(time.Nanosecond)
		throttle.Fail(fmt.Sprintf("2001:db8::%x", index))
	}
	if got := len(throttle.lastSeen); got != DefaultMaxSources {
		t.Fatalf("tracked sources=%d, want %d", got, DefaultMaxSources)
	}
}
