//go:build !unix

package antigravity

// socketLock is a no-op fallback. Unix sockets are not available on this
// platform, so the sidecar cannot serve a live socket here.
type socketLock struct{}

func acquireSocketLock(string) (*socketLock, error) {
	return &socketLock{}, nil
}

func (l *socketLock) Close() error { return nil }
