//go:build !darwin

package app

// systemLanguages is the locale the process was started in. On Windows that is
// usually nothing, and the UI's webview, which follows the system there, is
// asked instead.
func systemLanguages() []string { return envLanguages() }
