package ptyhold

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// SocketPath is where the holder of one data folder listens. Named by the
// folder, so a development build and a release — which keep separate data —
// keep separate holders too.
func SocketPath(dataDir string) (string, error) {
	dir, err := socketDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(dir, "xxvi-"+hex.EncodeToString(sum[:4])+".sock"), nil
}
