//go:build windows

package nativeterm

import "errors"

// Attach has nothing to bridge on Windows: no native terminal runs it there.
func Attach(string) error { return errors.New("native terminals are macOS only") }
