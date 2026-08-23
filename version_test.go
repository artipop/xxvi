package main

import (
	"testing"

	"github.com/artipop/xxvi/internal/buildversion"
)

// The version a build calls itself has to be the version its packaging says,
// because the updater compares the first against a release built from the
// second: a binary that reports 0.1.0 while the release is tagged from a
// build/config.yml saying 0.2.0 offers every user an update to the version they
// are already running, for ever.
func TestEveryBuildAssetStatesTheRunningVersion(t *testing.T) {
	found, err := buildversion.Read(".")
	if err != nil {
		t.Fatalf("читаем сборочные файлы: %v", err)
	}
	for path, versions := range found {
		for _, version := range versions {
			if version != appVersion {
				t.Errorf("%s говорит %q, version.go говорит %q — запустите `wails3 task version:set VERSION=%s`",
					path, version, appVersion, appVersion)
			}
		}
	}
}
