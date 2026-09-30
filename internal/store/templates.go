package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// The stage templates: the kinds of node the flow editor's palette offers
// (model.StageTemplate). A registry like the agents, ordered by hand rather
// than by name, because the palette is read in the order it was laid out.

type templateRow struct {
	ID          string `db:"id"`
	Ord         int    `db:"ord"`
	Name        string `db:"name"`
	Description string `db:"description"`
	Icon        string `db:"icon"`
	Color       string `db:"color"`
	Builtin     bool   `db:"builtin"`
	Action      string `db:"action"`
	Work        string `db:"work"`
	Prompt      string `db:"prompt"`
	Final       bool   `db:"final"`
	ScreensJSON string `db:"screens_json"`
	UpdatedAt   int64  `db:"updated_at"`
}

func (r templateRow) template() model.StageTemplate {
	t := model.StageTemplate{
		ID: r.ID, Name: r.Name, Description: r.Description, Icon: r.Icon, Color: r.Color,
		Builtin: r.Builtin, Action: r.Action, Work: r.Work, Prompt: r.Prompt, Final: r.Final,
	}
	decodeJSON(r.ScreensJSON, &t.Screens)
	// Absent rather than empty, the way a template arrives from the editor and
	// the way ValidateTemplate leaves it.
	if len(t.Screens) == 0 {
		t.Screens = nil
	}
	return t
}

// StageTemplates returns the palette, in its order.
func (s *Store) StageTemplates() ([]model.StageTemplate, error) {
	var rows []templateRow
	if err := s.db.Select(&rows, `SELECT * FROM stage_template ORDER BY ord, id`); err != nil {
		return nil, fmt.Errorf("read stage templates: %w", err)
	}
	out := make([]model.StageTemplate, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.template())
	}
	return out, nil
}

// SaveStageTemplate adds a template or replaces one by id. A new one goes to
// the end of the palette; an edited one stays where it was.
func (s *Store) SaveStageTemplate(t model.StageTemplate) (model.StageTemplate, error) {
	t, err := model.ValidateTemplate(t)
	if err != nil {
		return model.StageTemplate{}, err
	}
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	// Whether it is builtin is the row's to say, not the caller's: an edited
	// builtin stays one, and a new template cannot claim the application's
	// words by setting the flag.
	var builtin bool
	err = s.db.Get(&builtin, `SELECT builtin FROM stage_template WHERE id = ?`, t.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		builtin = false
	case err != nil:
		return model.StageTemplate{}, err
	}
	t.Builtin = builtin
	if t.Name == "" && !t.Builtin {
		return model.StageTemplate{}, msg.Err("template.noName")
	}
	_, err = s.db.Exec(`
		INSERT INTO stage_template (id, ord, name, description, icon, color, builtin, action, work, prompt, final, screens_json, updated_at)
		VALUES (?, (SELECT COALESCE(MAX(ord), 0) + 1 FROM stage_template), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, description = excluded.description, icon = excluded.icon,
			color = excluded.color, action = excluded.action, work = excluded.work,
			prompt = excluded.prompt, final = excluded.final,
			screens_json = excluded.screens_json, updated_at = excluded.updated_at`,
		t.ID, t.Name, t.Description, t.Icon, t.Color, t.Builtin, t.Action, t.Work, t.Prompt,
		t.Final, encodeJSON(nonNil(t.Screens)), millis(time.Now()))
	if err != nil {
		return model.StageTemplate{}, fmt.Errorf("save stage template: %w", err)
	}
	return t, nil
}

// DeleteStageTemplate removes a template. The stages made from it keep what
// they were given, and keep its id: the canvas then draws them plainly, and
// nothing about how a card moves depends on it.
func (s *Store) DeleteStageTemplate(id string) error {
	res, err := s.db.Exec(`DELETE FROM stage_template WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return msg.Tag(ErrNotFound, "template.notFound", "template", id)
	}
	return nil
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}
