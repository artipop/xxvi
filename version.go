package main

// appVersion is what this build calls itself, and the string the updater
// compares against what a release says (updates.go). It is a constant rather
// than an -ldflags injection because every Taskfile bakes its own
// `-ldflags="-w -s"` into a BUILD_FLAGS template string, and threading a value
// through four of them makes the version a property of how the binary was
// built — which is exactly what `wails3 dev` then gets wrong.
//
// The platform build assets carry the same number, and version_test.go is what
// keeps them in step; `wails3 task version:set VERSION=x.y.z` writes all of
// them at once. No leading "v": that is the tag's business, and the release
// workflow strips it on its side.
const appVersion = "0.1.0"
