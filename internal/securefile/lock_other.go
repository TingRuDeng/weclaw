//go:build !unix

package securefile

import (
	"context"
	"fmt"
	"runtime"
)

type exclusiveLock struct{}

func acquireExclusiveLock(context.Context, string) (*exclusiveLock, error) {
	return nil, fmt.Errorf("secure cross-process locks are unsupported on %s", runtime.GOOS)
}

func (*exclusiveLock) release() {}
