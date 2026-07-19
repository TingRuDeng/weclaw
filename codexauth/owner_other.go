//go:build !unix

package codexauth

import "os"

func validateCurrentOwner(os.FileInfo) error { return nil }
