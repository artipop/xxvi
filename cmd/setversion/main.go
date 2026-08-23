// Command setversion writes one version number into every file of this
// repository that states one — version.go, the wails3 project config and the
// per-platform build assets generated from it.
//
// It is what `wails3 task version:set VERSION=x.y.z` runs, and it is the whole
// of cutting a release on this side: bump, commit, tag `vx.y.z`, push, and the
// release workflow does the rest.
//
// The list of files lives in internal/buildversion, shared with the test beside
// version.go that fails when they disagree.
package main

import (
	"fmt"
	"os"

	"github.com/artipop/xxvi/internal/buildversion"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: setversion <x.y.z>")
		os.Exit(2)
	}
	changed, err := buildversion.Set(".", os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "setversion: %v\n", err)
		os.Exit(1)
	}
	if len(changed) == 0 {
		fmt.Printf("уже %s\n", os.Args[1])
		return
	}
	for _, path := range changed {
		fmt.Printf("%s → %s\n", path, os.Args[1])
	}
}
