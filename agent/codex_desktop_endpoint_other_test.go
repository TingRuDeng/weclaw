//go:build !darwin

package agent

import (
	"context"
	"errors"
	"testing"
)

func TestCodexDesktopEndpointUnavailableOffDarwin(t *testing.T) {
	_, err := dialCodexDesktopEndpoint(context.Background())
	if !errors.Is(err, ErrCodexDesktopUnavailable) {
		t.Fatalf("dialCodexDesktopEndpoint() error = %v, want unavailable", err)
	}
}
