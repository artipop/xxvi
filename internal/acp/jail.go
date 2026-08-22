package acp

import (
	"fmt"
	"path/filepath"
	"strings"
)

// A card's working folder is a boundary, and it has two sides: the agent
// reaches it through the protocol's file methods, and a person reaches the same
// files through the notes screen of the ribbon. One boundary, one piece of code
// — two would be two chances to draw it differently.

// Within resolves name inside root and refuses anything that lands outside it.
// A relative name is taken as relative to root; an absolute one has to already
// be under it.
//
// Both sides are compared as given and as resolved, because on macOS the temp
// and home trees are reached through symlinks (/var → /private/var), and a
// caller that resolved the path before asking would be refused its own folder.
func Within(root, name string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("не задана рабочая папка")
	}
	clean := filepath.Clean(name)
	if !filepath.IsAbs(clean) {
		clean = filepath.Clean(filepath.Join(root, clean))
	}
	for _, candidate := range []string{clean, resolvedPath(clean)} {
		for _, r := range []string{root, resolvedPath(root)} {
			if underRoot(candidate, r) {
				return clean, nil
			}
		}
	}
	return "", fmt.Errorf("путь %s вне рабочей папки", clean)
}

func underRoot(path, root string) bool {
	if root == "" {
		return false
	}
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// resolvedPath follows symlinks, falling back to the path as given — a path
// that cannot be resolved is not a reason to refuse everything. A file being
// created does not exist yet, so its directory is resolved instead.
func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir, base := filepath.Split(path)
	if resolved, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(resolved, base)
	}
	return path
}
