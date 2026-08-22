package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/artipop/xxvi/internal/model"
)

// The project registry: where work happens. Rows like any other, in the same
// database as the cards that point at them (docs/system.md §9).

type projectRow struct {
	ID        string `db:"id"`
	Name      string `db:"name"`
	NameKey   string `db:"name_key"`
	Kind      string `db:"kind"`
	Path      string `db:"path"`
	CreatedAt int64  `db:"created_at"`
}

func (r projectRow) project() model.Project {
	return model.Project{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Path: r.Path,
		CreatedAt: fromMillis(r.CreatedAt),
	}
}

// Projects is the registry, by name.
func (s *Store) Projects() ([]model.Project, error) {
	var rows []projectRow
	if err := s.db.Select(&rows, `SELECT * FROM project ORDER BY name_key`); err != nil {
		return nil, fmt.Errorf("прочитать проекты: %w", err)
	}
	out := make([]model.Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.project())
	}
	return out, nil
}

// Project is one entry, by the id a card points at.
func (s *Store) Project(id string) (model.Project, error) {
	var r projectRow
	err := s.db.Get(&r, `SELECT * FROM project WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Project{}, fmt.Errorf("проект %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.Project{}, err
	}
	return r.project(), nil
}

// SaveProject writes one entry, creating an id for a new one. The name is
// unique, folded the way every name here is folded.
func (s *Store) SaveProject(p model.Project) (model.Project, error) {
	p, err := model.ValidateProject(p)
	if err != nil {
		return model.Project{}, err
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}

	err = s.tx(func(tx *sqlx.Tx) error {
		var taken string
		err := tx.Get(&taken, `SELECT id FROM project WHERE name_key = ?`, nameKey(p.Name))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if taken != "" && taken != p.ID {
			return fmt.Errorf("проект «%s» уже есть", p.Name)
		}
		_, err = tx.Exec(`
			INSERT INTO project (id, name, name_key, kind, path, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name, name_key = excluded.name_key,
				kind = excluded.kind, path = excluded.path`,
			p.ID, p.Name, nameKey(p.Name), p.Kind, p.Path, millis(p.CreatedAt))
		return err
	})
	if err != nil {
		return model.Project{}, err
	}
	return p, nil
}

// DeleteProject removes an entry, refusing while a card still names it.
//
// Refused rather than cascaded: a card whose project vanished under it would
// start its next step somewhere else entirely, and working in the wrong place
// is worse than not working.
func (s *Store) DeleteProject(id string) error {
	var used int
	if err := s.db.Get(&used, `SELECT COUNT(*) FROM card WHERE project = ?`, id); err != nil {
		return err
	}
	if used > 0 {
		return fmt.Errorf("проект занят: на него ссылаются карточки (%d)", used)
	}
	_, err := s.db.Exec(`DELETE FROM project WHERE id = ?`, id)
	return err
}

// SetCardProject says where a card's work happens.
func (s *Store) SetCardProject(cardID, projectID string) error {
	_, err := s.db.Exec(`UPDATE card SET project = ?, updated_at = ? WHERE id = ?`,
		projectID, millis(time.Now()), cardID)
	return err
}
