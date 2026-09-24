package model

import (
	"strings"

	"github.com/artipop/xxvi/internal/msg"
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

	// Remote, Server and Provider are the project's hosting, as a person set
	// it up: which of the repository's remotes is the one on the hosting —
	// «origin», or «upstream» where origin is a fork — the server's address as
	// its web interface opens it, and which hosting it is. Chosen, not guessed:
	// a server's name says nothing reliable about what it runs, and ssh and
	// the web interface are not always on one host. Empty until connected.
	//
	// The remote is kept by name and its address asked of git when needed, so
	// a remote pointed somewhere else in git is followed rather than
	// remembered wrong.
	Remote   string `json:"remote,omitempty"`
	Server   string `json:"server,omitempty"`
	Provider string `json:"provider,omitempty"`

	// Repository is the project's path on the server, «group/repo», read off
	// the remote's address. Not stored: it is the remote, read another way.
	Repository string `json:"repository,omitempty"`

	// Account is who the application is on the hosting, when a token is kept
	// for its server. Not stored with the project: the token belongs to the
	// server, and every project on it shares the answer.
	Account string `json:"account,omitempty"`
	// ReviewInbox says the merge requests waiting on this account's review
	// come into the inbox. Read off the source registry, where it lives.
	ReviewInbox bool `json:"reviewInbox,omitempty"`
}

// Hosting providers: which API a project's remote speaks. Closed, like
// project kinds. GitHub is the one missing and the next to come; until then a
// provider nobody here speaks is not offered, since a choice that does nothing
// is a trap.
const (
	ProviderGitLab = "gitlab"
)

// Providers is every accepted provider, in the order the editor offers them.
var Providers = []string{ProviderGitLab}

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
		return Project{}, msg.Err("project.noName")
	}
	if p.Kind == "" {
		p.Kind = ProjectFolder
	}
	if !isProjectKind(p.Kind) {
		return Project{}, msg.Err("project.unknownKind",
			"kind", p.Kind, "allowed", strings.Join(ProjectKinds, ", "))
	}
	p.Remote = strings.TrimSpace(p.Remote)
	p.Server = strings.TrimRight(strings.TrimSpace(p.Server), "/")
	p.Provider = strings.TrimSpace(p.Provider)
	if p.Provider != "" && !isProvider(p.Provider) {
		return Project{}, msg.Err("project.unknownProvider",
			"provider", p.Provider, "allowed", strings.Join(Providers, ", "))
	}
	if p.Path == "" {
		return Project{}, msg.Err("project.noPath", "project", p.Name)
	}
	// Absolute, because a relative path means "relative to whatever this
	// process happened to be started in", and that is not a place.
	if !strings.HasPrefix(p.Path, "/") && !(len(p.Path) >= 2 && p.Path[1] == ':') {
		return Project{}, msg.Err("project.relativePath", "project", p.Name, "path", p.Path)
	}
	return p, nil
}

func isProvider(provider string) bool {
	for _, p := range Providers {
		if p == provider {
			return true
		}
	}
	return false
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
