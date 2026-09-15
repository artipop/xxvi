package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/artipop/xxvi/internal/model"
)

// A flow is saved whole: the editor hands over the entire graph, it is checked
// entirely (model.ValidateFlow) and then replaces what was there. Stages and
// edges are deleted and re-inserted rather than diffed — the graph is small,
// the ids are the editor's, and a diff would be a second description of the
// same change with its own bugs.

// ErrNotFound is what every lookup by id or name returns for something absent.
var ErrNotFound = errors.New("не найдено")

// Flows returns every flow with its stages and edges, by name.
func (s *Store) Flows() ([]model.Flow, error) {
	var rows []flowRow
	if err := s.db.Select(&rows, `SELECT * FROM flow ORDER BY name_key`); err != nil {
		return nil, fmt.Errorf("прочитать флоу: %w", err)
	}
	out := make([]model.Flow, 0, len(rows))
	for _, r := range rows {
		f, err := s.loadGraph(r)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// Flow returns one flow by id.
func (s *Store) Flow(id string) (model.Flow, error) {
	var r flowRow
	err := s.db.Get(&r, `SELECT * FROM flow WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Flow{}, fmt.Errorf("флоу %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.Flow{}, err
	}
	return s.loadGraph(r)
}

// FlowByName returns one flow by the name a person reads.
func (s *Store) FlowByName(name string) (model.Flow, error) {
	var r flowRow
	err := s.db.Get(&r, `SELECT * FROM flow WHERE name_key = ?`, nameKey(name))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Flow{}, fmt.Errorf("флоу «%s»: %w", name, ErrNotFound)
	}
	if err != nil {
		return model.Flow{}, err
	}
	return s.loadGraph(r)
}

// FlowForStage finds the flow a stage belongs to. It is what the engine asks
// when it has a card's position and needs the graph around it.
func (s *Store) FlowForStage(stageID string) (model.Flow, error) {
	var flowID string
	err := s.db.Get(&flowID, `SELECT flow_id FROM stage WHERE id = ?`, stageID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Flow{}, fmt.Errorf("стадия %q: %w", stageID, ErrNotFound)
	}
	if err != nil {
		return model.Flow{}, err
	}
	return s.Flow(flowID)
}

// SaveFlow validates a flow against the agent registry and stores it whole,
// creating it if its id is new. The stored flow is returned: ids filled in,
// crews spelled the way the registry spells them.
func (s *Store) SaveFlow(f model.Flow) (model.Flow, error) {
	agents, err := s.Agents()
	if err != nil {
		return model.Flow{}, err
	}
	// Ids are settled before validation, so an edge may reference a stage the
	// editor created in the same save.
	f = withIDs(f)
	f, err = model.ValidateFlow(f, agents)
	if err != nil {
		return model.Flow{}, err
	}

	err = s.tx(func(tx *sqlx.Tx) error {
		var clash string
		err := tx.Get(&clash, `SELECT id FROM flow WHERE name_key = ? AND id <> ?`, nameKey(f.Name), f.ID)
		if err == nil {
			return fmt.Errorf("флоу с именем «%s» уже существует", f.Name)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := checkStageIDsAreFree(tx, f); err != nil {
			return err
		}

		if _, err := tx.Exec(`
			INSERT INTO flow (id, name, name_key, description, entry_stage, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				name_key = excluded.name_key,
				description = excluded.description,
				entry_stage = excluded.entry_stage,
				updated_at = excluded.updated_at`,
			f.ID, f.Name, nameKey(f.Name), f.Description, f.EntryStage, millis(time.Now())); err != nil {
			return err
		}

		// Cards standing on a stage that survives this save keep standing on
		// it: their position references the stage id, and the ids are the
		// editor's own. A stage that is gone leaves its cards where they are,
		// and the card says its stage disappeared (docs/system.md §10).
		if _, err := tx.Exec(`DELETE FROM edge WHERE flow_id = ?`, f.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM stage WHERE flow_id = ?`, f.ID); err != nil {
			return err
		}
		for i, st := range f.Stages {
			if _, err := tx.Exec(`
				INSERT INTO stage (id, flow_id, ord, name, action, work, prompt, max_running, final, x, y, writes_json, reads_json, screens_json)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				st.ID, f.ID, i, st.Name, st.Action, st.Work, st.Prompt, st.MaxRunning, st.Final, st.X, st.Y,
				encodeJSON(st.Writes), encodeJSON(st.Reads), encodeJSON(st.Screens)); err != nil {
				return err
			}
			for j, name := range st.Crew {
				if _, err := tx.Exec(`INSERT INTO stage_agent (stage_id, ord, agent_name, agent_key) VALUES (?, ?, ?, ?)`,
					st.ID, j, name, model.Username(name)); err != nil {
					return err
				}
			}
		}
		for i, e := range f.Edges {
			var prop, value, comment string
			if e.If != nil {
				prop, value, comment = e.If.Property, e.If.Value, e.If.CommentContains
			}
			if _, err := tx.Exec(`
				INSERT INTO edge (id, flow_id, ord, from_stage, to_stage, on_trigger, cond_property, cond_value, cond_comment)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.ID, f.ID, i, e.From, e.To, e.On, prop, value, comment); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Flow{}, err
	}
	return f, nil
}

// DeleteFlow removes a flow. Cards standing on it are not moved and not
// deleted: they stop advancing by themselves and say so, which is the same
// thing that happens to a card whose stage was edited away.
func (s *Store) DeleteFlow(id string) error {
	res, err := s.db.Exec(`DELETE FROM flow WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("флоу %q: %w", id, ErrNotFound)
	}
	return nil
}

// checkStageIDsAreFree refuses a flow whose stage id belongs to another one.
//
// Stage ids are unique across every flow, not merely within one: a card records
// where it stands by stage id, and "which flow is this stage in" has to have a
// single answer. The editor generates uuids, so this never fires for it — it
// fires for a flow written by hand, and it fires with a sentence rather than
// with a constraint violation.
func checkStageIDsAreFree(tx *sqlx.Tx, f model.Flow) error {
	for _, st := range f.Stages {
		var owner string
		err := tx.Get(&owner, `
			SELECT fl.name FROM stage s JOIN flow fl ON fl.id = s.flow_id
			WHERE s.id = ? AND s.flow_id <> ?`, st.ID, f.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("идентификатор стадии «%s» уже занят флоу «%s» — он должен быть уникальным среди всех флоу",
			st.ID, owner)
	}
	return nil
}

// withIDs gives a flow, its stages and its edges the ids they are missing. The
// editor may send a brand-new graph with none at all.
func withIDs(f model.Flow) model.Flow {
	if strings.TrimSpace(f.ID) == "" {
		f.ID = uuid.NewString()
	}
	for i, st := range f.Stages {
		if strings.TrimSpace(st.ID) == "" {
			f.Stages[i].ID = uuid.NewString()
		}
	}
	for i, e := range f.Edges {
		if strings.TrimSpace(e.ID) == "" {
			f.Edges[i].ID = uuid.NewString()
		}
	}
	return f
}

// ---- rows ----

type flowRow struct {
	ID          string `db:"id"`
	Name        string `db:"name"`
	NameKey     string `db:"name_key"`
	Description string `db:"description"`
	EntryStage  string `db:"entry_stage"`
	UpdatedAt   int64  `db:"updated_at"`
}

type stageRow struct {
	ID          string  `db:"id"`
	FlowID      string  `db:"flow_id"`
	Ord         int     `db:"ord"`
	Name        string  `db:"name"`
	Action      string  `db:"action"`
	Work        string  `db:"work"`
	Prompt      string  `db:"prompt"`
	MaxRunning  int     `db:"max_running"`
	Final       bool    `db:"final"`
	X           float64 `db:"x"`
	Y           float64 `db:"y"`
	WritesJSON  string  `db:"writes_json"`
	ReadsJSON   string  `db:"reads_json"`
	ScreensJSON string  `db:"screens_json"`
}

type edgeRow struct {
	ID           string `db:"id"`
	FlowID       string `db:"flow_id"`
	Ord          int    `db:"ord"`
	FromStage    string `db:"from_stage"`
	ToStage      string `db:"to_stage"`
	OnTrigger    string `db:"on_trigger"`
	CondProperty string `db:"cond_property"`
	CondValue    string `db:"cond_value"`
	CondComment  string `db:"cond_comment"`
}

// loadGraph fills a flow's stages and edges. Three queries whatever the size of
// the graph: the crews come back in one pass and are handed out by stage.
func (s *Store) loadGraph(r flowRow) (model.Flow, error) {
	f := model.Flow{ID: r.ID, Name: r.Name, Description: r.Description, EntryStage: r.EntryStage}

	var stages []stageRow
	if err := s.db.Select(&stages, `SELECT * FROM stage WHERE flow_id = ? ORDER BY ord`, r.ID); err != nil {
		return model.Flow{}, fmt.Errorf("прочитать стадии флоу «%s»: %w", r.Name, err)
	}
	crews, err := s.crews(r.ID)
	if err != nil {
		return model.Flow{}, err
	}
	for _, st := range stages {
		stage := model.Stage{
			ID: st.ID, Name: st.Name, Action: st.Action, Work: st.Work, Prompt: st.Prompt,
			Crew: crews[st.ID], MaxRunning: st.MaxRunning, Final: st.Final, X: st.X, Y: st.Y,
		}
		decodeJSON(st.WritesJSON, &stage.Writes)
		decodeJSON(st.ReadsJSON, &stage.Reads)
		decodeJSON(st.ScreensJSON, &stage.Screens)
		f.Stages = append(f.Stages, stage)
	}

	var edges []edgeRow
	if err := s.db.Select(&edges, `SELECT * FROM edge WHERE flow_id = ? ORDER BY ord`, r.ID); err != nil {
		return model.Flow{}, fmt.Errorf("прочитать переходы флоу «%s»: %w", r.Name, err)
	}
	for _, e := range edges {
		edge := model.Edge{ID: e.ID, From: e.FromStage, To: e.ToStage, On: e.OnTrigger}
		cond := model.Cond{Property: e.CondProperty, Value: e.CondValue, CommentContains: e.CondComment}
		if !cond.IsZero() {
			edge.If = &cond
		}
		f.Edges = append(f.Edges, edge)
	}
	return f, nil
}

func (s *Store) crews(flowID string) (map[string][]string, error) {
	var rows []struct {
		StageID string `db:"stage_id"`
		Name    string `db:"agent_name"`
	}
	if err := s.db.Select(&rows, `
		SELECT sa.stage_id, sa.agent_name
		FROM stage_agent sa
		JOIN stage s ON s.id = sa.stage_id
		WHERE s.flow_id = ?
		ORDER BY sa.stage_id, sa.ord`, flowID); err != nil {
		return nil, fmt.Errorf("прочитать составы стадий: %w", err)
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.StageID] = append(out[r.StageID], r.Name)
	}
	return out, nil
}
