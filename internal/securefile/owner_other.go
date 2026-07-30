//go:build !unix

package securefile

import "os"

func validateCurrentOwner(os.FileInfo) error { return nil }
