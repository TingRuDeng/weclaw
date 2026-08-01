package accountstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWithWriteLockHonorsContextWhileProcessBusy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")
	if err := WithWriteLock(context.Background(), dir, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
		defer cancel()
		err := WithWriteLock(ctx, dir, func() error {
			return errors.New("second callback must not run")
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second lock error=%v, want deadline exceeded", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
