package model

import (
	"fmt"
	"strings"
)

// A project is where the work happens: the card says what, the flow says how it
// travels, and this says in which folder it all takes place.
//
// Before it, every card's agent opened in a fresh empty folder beside the
// database. That is right for exactly one case — work that starts from nothing —
// and useless for the rest: a task about existing code has to be done in it.

// Project kinds. Closed and implemented in Go, like stage actions and triggers.
//
// There is one for now. The room for `github` and the rest is the same room the
// trigger kinds keep for `git` (docs/poc.md): adding one changes neither the
// card nor the engine nor how a project is chosen — only how it turns into a
// folder.
const (
	// ProjectFolder is a folder on this machine, and the agent works in it.
	ProjectFolder = "folder"
)

// ProjectKinds is every accepted kind, in the order the editor offers them.
var ProjectKinds = []string{ProjectFolder}

// ProjectKindLabel names a kind for a person.
func ProjectKindLabel(kind string) string {
	if kind == ProjectFolder {
		return "папка"
	}
	return kind
}

// Project is one place work is done.
type Project struct {
	// ID is what a card points at, and the one field that never changes: a
	// project can be renamed without dragging anything behind it, because a
	// name is a label and this is the reference.
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Path is the folder, absolute. For a kind that is not a folder this is
	// where it lands on this machine once it is fetched.
	Path string `json:"path"`
	// Repo says the folder is a git repository, which is what offers a card
	// the choice of a branch of its own. Not stored: asked of the folder when
	// the registry is read, since a folder can become one at any time.
	Repo bool `json:"repo,omitempty"`
}

// There is deliberately no timestamp here, as there is none on an agent, a
// source or a flow. A registry entry is edited and handed back whole, so every
// field on it is a field the editor sends — and a time the editor has no
// business owning arrives from a blank form as an empty string, which is not a
// time. When the row was made is the database's business, and it is the
// database that fills it in.

// ValidateProject normalizes and checks one registry entry. The returned
// project is the one to store.
//
// Whether the folder is actually there is not asked here: this package touches
// no disk, and the answer would be a different one tomorrow anyway. It is asked
// where the entry is saved, which is where a person is looking.
func ValidateProject(p Project) (Project, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Kind = strings.TrimSpace(p.Kind)
	p.Path = strings.TrimSpace(p.Path)

	if p.Name == "" {
		return Project{}, fmt.Errorf("у проекта нет названия")
	}
	if p.Kind == "" {
		p.Kind = ProjectFolder
	}
	if !isProjectKind(p.Kind) {
		return Project{}, fmt.Errorf("неизвестный вид проекта «%s» (допустимо: %s)",
			p.Kind, strings.Join(ProjectKinds, ", "))
	}
	if p.Path == "" {
		return Project{}, fmt.Errorf("у проекта «%s» не указана папка", p.Name)
	}
	// Absolute, because a relative path means "relative to whatever this
	// process happened to be started in", and that is not a place.
	if !strings.HasPrefix(p.Path, "/") && !(len(p.Path) >= 2 && p.Path[1] == ':') {
		return Project{}, fmt.Errorf("путь проекта «%s» должен быть абсолютным: %s", p.Name, p.Path)
	}
	return p, nil
}

func isProjectKind(kind string) bool {
	for _, k := range ProjectKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// ProjectByID finds a project in a registry snapshot.
func ProjectByID(projects []Project, id string) (Project, bool) {
	for _, p := range projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}
