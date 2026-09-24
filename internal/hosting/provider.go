package hosting

import (
	"context"
	"time"
)

// MR is a merge request as this application needs it. One word for both
// hostings: GitHub calls it a pull request, the interface calls it an MR.
type MR struct {
	// IID is the number people say — «!42» — and the one every request is
	// addressed by.
	IID   int    `json:"iid"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	URL   string `json:"url"`
	// Source is the branch the work is on, Target where it lands.
	Source string `json:"source"`
	Target string `json:"target"`
	// Head is the last commit. A new one is how an MR says it changed, and
	// what an approval is pinned to.
	Head   string    `json:"head"`
	State  string    `json:"state"`
	Draft  bool      `json:"draft,omitempty"`
	Author string    `json:"author,omitempty"`
	At     time.Time `json:"at"`
}

// MR states, as this application reads them.
const (
	StateOpen   = "open"
	StateMerged = "merged"
	StateClosed = "closed"
)

// NewMR is what opening one needs.
type NewMR struct {
	Source, Target, Title, Body string
}

// Provider is one hosting's API, for one server and one token. Every call
// names the repository, because one token serves every project on a server.
type Provider interface {
	// Me is who the token belongs to.
	Me(ctx context.Context) (string, error)
	// OpenMR finds the open MR from a branch, if there is one.
	OpenMR(ctx context.Context, repo, source string) (MR, bool, error)
	CreateMR(ctx context.Context, repo string, mr NewMR) (MR, error)
	UpdateMRBody(ctx context.Context, repo string, iid int, body string) error
	MR(ctx context.Context, repo string, iid int) (MR, error)
	// ReviewQueue is the open MRs where this account is a reviewer.
	ReviewQueue(ctx context.Context, repo, account string) ([]MR, error)
	// Approve approves exactly the commit the person looked at: an MR that has
	// moved since is refused rather than approved unseen.
	Approve(ctx context.Context, repo string, iid int, head string) error
	// RequestChanges takes back an approval if there was one and leaves the
	// remarks where the author reads them.
	RequestChanges(ctx context.Context, repo string, iid int, remarks string) error
}
