//go:build windows

package ptyhold

import "os"

// socketDir is the user's own temporary folder, which is private to them.
func socketDir() (string, error) { return os.TempDir(), nil }
