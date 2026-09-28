//go:build !windows

package ptyhold

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// socketDir is under /tmp rather than the data folder: a unix socket's path is
// limited to about a hundred bytes on macOS, and a data folder under
// ~/Library/Application Support/ with a home folder of any length is past it.
// The folder is the user's alone, because whoever can connect to the holder
// can run anything as this user.
func socketDir() (string, error) {
	dir := filepath.Join("/tmp", fmt.Sprintf("xxvi-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Getuid() || info.Mode().Perm() != 0o700 {
		return "", fmt.Errorf("%s is not a private folder of this user", dir)
	}
	return dir, nil
}
