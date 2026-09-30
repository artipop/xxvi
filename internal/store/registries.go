package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// The two registries a person edits: agents and sources. They are rows like
// everything else — the same queries work on them, and there is no second place
// to look for what this machine is configured with.

// ---- agents ----

type agentRow struct {
	Name          string `db:"name"`
	NameKey       string `db:"name_key"`
	Kind          string `db:"kind"`
	BinPath       string `db:"bin_path"`
	Model         string `db:"model"`
	Prompt        string `db:"prompt"`
	EnvJSON       string `db:"env_json"`
	ArgsJSON      string `db:"args_json"`
	CommandJSON   string `db:"command_json"`
	AutoAllowJSON string `db:"auto_allow_json"`
	Proxy         string `db:"proxy"`
	CreatedAt     int64  `db:"created_at"`
}

func (r agentRow) agent() model.Agent {
	a := model.Agent{Name: r.Name, Kind: r.Kind, BinPath: r.BinPath, Model: r.Model, Prompt: r.Prompt, Proxy: r.Proxy}
	decodeJSON(r.EnvJSON, &a.Env)
	decodeJSON(r.ArgsJSON, &a.Args)
	decodeJSON(r.CommandJSON, &a.Command)
	decodeJSON(r.AutoAllowJSON, &a.AutoAllowTools)
	return a
}

// Agents returns the registry, by name.
func (s *Store) Agents() ([]model.Agent, error) {
	var rows []agentRow
	if err := s.db.Select(&rows, `SELECT * FROM agent ORDER BY name_key`); err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	proxies, err := s.proxyIndex()
	if err != nil {
		return nil, err
	}
	out := make([]model.Agent, 0, len(rows))
	for _, r := range rows {
		a := r.agent()
		a.Network = proxies.of(a)
		out = append(out, a)
	}
	return out, nil
}

// Agent returns one registry entry, matched the way names are matched
// everywhere here.
func (s *Store) Agent(name string) (model.Agent, error) {
	var r agentRow
	err := s.db.Get(&r, `SELECT * FROM agent WHERE name_key = ?`, model.Username(name))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Agent{}, msg.Tag(ErrNotFound, "agent.notFound", "agent", name)
	}
	if err != nil {
		return model.Agent{}, err
	}
	a := r.agent()
	if a.Proxy != "" {
		p, err := s.Proxy(a.Proxy)
		if err != nil {
			return model.Agent{}, err
		}
		a.Network = &p
	}
	return a, nil
}

// SaveAgent adds or replaces a registry entry, keyed by name.
func (s *Store) SaveAgent(a model.Agent) (model.Agent, error) {
	a, err := validateAgent(a)
	if err != nil {
		return model.Agent{}, err
	}
	if a.Proxy != "" {
		// Checked against the kind too: an edit cannot leave an agent with a
		// configuration its CLI cannot use.
		p, err := s.Proxy(a.Proxy)
		if err != nil {
			return model.Agent{}, err
		}
		if _, err := p.Validate(a.Kind); err != nil {
			return model.Agent{}, msg.Wrap(err, "proxy.unusable", "agent", a.Name, "kind", a.Kind)
		}
		a.Proxy = p.Name
	}
	_, err = s.db.Exec(`
		INSERT INTO agent (name, name_key, kind, bin_path, model, prompt, env_json, args_json, command_json, auto_allow_json, proxy, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			kind = excluded.kind, bin_path = excluded.bin_path, model = excluded.model,
			prompt = excluded.prompt, env_json = excluded.env_json, args_json = excluded.args_json,
			command_json = excluded.command_json, auto_allow_json = excluded.auto_allow_json,
			proxy = excluded.proxy`,
		a.Name, model.Username(a.Name), a.Kind, a.BinPath, a.Model, a.Prompt,
		encodeJSON(a.Env), encodeJSON(a.Args), encodeJSON(a.Command), encodeJSON(a.AutoAllowTools), a.Proxy,
		millis(time.Now()))
	if err != nil {
		return model.Agent{}, fmt.Errorf("save agent: %w", err)
	}
	return a, nil
}

// DeleteAgent removes an entry. A stage that named it keeps the name: the flow
// says so when it is next saved, which is a better moment to find out than
// mid-run.
func (s *Store) DeleteAgent(name string) error {
	res, err := s.db.Exec(`DELETE FROM agent WHERE name_key = ?`, model.Username(name))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return msg.Tag(ErrNotFound, "agent.notFound", "agent", name)
	}
	return nil
}

func validateAgent(a model.Agent) (model.Agent, error) {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return model.Agent{}, msg.Err("agent.noName")
	}
	a.Kind = strings.TrimSpace(strings.ToLower(a.Kind))
	a.BinPath = strings.TrimSpace(a.BinPath)
	a.Model = strings.TrimSpace(a.Model)
	a.Command = trimAll(a.Command)
	a.Args = trimAll(a.Args)
	a.AutoAllowTools = trimAll(a.AutoAllowTools)

	switch a.Kind {
	case model.KindACP:
		// The generic kind carries its own agent: there is nothing for us to
		// look up, so an empty command is an agent that cannot start.
		if len(a.Command) == 0 {
			return model.Agent{}, msg.Err("agent.noCommand", "kind", model.KindACP)
		}
	default:
		if slices.Contains(model.Kinds, a.Kind) {
			break
		}
		return model.Agent{}, msg.Err("agent.unknownKind", "kind", a.Kind, "allowed", strings.Join(model.Kinds, ", "))
	}
	return a, nil
}

// ---- proxies ----

type proxyRow struct {
	Name      string `db:"name"`
	NameKey   string `db:"name_key"`
	URL       string `db:"url"`
	NoProxy   string `db:"no_proxy"`
	CACert    string `db:"ca_cert"`
	Username  string `db:"username"`
	CreatedAt int64  `db:"created_at"`
}

func (r proxyRow) proxy() model.Proxy {
	return model.Proxy{Name: r.Name, URL: r.URL, NoProxy: r.NoProxy, CACert: r.CACert, Username: r.Username}
}

// Proxies returns the registry, by name.
func (s *Store) Proxies() ([]model.Proxy, error) {
	var rows []proxyRow
	if err := s.db.Select(&rows, `SELECT * FROM proxy ORDER BY name_key`); err != nil {
		return nil, fmt.Errorf("read proxies: %w", err)
	}
	out := make([]model.Proxy, 0, len(rows))
	for _, r := range rows {
		p := r.proxy()
		pw, err := s.proxyPassword(p.Name)
		if err != nil {
			return nil, err
		}
		p.Password = pw
		out = append(out, p)
	}
	return out, nil
}

// Proxy returns one entry by name.
func (s *Store) Proxy(name string) (model.Proxy, error) {
	var r proxyRow
	err := s.db.Get(&r, `SELECT * FROM proxy WHERE name_key = ?`, nameKey(name))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Proxy{}, msg.Tag(ErrNotFound, "proxy.notFound", "proxy", name)
	}
	if err != nil {
		return model.Proxy{}, err
	}
	p := r.proxy()
	pw, err := s.proxyPassword(p.Name)
	if err != nil {
		return model.Proxy{}, err
	}
	p.Password = pw
	return p, nil
}

// The password lives in the keychain, under the name the entry is found by.
func proxySecretKey(name string) string { return "proxy:" + nameKey(name) }

func (s *Store) proxyPassword(name string) (string, error) {
	pw, err := s.secrets.Token(proxySecretKey(name))
	if err != nil {
		return "", fmt.Errorf("read the password of the proxy %q: %w", name, err)
	}
	return pw, nil
}

// proxySet is the registry indexed for resolving several agents at once.
type proxySet map[string]model.Proxy

func (s *Store) proxyIndex() (proxySet, error) {
	list, err := s.Proxies()
	if err != nil {
		return nil, err
	}
	set := make(proxySet, len(list))
	for _, p := range list {
		set[nameKey(p.Name)] = p
	}
	return set, nil
}

func (ps proxySet) of(a model.Agent) *model.Proxy {
	if a.Proxy == "" {
		return nil
	}
	p, ok := ps[nameKey(a.Proxy)]
	if !ok {
		return nil
	}
	return &p
}

// SaveProxy adds or replaces an entry. oldName is the name it had when the
// form was opened, so a rename is an edit rather than a second entry; agents
// that named it follow. Agents using it are re-checked against the new
// settings.
func (s *Store) SaveProxy(oldName string, p model.Proxy) (model.Proxy, error) {
	p, err := p.Validate("")
	if err != nil {
		return model.Proxy{}, err
	}
	agents, err := s.Agents()
	if err != nil {
		return model.Proxy{}, err
	}
	key := nameKey(p.Name)
	prev := nameKey(oldName)
	if prev == "" {
		prev = key
	}
	var users []string
	for _, a := range agents {
		if a.Proxy == "" || nameKey(a.Proxy) != prev {
			continue
		}
		if _, err := p.Validate(a.Kind); err != nil {
			return model.Proxy{}, msg.Wrap(err, "proxy.unusable", "agent", a.Name, "kind", a.Kind)
		}
		users = append(users, a.Name)
	}
	if key != prev {
		if _, err := s.Proxy(p.Name); err == nil {
			return model.Proxy{}, msg.Err("proxy.exists", "proxy", p.Name)
		}
	}
	// The keychain first: a row whose password did not get stored would start
	// agents without it. A failure after this leaves a stray secret, which the
	// next save of the same name replaces.
	if p.Password != "" {
		if err := s.secrets.SetToken(proxySecretKey(p.Name), p.Password); err != nil {
			return model.Proxy{}, fmt.Errorf("store the proxy password: %w", err)
		}
	} else if err := s.secrets.DeleteToken(proxySecretKey(p.Name)); err != nil {
		return model.Proxy{}, fmt.Errorf("store the proxy password: %w", err)
	}
	tx, err := s.db.Beginx()
	if err != nil {
		return model.Proxy{}, err
	}
	defer tx.Rollback()
	if key != prev {
		// Renamed: the row moves and the agents that named it follow.
		if _, err := tx.Exec(`DELETE FROM proxy WHERE name_key = ?`, prev); err != nil {
			return model.Proxy{}, err
		}
		// By agent name, not lower(): SQLite's is ASCII-only.
		for _, agent := range users {
			if _, err := tx.Exec(`UPDATE agent SET proxy = ? WHERE name = ?`, p.Name, agent); err != nil {
				return model.Proxy{}, err
			}
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO proxy (name, name_key, url, no_proxy, ca_cert, username, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			name_key = excluded.name_key, url = excluded.url, no_proxy = excluded.no_proxy,
			ca_cert = excluded.ca_cert, username = excluded.username`,
		p.Name, key, p.URL, p.NoProxy, p.CACert, p.Username, millis(time.Now())); err != nil {
		return model.Proxy{}, fmt.Errorf("save proxy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Proxy{}, err
	}
	if key != prev {
		_ = s.secrets.DeleteToken(proxySecretKey(oldName))
	}
	return p, nil
}

// DeleteProxy removes an entry, refusing while agents still name it: they
// would quietly fall back to the app's own network.
func (s *Store) DeleteProxy(name string) error {
	agents, err := s.Agents()
	if err != nil {
		return err
	}
	var used []string
	for _, a := range agents {
		if a.Proxy != "" && nameKey(a.Proxy) == nameKey(name) {
			used = append(used, a.Name)
		}
	}
	if len(used) > 0 {
		return msg.Err("proxy.inUse", "proxy", name, "agents", strings.Join(used, ", "))
	}
	res, err := s.db.Exec(`DELETE FROM proxy WHERE name_key = ?`, nameKey(name))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return msg.Tag(ErrNotFound, "proxy.notFound", "proxy", name)
	}
	_ = s.secrets.DeleteToken(proxySecretKey(name))
	return nil
}

// trimAll drops the empties. An empty element — a stray space in a UI input, a
// hand-edited row — would become an empty argv[0] and a confusing exec error.
func trimAll(list []string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---- sources ----

type sourceRow struct {
	Name            string `db:"name"`
	NameKey         string `db:"name_key"`
	Plugin          string `db:"plugin"`
	Enabled         bool   `db:"enabled"`
	Noisy           bool   `db:"noisy"`
	UpdateMode      string `db:"update_mode"`
	Config          string `db:"config"`
	IntervalSeconds int    `db:"interval_seconds"`
	CreatedAt       int64  `db:"created_at"`
}

func (r sourceRow) source() model.Source {
	s := model.Source{
		Name: r.Name, Plugin: r.Plugin, Enabled: r.Enabled, Noisy: r.Noisy,
		Update: r.UpdateMode, IntervalSeconds: r.IntervalSeconds,
	}
	decodeJSON(r.Config, &s.Config)
	return s
}

// Sources returns the registry with every source's rules, by name.
func (s *Store) Sources() ([]model.Source, error) {
	var rows []sourceRow
	if err := s.db.Select(&rows, `SELECT * FROM source ORDER BY name_key`); err != nil {
		return nil, fmt.Errorf("read sources: %w", err)
	}
	out := make([]model.Source, 0, len(rows))
	for _, r := range rows {
		src := r.source()
		rules, err := s.rules(src.Name)
		if err != nil {
			return nil, err
		}
		src.Rules = rules
		out = append(out, src)
	}
	return out, nil
}

// Source returns one registered source with its rules.
func (s *Store) Source(name string) (model.Source, error) {
	var r sourceRow
	err := s.db.Get(&r, `SELECT * FROM source WHERE name_key = ?`, nameKey(name))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Source{}, msg.Tag(ErrNotFound, "source.notFound", "source", name)
	}
	if err != nil {
		return model.Source{}, err
	}
	src := r.source()
	rules, err := s.rules(src.Name)
	if err != nil {
		return model.Source{}, err
	}
	src.Rules = rules
	return src, nil
}

// SaveSource adds or replaces a source and its rules. Rules are stored in
// order, because the first match wins and the order is the whole of what a
// person meant.
func (s *Store) SaveSource(src model.Source) (model.Source, error) {
	src, err := model.ValidateSource(src)
	if err != nil {
		return model.Source{}, err
	}
	err = s.tx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO source (name, name_key, plugin, enabled, noisy, update_mode, config, interval_seconds, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				plugin = excluded.plugin, enabled = excluded.enabled, noisy = excluded.noisy,
				update_mode = excluded.update_mode, config = excluded.config,
				interval_seconds = excluded.interval_seconds`,
			src.Name, nameKey(src.Name), src.Plugin, src.Enabled, src.Noisy, src.UpdateMode(),
			encodeJSON(src.Config), src.IntervalSeconds, millis(time.Now())); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM source_rule WHERE source = ?`, src.Name); err != nil {
			return err
		}
		for i, r := range src.Rules {
			if _, err := tx.Exec(`
				INSERT INTO source_rule (source, ord, name, then_action, match_json, props_json, suggest_flow, assignee)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				src.Name, i, r.Name, r.Then, encodeJSON(r.When), encodeJSON(r.Props),
				r.SuggestFlow, r.Assignee); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Source{}, fmt.Errorf("save source: %w", err)
	}
	return src, nil
}

// DeleteSource removes a source. The cards it brought stay: they are work, and
// the source is only where they came from.
func (s *Store) DeleteSource(name string) error {
	res, err := s.db.Exec(`DELETE FROM source WHERE name_key = ?`, nameKey(name))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return msg.Tag(ErrNotFound, "source.notFound", "source", name)
	}
	return nil
}

func (s *Store) rules(source string) ([]model.Rule, error) {
	var rows []struct {
		Name        string `db:"name"`
		ThenAction  string `db:"then_action"`
		MatchJSON   string `db:"match_json"`
		PropsJSON   string `db:"props_json"`
		SuggestFlow string `db:"suggest_flow"`
		Assignee    string `db:"assignee"`
	}
	if err := s.db.Select(&rows, `
		SELECT name, then_action, match_json, props_json, suggest_flow, assignee
		FROM source_rule WHERE source = ? ORDER BY ord`, source); err != nil {
		return nil, fmt.Errorf("read rules of source %q: %w", source, err)
	}
	out := make([]model.Rule, 0, len(rows))
	for _, r := range rows {
		rule := model.Rule{Name: r.Name, Then: r.ThenAction, SuggestFlow: r.SuggestFlow, Assignee: r.Assignee}
		decodeJSON(r.MatchJSON, &rule.When)
		decodeJSON(r.PropsJSON, &rule.Props)
		out = append(out, rule)
	}
	return out, nil
}

// ---- what a source has already brought ----

// SeenItem records that a source's item was handled, and which card it became.
func (s *Store) SeenItem(source, externalID, version, cardID string) error {
	_, err := s.db.Exec(`
		INSERT INTO source_item (source, external_id, version, card_id, seen_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(source, external_id) DO UPDATE SET
			version = excluded.version, card_id = excluded.card_id, seen_at = excluded.seen_at`,
		source, externalID, version, cardID, millis(time.Now()))
	return err
}

// ItemSeen reports what is known about an item: the card it became and the
// version we last saw. Not found is not an error.
func (s *Store) ItemSeen(source, externalID string) (cardID, version string, ok bool, err error) {
	var r struct {
		CardID  string `db:"card_id"`
		Version string `db:"version"`
	}
	err = s.db.Get(&r, `SELECT card_id, version FROM source_item WHERE source = ? AND external_id = ?`,
		source, externalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return r.CardID, r.Version, true, nil
}
