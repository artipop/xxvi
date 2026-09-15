package store

import "fmt"

// The schema, as a list of steps applied in order under schema_migration.
//
// A library is not needed: there are few steps and they have to stay readable.
// A step is never edited once released — the next one is appended instead —
// because an installed database has already run it.
//
// Times are INTEGER unix milliseconds throughout. Sets that a person orders
// (a stage's crew, a flow's edges) carry an explicit `ord`, because "the order
// they were inserted in" is not a thing SQL promises.
func migrations(d Dialect) []string {
	return []string{
		// 1. Registries: sources with their rules, and agents.
		`
-- name_key is the name folded for comparison, computed in Go rather than by
-- lower(): SQLite's own lower() is ASCII-only, so «Разработка» and
-- «разработка» would be two different sources. Every case-insensitive lookup
-- and every uniqueness rule in this schema goes through such a column.
CREATE TABLE source (
	name             TEXT PRIMARY KEY,
	name_key         TEXT    NOT NULL,
	plugin           TEXT    NOT NULL DEFAULT '',
	enabled          INTEGER NOT NULL DEFAULT 1,
	noisy            INTEGER NOT NULL DEFAULT 0,
	update_mode      TEXT    NOT NULL DEFAULT 'comment',
	config           TEXT    NOT NULL DEFAULT '{}',
	interval_seconds INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_source_key ON source(name_key);

CREATE TABLE source_rule (
	source       TEXT    NOT NULL REFERENCES source(name) ON DELETE CASCADE,
	ord          INTEGER NOT NULL,
	name         TEXT    NOT NULL DEFAULT '',
	then_action  TEXT    NOT NULL,
	match_json   TEXT    NOT NULL DEFAULT '{}',
	props_json   TEXT    NOT NULL DEFAULT '{}',
	suggest_flow TEXT    NOT NULL DEFAULT '',
	assignee     TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (source, ord)
);

CREATE TABLE agent (
	name             TEXT PRIMARY KEY,
	name_key         TEXT NOT NULL,
	kind             TEXT NOT NULL,
	bin_path         TEXT NOT NULL DEFAULT '',
	model            TEXT NOT NULL DEFAULT '',
	prompt           TEXT NOT NULL DEFAULT '',
	env_json         TEXT NOT NULL DEFAULT '{}',
	args_json        TEXT NOT NULL DEFAULT '[]',
	command_json     TEXT NOT NULL DEFAULT '[]',
	auto_allow_json  TEXT NOT NULL DEFAULT '[]',
	created_at       INTEGER NOT NULL
);
-- The agent's key is model.Username, not a plain fold: a card whose assignee
-- reads "my-agent" and a registry entry named "My Agent" are one agent, and
-- that has to hold in the index as well as in the code.
CREATE UNIQUE INDEX idx_agent_key ON agent(name_key);`,

		// 2. Flows, laid out relationally rather than as a JSON blob: "where are
		// this flow's cards" and "which stages use this agent" are queries, and
		// they should be queries.
		`
CREATE TABLE flow (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	name_key    TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	entry_stage TEXT NOT NULL DEFAULT '',
	updated_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_flow_key ON flow(name_key);

CREATE TABLE stage (
	id          TEXT PRIMARY KEY,
	flow_id     TEXT    NOT NULL REFERENCES flow(id) ON DELETE CASCADE,
	ord         INTEGER NOT NULL DEFAULT 0,
	name        TEXT    NOT NULL,
	action      TEXT    NOT NULL DEFAULT 'none',
	prompt      TEXT    NOT NULL DEFAULT '',
	max_running INTEGER NOT NULL DEFAULT 0,
	final       INTEGER NOT NULL DEFAULT 0,
	x           REAL    NOT NULL DEFAULT 0,
	y           REAL    NOT NULL DEFAULT 0
);
CREATE INDEX idx_stage_flow ON stage(flow_id);

CREATE TABLE stage_agent (
	stage_id   TEXT    NOT NULL REFERENCES stage(id) ON DELETE CASCADE,
	ord        INTEGER NOT NULL,
	agent_name TEXT    NOT NULL,
	-- The folded name beside the written one, so "which stages use this agent"
	-- is a query rather than a scan: a crew is stored in the registry's own
	-- spelling, but the question is asked in whatever spelling the caller has.
	agent_key  TEXT    NOT NULL,
	PRIMARY KEY (stage_id, ord)
);
CREATE INDEX idx_stage_agent_key ON stage_agent(agent_key);

CREATE TABLE edge (
	id           TEXT PRIMARY KEY,
	flow_id      TEXT    NOT NULL REFERENCES flow(id) ON DELETE CASCADE,
	ord          INTEGER NOT NULL DEFAULT 0,
	from_stage   TEXT    NOT NULL,
	to_stage     TEXT    NOT NULL,
	on_trigger   TEXT    NOT NULL,
	cond_property TEXT   NOT NULL DEFAULT '',
	cond_value    TEXT   NOT NULL DEFAULT '',
	cond_comment  TEXT   NOT NULL DEFAULT ''
);
CREATE INDEX idx_edge_flow ON edge(flow_id);
CREATE INDEX idx_edge_from ON edge(from_stage, on_trigger);`,

		// 3. Cards, their properties and their history.
		fmt.Sprintf(`
CREATE TABLE card (
	id           TEXT PRIMARY KEY,
	source       TEXT NOT NULL DEFAULT '',
	external_id  TEXT NOT NULL DEFAULT '',
	item_version TEXT NOT NULL DEFAULT '',
	title        TEXT NOT NULL,
	body         TEXT NOT NULL DEFAULT '',
	url          TEXT NOT NULL DEFAULT '',
	state        TEXT NOT NULL,
	assignee     TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL
);
CREATE INDEX idx_card_state ON card(state);
CREATE INDEX idx_card_source ON card(source);
-- One item of one source is one card. Partial, so cards a person typed — which
-- carry no external id — are not all "the same item".
CREATE UNIQUE INDEX idx_card_item ON card(source, external_id) WHERE external_id <> '';

CREATE TABLE card_prop (
	card_id TEXT NOT NULL REFERENCES card(id) ON DELETE CASCADE,
	name    TEXT NOT NULL,
	value   TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (card_id, name)
);

CREATE TABLE card_comment (
	id         %s,
	card_id    TEXT NOT NULL REFERENCES card(id) ON DELETE CASCADE,
	author     TEXT NOT NULL DEFAULT '',
	text       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX idx_card_comment_card ON card_comment(card_id, id);`, d.AutoIncrementPK()),

		// 4. Where a card stands, where it has been, and why it moved.
		fmt.Sprintf(`
CREATE TABLE card_flow (
	card_id    TEXT PRIMARY KEY REFERENCES card(id) ON DELETE CASCADE,
	flow_id    TEXT NOT NULL,
	stage_id   TEXT NOT NULL,
	entered_at INTEGER NOT NULL
);
CREATE INDEX idx_card_flow_stage ON card_flow(stage_id);
CREATE INDEX idx_card_flow_flow ON card_flow(flow_id);

CREATE TABLE card_flow_visit (
	card_id  TEXT NOT NULL REFERENCES card(id) ON DELETE CASCADE,
	stage_id TEXT NOT NULL,
	PRIMARY KEY (card_id, stage_id)
);

CREATE TABLE flow_event (
	id         %s,
	card_id    TEXT NOT NULL REFERENCES card(id) ON DELETE CASCADE,
	flow_id    TEXT NOT NULL,
	from_stage TEXT NOT NULL DEFAULT '',
	to_stage   TEXT NOT NULL,
	on_trigger TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX idx_flow_event_card ON flow_event(card_id, id);`, d.AutoIncrementPK()),

		// 5. The running half: sessions, their stream, the stage queue, and the
		// keys that make one event move a card once.
		fmt.Sprintf(`
CREATE TABLE agent_session (
	id             TEXT PRIMARY KEY,
	card_id        TEXT NOT NULL,
	flow_id        TEXT NOT NULL DEFAULT '',
	stage_id       TEXT NOT NULL DEFAULT '',
	agent_name     TEXT NOT NULL DEFAULT '',
	agent_kind     TEXT NOT NULL DEFAULT '',
	acp_session_id TEXT NOT NULL DEFAULT '',
	status         TEXT NOT NULL,
	cwd            TEXT NOT NULL DEFAULT '',
	started_at     INTEGER NOT NULL,
	finished_at    INTEGER,
	error_text     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_session_card ON agent_session(card_id);
CREATE INDEX idx_session_status ON agent_session(status);

CREATE TABLE session_event (
	id         %s,
	session_id TEXT    NOT NULL REFERENCES agent_session(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL,
	kind       TEXT    NOT NULL,
	payload    TEXT    NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_session_event_seq ON session_event(session_id, seq);

CREATE TABLE stage_queue (
	card_id   TEXT PRIMARY KEY REFERENCES card(id) ON DELETE CASCADE,
	flow_id   TEXT    NOT NULL,
	stage_id  TEXT    NOT NULL,
	queued_at INTEGER NOT NULL
);
CREATE INDEX idx_stage_queue_stage ON stage_queue(stage_id, queued_at);

CREATE TABLE idempotency (
	key        TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL
);

CREATE TABLE source_item (
	source      TEXT NOT NULL,
	external_id TEXT NOT NULL,
	version     TEXT NOT NULL DEFAULT '',
	card_id     TEXT NOT NULL DEFAULT '',
	seen_at     INTEGER NOT NULL,
	PRIMARY KEY (source, external_id)
);

CREATE TABLE setting (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);`, d.AutoIncrementPK()),

		// 6. What a stage declares it leaves on the card and what it is handed
		// on the way in. JSON in a column rather than two more tables: neither
		// is ever queried across flows — they are read with the stage and
		// written with it — and a table would be a join for something that is
		// part of the stage's own text.
		`
ALTER TABLE stage ADD COLUMN writes_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE stage ADD COLUMN reads_json  TEXT NOT NULL DEFAULT '[]';`,

		// 7. What a stage puts in front of the person standing at it. Same seam
		// as 6 and for the same reason: screens are read with the stage and
		// written with it, and nothing ever asks across flows which stage shows
		// which screen.
		`
ALTER TABLE stage ADD COLUMN screens_json TEXT NOT NULL DEFAULT '[]';`,

		// 8. Where the work happens. A registry like the agents and the sources,
		// and a card points at one by id — so renaming a project drags nothing
		// behind it. Empty on the card is a real answer: a task starting from a
		// blank page gets a folder of its own.
		`
CREATE TABLE IF NOT EXISTS project (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	name_key   TEXT NOT NULL UNIQUE,
	kind       TEXT NOT NULL,
	path       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
ALTER TABLE card ADD COLUMN project TEXT NOT NULL DEFAULT '';`,

		// 9. Where an agent stage runs: in the card's terminal or as a session
		// (docs/system.md §4.1.1). The same word is recorded on the run as well,
		// and not because it could be read off the stage: the ribbon is a
		// journal, and a stage whose mode was changed afterwards must not make
		// last week's step look like something it never was.
		`
ALTER TABLE stage ADD COLUMN work TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_session ADD COLUMN work TEXT NOT NULL DEFAULT '';`,
	}
}
