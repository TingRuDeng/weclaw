package securefile

import (
	"context"
	"fmt"
)

// WithExclusiveLock runs fn while holding a process-shared advisory lock.
// The lock file is persistent: replacing or removing it would let contenders
// lock different inodes and enter the critical section together.
func WithExclusiveLock(ctx context.Context, path string, fn func() error) error {
	if ctx == nil {
		return fmt.Errorf("acquire secure lock: nil context")
	}
	if fn == nil {
		return fmt.Errorf("acquire secure lock: nil callback")
	}
	lock, err := acquireExclusiveLock(ctx, path)
	if err != nil {
		return err
	}
	defer lock.release()
	return fn()
}
