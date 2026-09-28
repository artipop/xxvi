//go:build !production

package app

// A dev build migrates and seeds the database it opens, so pointed at the
// released application's folder it would rewrite the data someone works in.
const dataDirName = "XXVI Dev"
