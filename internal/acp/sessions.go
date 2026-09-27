package acp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// Conversations an agent already had, asked of the agent itself over ACP.
//
// The protocol's session/list is the one catalogue every vendor keeps the same
// way; the files behind it are each vendor's own and change when they please.
// The adapter is started only to answer and is gone afterwards: nothing is
// worked here, which is why it needs no session of ours and no card.

// PastSession is one conversation as the agent lists it.
type PastSession struct {
	ID    string `json:"id"`
	Cwd   string `json:"cwd"`
	Title string `json:"title,omitempty"`
	// UpdatedAt is what the agent says. For claude it is the file's mtime, which
	// the CLI also bumps when it merely annotates an old conversation, so it
	// orders roughly rather than exactly.
	UpdatedAt time.Time `json:"updatedAt"`
}

// pastSessionsMax bounds how much of a long history is fetched: the list is for
// a person picking by eye, and the pages after this are older than anybody
// picks from.
const pastSessionsMax = 200

// PastSessions lists an agent's conversations held in cwd, newest first. An
// agent that does not list its sessions says so as an error rather than as an
// empty list: «none» and «cannot tell» are different answers.
func PastSessions(ctx context.Context, a model.Agent, cwd string) ([]PastSession, error) {
	l, err := launchFor(a)
	if err != nil {
		return nil, err
	}
	argv := resolveArgv0(l.argv)
	if len(argv) == 0 {
		return nil, msg.Err("agent.emptyCommand")
	}
	env := append(append([]string{}, l.env...), spawnEnv(a)...)
	proc, err := spawn(ctx, argv, cwd, env, l.dropEnv...)
	if err != nil {
		return nil, msg.Wrap(err, "agent.startFailed", "command", argv[0])
	}
	defer func() {
		proc.killGroup(2 * time.Second)
		_ = proc.wait()
	}()

	conn := acpsdk.NewClientSideConnection(listingClient{}, proc.stdin, proc.stdout)
	init, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if init.AgentCapabilities.SessionCapabilities.List == nil {
		return nil, msg.Err("sessions.notListed", "agent", a.Name)
	}

	var out []PastSession
	var cursor *string
	for len(out) < pastSessionsMax {
		page, err := conn.ListSessions(ctx, acpsdk.ListSessionsRequest{Cwd: &cwd, Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("session/list: %w", err)
		}
		for _, s := range page.Sessions {
			p := PastSession{ID: string(s.SessionId), Cwd: s.Cwd}
			if s.Title != nil {
				p.Title = *s.Title
			}
			if s.UpdatedAt != nil {
				p.UpdatedAt, _ = time.Parse(time.RFC3339, *s.UpdatedAt)
			}
			out = append(out, p)
		}
		if page.NextCursor == nil || *page.NextCursor == "" || len(page.Sessions) == 0 {
			break
		}
		cursor = page.NextCursor
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// listingClient is the client side of a connection that only asks. An agent
// has no reason to call back while listing; if one does, it is refused rather
// than served with nobody's card behind it.
type listingClient struct{}

var _ acpsdk.Client = listingClient{}

var errListingOnly = errors.New("this connection only lists sessions")

func (listingClient) ReadTextFile(context.Context, acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, errListingOnly
}

func (listingClient) WriteTextFile(context.Context, acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, errListingOnly
}

func (listingClient) RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{}}}, nil
}

func (listingClient) SessionUpdate(context.Context, acpsdk.SessionNotification) error { return nil }

func (listingClient) CreateTerminal(context.Context, acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, errListingOnly
}

func (listingClient) KillTerminal(context.Context, acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, errListingOnly
}

func (listingClient) TerminalOutput(context.Context, acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, errListingOnly
}

func (listingClient) ReleaseTerminal(context.Context, acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, errListingOnly
}

func (listingClient) WaitForTerminalExit(context.Context, acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, errListingOnly
}
