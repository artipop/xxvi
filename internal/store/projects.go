package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// The project registry: where work happens. Rows like any other, in the same
// database as the cards that point at them (docs/system.md §9).

type projectRow struct {
	ID        string `db:"id"`
	Name      string `db:"name"`
	NameKey   string `db:"name_key"`
	Kind      string `db:"kind"`
	Path      string `db:"path"`
	Remote    string `db:"remote"`
	Provider  string `db:"provider"`
	CreatedAt int64  `db:"created_at"`
}

func (r projectRow) project() model.Project {
	return model.Project{ID: r.ID, Name: r.Name, Kind: r.Kind, Path: r.Path, Remote: r.Remote, Provider: r.Provider}
}

// Projects is the registry, by name.
func (s *Store) Projects() ([]model.Project, error) {
	var rows []projectRow
	if err := s.db.Select(&rows, `SELECT * FROM project ORDER BY name_key`); err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
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
		return model.Project{}, msg.Tag(ErrNotFound, "project.notFound", "project", id)
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
	err = s.tx(func(tx *sqlx.Tx) error {
		var taken string
		err := tx.Get(&taken, `SELECT id FROM project WHERE name_key = ?`, nameKey(p.Name))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if taken != "" && taken != p.ID {
			return msg.Err("project.nameTaken", "project", p.Name)
		}
		_, err = tx.Exec(`
			INSERT INTO project (id, name, name_key, kind, path, remote, provider, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name, name_key = excluded.name_key,
				kind = excluded.kind, path = excluded.path,
				remote = excluded.remote, provider = excluded.provider`,
			p.ID, p.Name, nameKey(p.Name), p.Kind, p.Path, p.Remote, p.Provider, millis(time.Now()))
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
		return msg.Err("project.inUse", "count", strconv.Itoa(used))
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

// SetCardWorkMode says how a card works in its project's repository.
func (s *Store) SetCardWorkMode(cardID, mode string) error {
	_, err := s.db.Exec(`UPDATE card SET work_mode = ?, updated_at = ? WHERE id = ?`,
		mode, millis(time.Now()), cardID)
	return err
}

// SetCardWorkspace records what the card's work mode came to: its branch, what
// that was cut from, and the working tree when there is one.
func (s *Store) SetCardWorkspace(cardID, branch, base, worktree string) error {
	// A tree made again is a new question when the card closes again.
	_, err := s.db.Exec(`UPDATE card SET branch = ?, base_ref = ?, worktree = ?, keep_worktree = 0, updated_at = ? WHERE id = ?`,
		branch, base, worktree, millis(time.Now()), cardID)
	return err
}

// FolderHolder is the card that has a project's folder switched to its branch,
// other than the one asking: a card in branch mode with its branch made and
// its work not over. Not found is the folder being free.
//
// No lock of its own: the card's state is the lock. A card that is done or
// dropped has let go, and nothing has to remember to release anything.
func (s *Store) FolderHolder(projectID, exceptCard string) (model.Card, bool, error) {
	var r cardRow
	err := s.db.Get(&r, `
		SELECT * FROM card
		WHERE project = ? AND work_mode = ? AND branch != '' AND id != ?
		  AND state NOT IN (?, ?)
		ORDER BY updated_at DESC LIMIT 1`,
		projectID, model.WorkModeBranch, exceptCard, string(model.StateDone), string(model.StateDropped))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Card{}, false, nil
	}
	if err != nil {
		return model.Card{}, false, err
	}
	return r.card(), true, nil
}

// ClosedWithWorktree is every card that is done or dropped and still has a
// working tree nobody has decided about: what is asked of a person, whether to
// remove it.
func (s *Store) ClosedWithWorktree() ([]model.Card, error) {
	var rows []cardRow
	if err := s.db.Select(&rows, `
		SELECT * FROM card
		WHERE worktree != '' AND keep_worktree = 0 AND state IN (?, ?)
		ORDER BY updated_at`,
		string(model.StateDone), string(model.StateDropped)); err != nil {
		return nil, err
	}
	out := make([]model.Card, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.card())
	}
	return out, nil
}

// KeepWorktree records that a person chose to keep a closed card's tree.
func (s *Store) KeepWorktree(cardID string) error {
	_, err := s.db.Exec(`UPDATE card SET keep_worktree = 1 WHERE id = ?`, cardID)
	return err
}
