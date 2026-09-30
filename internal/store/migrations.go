package store

import "fmt"

// The schema, as a list of steps applied in order under schema_migration.
//
// A library is not needed: there are few steps and they have to stay readable.
// A step is never edited once released — the next one is appended instead —
// because an installed database has already run it.
//
// The first step is a baseline: the seventeen steps the schema was built in,
// squashed into what they came to. It stands for versions 1 to Baseline, and
// the steps after it are Baseline+1 and on. A database stopped partway
// through those seventeen is refused rather than guessed at (Store.migrate):
// the steps that would finish it are gone.
//
// Times are INTEGER unix milliseconds throughout. Sets that a person orders
// (a stage's crew, a flow's edges) carry an explicit `ord`, because "the order
// they were inserted in" is not a thing SQL promises.

// Baseline is the version the first step brings a new database to.
const Baseline = 17

func migrations(d Dialect) []string {
	return []string{
		// 1–17. The baseline.
		fmt.Sprintf(`
-- name_key is the name folded for comparison, computed in Go rather than by
-- lower(): SQLite's own lower() is ASCII-only, so «Über» and
-- «über» would be two different sources. Every case-insensitive lookup
-- and every uniqueness rule in this schema goes through such a column.

-- ---- registries: sources with their rules, agents, projects ----

CREATE TABLE source (
	name             TEXT PRIMARY KEY,
	name_key         TEXT    NOT NULL,
	plugin           TEXT    NOT NULL DEFAULT '',
	enabled          INTEGER NOT NULL DEFAULT 1,
	noisy            INTEGER NOT NULL DEFAULT 0,
	update_mode      TEXT    NOT NULL DEFAULT 'update',
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
CREATE UNIQUE INDEX idx_agent_key ON agent(name_key);

-- Where the work happens. A card points at one by id, so renaming a project
-- drags nothing behind it. Its hosting — which remote, which server, which
-- kind — is here; the token is not: it lives in the system keychain, by server.
CREATE TABLE project (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	name_key   TEXT NOT NULL UNIQUE,
	kind       TEXT NOT NULL,
	path       TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	remote     TEXT NOT NULL DEFAULT '',
	server     TEXT NOT NULL DEFAULT '',
	provider   TEXT NOT NULL DEFAULT ''
);

-- ---- flows ----
--
-- Laid out relationally rather than as a JSON blob: "where are this flow's
-- cards" and "which stages use this agent" are queries, and they should be
-- queries.

CREATE TABLE flow (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	name_key    TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	entry_stage TEXT NOT NULL DEFAULT '',
	updated_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_flow_key ON flow(name_key);

-- What a stage leaves on the card, what it is handed on the way in and what it
-- puts in front of the person standing at it are JSON in a column rather than
-- more tables: none is ever queried across flows — they are read with the
-- stage and written with it — and a table would be a join for something that
-- is part of the stage's own text.
--
-- work is where an agent stage runs: in the card's terminal or as a session
-- (docs/system.md §4.1.1).
CREATE TABLE stage (
	id           TEXT PRIMARY KEY,
	flow_id      TEXT    NOT NULL REFERENCES flow(id) ON DELETE CASCADE,
	ord          INTEGER NOT NULL DEFAULT 0,
	name         TEXT    NOT NULL,
	action       TEXT    NOT NULL DEFAULT 'none',
	prompt       TEXT    NOT NULL DEFAULT '',
	max_running  INTEGER NOT NULL DEFAULT 0,
	final        INTEGER NOT NULL DEFAULT 0,
	x            REAL    NOT NULL DEFAULT 0,
	y            REAL    NOT NULL DEFAULT 0,
	writes_json  TEXT    NOT NULL DEFAULT '[]',
	reads_json   TEXT    NOT NULL DEFAULT '[]',
	screens_json TEXT    NOT NULL DEFAULT '[]',
	work         TEXT    NOT NULL DEFAULT ''
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
	id            TEXT PRIMARY KEY,
	flow_id       TEXT    NOT NULL REFERENCES flow(id) ON DELETE CASCADE,
	ord           INTEGER NOT NULL DEFAULT 0,
	from_stage    TEXT    NOT NULL,
	to_stage      TEXT    NOT NULL,
	on_trigger    TEXT    NOT NULL,
	cond_property TEXT    NOT NULL DEFAULT '',
	cond_value    TEXT    NOT NULL DEFAULT '',
	cond_comment  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_edge_flow ON edge(flow_id);
CREATE INDEX idx_edge_from ON edge(from_stage, on_trigger);

-- ---- cards, their properties and their journal ----

-- project is the registry entry the card works in; empty is a real answer: a
-- task starting from a blank page gets a folder of its own.
--
-- work_mode is how the card works in its project when that is a repository —
-- in the folder as it stands, in a separate working tree, or on a branch in
-- the folder itself — and branch, base_ref and worktree what that came to. On
-- the card, because the card is what owns the work. keep_worktree remembers
-- «keep» as an answer to removing a closed card's tree, or the question would
-- come back every time the list is read.
--
-- session is a conversation the card was started from, by the id its agent
-- gave it.
CREATE TABLE card (
	id            TEXT PRIMARY KEY,
	source        TEXT    NOT NULL DEFAULT '',
	external_id   TEXT    NOT NULL DEFAULT '',
	item_version  TEXT    NOT NULL DEFAULT '',
	title         TEXT    NOT NULL,
	body          TEXT    NOT NULL DEFAULT '',
	url           TEXT    NOT NULL DEFAULT '',
	state         TEXT    NOT NULL,
	assignee      TEXT    NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL,
	project       TEXT    NOT NULL DEFAULT '',
	work_mode     TEXT    NOT NULL DEFAULT '',
	branch        TEXT    NOT NULL DEFAULT '',
	base_ref      TEXT    NOT NULL DEFAULT '',
	worktree      TEXT    NOT NULL DEFAULT '',
	keep_worktree INTEGER NOT NULL DEFAULT 0,
	session       TEXT    NOT NULL DEFAULT ''
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

-- The card's journal. An entry says what it is and where it belongs: the ribbon
-- shows a step's report and the reason a card stands on their own segment.
-- Time cannot place them — a report is written in the same millisecond as the
-- transition after it — so the entry carries the run it was written for and the
-- transition the card stood in. What the application itself writes is a code
-- (msg) the UI words in the person's language, beside the text an agent or a
-- person wrote.
CREATE TABLE card_comment (
	id         %[1]s,
	card_id    TEXT    NOT NULL REFERENCES card(id) ON DELETE CASCADE,
	author     TEXT    NOT NULL DEFAULT '',
	text       TEXT    NOT NULL,
	created_at INTEGER NOT NULL,
	kind       TEXT    NOT NULL DEFAULT '',
	session_id TEXT    NOT NULL DEFAULT '',
	event_id   INTEGER NOT NULL DEFAULT 0,
	msg        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_card_comment_card ON card_comment(card_id, id);

-- ---- where a card stands, where it has been, and why it moved ----

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
	id         %[1]s,
	card_id    TEXT NOT NULL REFERENCES card(id) ON DELETE CASCADE,
	flow_id    TEXT NOT NULL,
	from_stage TEXT NOT NULL DEFAULT '',
	to_stage   TEXT NOT NULL,
	on_trigger TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX idx_flow_event_card ON flow_event(card_id, id);

-- ---- the running half: sessions, their stream, the stage queue, and the keys
-- that make one event move a card once ----

-- work is recorded on the run as well as on the stage, and not because it
-- could be read off the stage: the ribbon is a journal, and a stage whose mode
-- was changed afterwards must not make last week's step look like something it
-- never was.
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
	error_text     TEXT NOT NULL DEFAULT '',
	work           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_session_card ON agent_session(card_id);
CREATE INDEX idx_session_status ON agent_session(status);

CREATE TABLE session_event (
	id         %[1]s,
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

		// 18. Whether the card was typed into the ribbon, whose body is then
		// the person's words and nothing more. Every card before this was
		// sent to its agent with its title, so none of them is.
		`
ALTER TABLE card ADD COLUMN typed INTEGER NOT NULL DEFAULT 0;`,

		// 19. Stage templates: the kinds of node the flow editor offers, as a
		// registry a person edits, and the one a stage was made from. The
		// builtin ones are written here rather than by the first-run seed, so
		// a database that already exists gets them too — once: deleting one
		// is an answer, and a startup rule putting it back would overrule it.
		// An empty name on a builtin is the application's own words
		// (model.StageTemplate); model.BuiltinTemplates is the same list, and
		// a test holds the two together.
		`
ALTER TABLE stage ADD COLUMN template TEXT NOT NULL DEFAULT '';

CREATE TABLE stage_template (
	id           TEXT PRIMARY KEY,
	ord          INTEGER NOT NULL DEFAULT 0,
	name         TEXT    NOT NULL DEFAULT '',
	description  TEXT    NOT NULL DEFAULT '',
	icon         TEXT    NOT NULL DEFAULT '',
	color        TEXT    NOT NULL DEFAULT '',
	builtin      INTEGER NOT NULL DEFAULT 0,
	action       TEXT    NOT NULL DEFAULT 'none',
	work         TEXT    NOT NULL DEFAULT '',
	prompt       TEXT    NOT NULL DEFAULT '',
	crew_json    TEXT    NOT NULL DEFAULT '[]',
	final        INTEGER NOT NULL DEFAULT 0,
	screens_json TEXT    NOT NULL DEFAULT '[]',
	updated_at   INTEGER NOT NULL DEFAULT 0
);

INSERT INTO stage_template (id, ord, icon, color, builtin, action, work, final, screens_json) VALUES
	('agent',    1,  'agent',    '#4fb8ad', 1, 'agent',   'terminal', 0, '[]'),
	('terminal', 2,  'terminal', '#c9cdd6', 1, 'none',    '', 0, '[{"kind":"terminal"}]'),
	('docker',   3,  'docker',   '#2496ed', 1, 'none',    '', 0, '[{"kind":"terminal","ref":"docker compose up"}]'),
	('web',      4,  'web',      '#e0a458', 1, 'none',    '', 0, '[{"kind":"run","ref":"web"}]'),
	('diff',     5,  'diff',     '#c38ee0', 1, 'none',    '', 0, '[{"kind":"diff"}]'),
	('notes',    6,  'notes',    '#d9c36a', 1, 'none',    '', 0, '[{"kind":"notes","ref":"plan.md"}]'),
	('wait',     7,  'wait',     '#949aab', 1, 'none',    '', 0, '[]'),
	('publish',  8,  'publish',  '#6aa2e8', 1, 'publish', '', 0, '[]'),
	('verdict',  9,  'verdict',  '#6aa2e8', 1, 'verdict', '', 0, '[]'),
	('final',    10, 'final',    '#7bc47f', 1, 'none',    '', 1, '[]');`,

		// 20. A project's folders: a front and a back are one project in two
		// places. Every project so far had one, and it becomes the first under
		// the project's own id, so a task that named no folder — all of them
		// until now — keeps working where it did. project.path stays, written
		// as the first folder, for the rows that read it.
		`
CREATE TABLE project_folder (
	id         TEXT PRIMARY KEY,
	project_id TEXT    NOT NULL REFERENCES project(id) ON DELETE CASCADE,
	ord        INTEGER NOT NULL,
	name       TEXT    NOT NULL DEFAULT '',
	path       TEXT    NOT NULL
);
CREATE INDEX idx_project_folder_project ON project_folder(project_id, ord);
INSERT INTO project_folder (id, project_id, ord, name, path) SELECT id, id, 0, '', path FROM project;

ALTER TABLE card ADD COLUMN folder TEXT NOT NULL DEFAULT '';`,

		// A session run's conversation outlives its process only when the
		// agent said at initialize that it can open one again; a terminal
		// run's CLI always can, so the column matters for sessions alone.
		`ALTER TABLE agent_session ADD COLUMN revivable INTEGER NOT NULL DEFAULT 0;`,

		// A stage is a place where an agent works, and which agent is the
		// task's to say, so a stage no longer has a crew of its own.
		`DROP TABLE stage_agent;
ALTER TABLE stage_template DROP COLUMN crew_json;`,
	}
}
