package web

import (
	"net/http"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/auththrottle"
)

const (
	authMaxFailures = auththrottle.DefaultMaxFailures
	authWindow      = auththrottle.DefaultWindow
	authBlockFor    = auththrottle.DefaultBlockFor
)

type authThrottle = auththrottle.Throttle

func newAuthThrottle() *authThrottle {
	return auththrottle.New()
}

func newAuthThrottleWithClock(now func() time.Time) *authThrottle {
	return auththrottle.NewWithClock(now)
}

// clientKey 取请求来源 IP 作为限速键。
func clientKey(r *http.Request) string {
	return auththrottle.ClientKey(r)
}
