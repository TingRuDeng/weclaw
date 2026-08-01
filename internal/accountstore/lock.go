package accountstore

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

const writeLockFilename = ".write.lock"

var processWriteSlot = make(chan struct{}, 1)

// WithWriteLock serializes account-directory read-modify-write transactions
// across WeClaw processes that share the same data directory.
func WithWriteLock(ctx context.Context, dir string, fn func() error) error {
	if ctx == nil {
		return fmt.Errorf("lock account store: nil context")
	}
	if fn == nil {
		return fmt.Errorf("lock account store: nil callback")
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return fmt.Errorf("lock account store: directory must be absolute")
	}
	select {
	case processWriteSlot <- struct{}{}:
		defer func() { <-processWriteSlot }()
	case <-ctx.Done():
		return fmt.Errorf("lock account store: %w", ctx.Err())
	}
	if err := securefile.EnsureDir(dir); err != nil {
		return fmt.Errorf("prepare account store: %w", err)
	}
	if err := securefile.WithExclusiveLock(ctx, filepath.Join(dir, writeLockFilename), fn); err != nil {
		return fmt.Errorf("lock account store: %w", err)
	}
	return nil
}
