package cmd

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	daemonLogMaxBytes = 20 << 20
	daemonLogBackups  = 3
)

type daemonLogWriter struct {
	mu      sync.Mutex
	path    string
	max     int64
	backups int
	rebind  func(*os.File) error
	file    *os.File
	size    int64
}

func newDaemonLogWriter(path string, max int64, backups int, rebind func(*os.File) error) (*daemonLogWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon log dir: %w", err)
	}
	w := &daemonLogWriter{path: path, max: max, backups: backups, rebind: rebind}
	if err := w.openLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *daemonLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.max > 0 && w.size+int64(len(data)) > w.max {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	return n, err
}

func (w *daemonLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *daemonLogWriter) rotateLocked() error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	if err := rotateDaemonLogBackups(w.path, w.backups); err != nil {
		return err
	}
	if err := w.openLocked(); err != nil {
		return err
	}
	if w.rebind != nil {
		return w.rebind(w.file)
	}
	return nil
}

func (w *daemonLogWriter) openLocked() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat daemon log: %w", err)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func rotateDaemonLogBackups(path string, backups int) error {
	if backups <= 0 {
		return os.Remove(path)
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", path, backups))
	for index := backups - 1; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", path, index)
		to := fmt.Sprintf("%s.%d", path, index+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate daemon log backup: %w", err)
		}
	}
	if err := os.Rename(path, path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate daemon log: %w", err)
	}
	return nil
}

func configureDaemonLogging() (io.Closer, error) {
	if os.Getenv(daemonChildEnv) != "1" {
		return nil, nil
	}
	w, err := newDaemonLogWriter(logFile(), daemonLogMaxBytes, daemonLogBackups, rebindDaemonStandardFiles)
	if err != nil {
		return nil, err
	}
	log.SetOutput(w)
	return w, nil
}
