//go:build unix

package securefile

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExclusiveLockHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WithExclusiveLock(ctx, path, func() error {
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
		defer waitCancel()
		err := WithExclusiveLock(waitCtx, path, func() error {
			return errors.New("second callback must not run")
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("second lock error=%v, want deadline exceeded", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExclusiveLockSerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("WECLAW_SECURE_LOCK_WAITER") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := WithExclusiveLock(ctx, os.Getenv("WECLAW_SECURE_LOCK_PATH"), func() error {
			return errors.New("waiter callback must not run")
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiter lock error=%v, want deadline exceeded", err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "state.lock")
	if err := WithExclusiveLock(context.Background(), path, func() error {
		command := exec.Command(os.Args[0], "-test.run=^TestExclusiveLockSerializesAcrossProcesses$")
		command.Env = append(os.Environ(),
			"WECLAW_SECURE_LOCK_WAITER=1",
			"WECLAW_SECURE_LOCK_PATH="+path,
		)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("cross-process waiter: %w: %s", err, output)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExclusiveLockReleasedWhenProcessExits(t *testing.T) {
	if os.Getenv("WECLAW_SECURE_LOCK_HOLDER") == "1" {
		err := WithExclusiveLock(context.Background(), os.Getenv("WECLAW_SECURE_LOCK_PATH"), func() error {
			fmt.Println("LOCKED")
			time.Sleep(30 * time.Second)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "state.lock")
	command := exec.Command(os.Args[0], "-test.run=^TestExclusiveLockReleasedWhenProcessExits$")
	command.Env = append(os.Environ(),
		"WECLAW_SECURE_LOCK_HOLDER=1",
		"WECLAW_SECURE_LOCK_PATH="+path,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "LOCKED" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("holder did not report lock acquisition: %v", scanner.Err())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WithExclusiveLock(ctx, path, func() error { return nil }); err != nil {
		t.Fatalf("lock after holder exit: %v", err)
	}
}

func TestExclusiveLockRejectsSymlinkAndTightensMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.lock")
	target := filepath.Join(dir, "target.lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := WithExclusiveLock(context.Background(), path, func() error { return nil }); err == nil {
		t.Fatal("symlink lock should be rejected")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WithExclusiveLock(context.Background(), path, func() error { return nil }); err != nil {
		t.Fatalf("tighten lock mode: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode=%o, want 600", info.Mode().Perm())
	}
}
