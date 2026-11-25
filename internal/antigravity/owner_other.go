//go:build !unix

package antigravity

import (
	"fmt"
	"os"
)

func validateSocketOwner(os.FileInfo) error {
	return fmt.Errorf("%w: unsupported platform", ErrUnsafeSocket)
}

func transcriptFileID(info os.FileInfo) string {
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}
