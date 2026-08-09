//go:build !unix

package agent

import "os"

func validateCodexProviderOwner(os.FileInfo) error { return nil }
