package store

import "strings"

// Everything that differs between SQL dialects lives here, and nowhere else.
//
// Only SQLite is implemented — see docs/poc.md. The seam exists because adding
// Postgres later should mean writing a second implementation of this interface
// rather than editing queries across the store, and because that seam is cheap
// today: queries are written with `?` and sqlx.Rebind translates them to the
// driver's own style, so placeholders — the difference that would otherwise be
// on every line — cost nothing.
type Dialect interface {
	// Name is the driver name to open, and what the store reports about itself.
	Name() string
	// DSN builds a connection string for a database at path.
	DSN(path string) string
	// AutoIncrementPK is the primary key clause for a table whose rows are only
	// ever appended and read back in order: the journal, flow events.
	AutoIncrementPK() string
	// Setup is what has to run on every fresh connection before anything else.
	Setup() []string
	// Migrations are the schema steps, in order: the baseline, standing for
	// versions 1 to Baseline, then one step per version after it.
	Migrations() []string
}

// sqliteDialect is the only implementation. Times are stored as INTEGER unix
// milliseconds — a representation both dialects read the same way, which is
// what keeps the seam from leaking into every scan.
type sqliteDialect struct{}

func (sqliteDialect) Name() string { return "sqlite" }

func (sqliteDialect) DSN(path string) string {
	// WAL so a read never waits behind the writer, and a busy timeout so a
	// concurrent write waits instead of failing: the flow engine and the UI
	// write from different goroutines all the time.
	return path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

func (sqliteDialect) AutoIncrementPK() string { return "INTEGER PRIMARY KEY AUTOINCREMENT" }

func (sqliteDialect) Setup() []string {
	// Repeated per connection: the pool opens more than one, and pragmas are
	// per connection rather than per database.
	return []string{"PRAGMA foreign_keys = ON"}
}

func (d sqliteDialect) Migrations() []string {
	return migrations(d)
}

// nameKey folds a name for comparison and uniqueness. It is computed here, in
// Go, rather than by the database's own lower(): SQLite's is ASCII-only, so
// «Über» and «über» would be two different flows. Go's ToLower is
// Unicode-aware, and doing it on this side also means the rule is the same
// whatever dialect is underneath.
func nameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
