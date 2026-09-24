package hosting

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// Service is the hosting side of the application: which server a project is
// on, who we are there, and — in the files beside this one — the stage that
// pushes and opens an MR, the verdict sent back to one, and the reading of MRs
// that wait on a review.
type Service struct {
	store   *store.Store
	secrets Secrets
	client  *http.Client
	log     *slog.Logger

	reporter Reporter
	pollState
}

func New(st *store.Store, secrets Secrets, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	if secrets == nil {
		secrets = &Memory{}
	}
	return &Service{store: st, secrets: secrets, client: &http.Client{}, log: log}
}

// accountKey is where the account a server's token belongs to is remembered:
// asked once, when the token is given, rather than on every read of the
// project list.
func accountKey(web string) string { return "hosting.account|" + web }

// Describe fills in what a project's hosting comes to on this machine: who we
// are on its server.
func (s *Service) Describe(p *model.Project) {
	r, ok := ParseRemote(p.Remote)
	if !ok {
		return
	}
	p.Server, p.Repository = r.Web, r.Path
	if p.Provider != "" {
		p.Account, _ = s.store.Setting(accountKey(r.Web), "")
	}
}

// UseSecrets replaces where tokens are kept. A test uses it to stay out of
// the keychain of the machine it runs on.
func (s *Service) UseSecrets(secrets Secrets) { s.secrets = secrets }

// Connect checks a token by asking the server who it belongs to, and keeps
// it only when the server says. A token that was typed wrong is refused
// here, where it was typed, rather than on the first stage that needs it.
func (s *Service) Connect(p model.Project, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", msg.Err("hosting.noToken")
	}
	r, provider, err := s.provider(p, token)
	if err != nil {
		return "", err
	}
	account, err := provider.Me(context.Background())
	if err != nil {
		return "", err
	}
	if err := s.secrets.SetToken(r.Web, token); err != nil {
		return "", msg.Wrap(err, "hosting.keychainFailed")
	}
	if err := s.store.SetSetting(accountKey(r.Web), account); err != nil {
		return "", err
	}
	return account, nil
}

// Disconnect forgets the token of a project's server — for every project on
// that server, since the token was never the project's.
func (s *Service) Disconnect(p model.Project) error {
	r, ok := ParseRemote(p.Remote)
	if !ok {
		return nil
	}
	if err := s.secrets.DeleteToken(r.Web); err != nil {
		return msg.Wrap(err, "hosting.keychainFailed")
	}
	return s.store.SetSetting(accountKey(r.Web), "")
}

// For is the API of a project's hosting, with the token kept for its server.
func (s *Service) For(p model.Project) (Remote, Provider, error) {
	r, ok := ParseRemote(p.Remote)
	if !ok {
		return Remote{}, nil, msg.Err("hosting.noRemote", "project", p.Name)
	}
	token, err := s.secrets.Token(r.Web)
	if err != nil {
		return Remote{}, nil, msg.Wrap(err, "hosting.keychainFailed")
	}
	if token == "" {
		return Remote{}, nil, msg.Err("hosting.notConnected", "project", p.Name, "server", r.Web)
	}
	return s.provider(p, token)
}

func (s *Service) provider(p model.Project, token string) (Remote, Provider, error) {
	r, ok := ParseRemote(p.Remote)
	if !ok {
		return Remote{}, nil, msg.Err("hosting.noRemote", "project", p.Name)
	}
	switch p.Provider {
	case model.ProviderGitLab:
		return r, NewGitLab(r.Web, token, s.client), nil
	case "":
		return Remote{}, nil, msg.Err("hosting.noProvider", "project", p.Name, "server", r.Web)
	}
	return Remote{}, nil, msg.Err("project.unknownProvider", "provider", p.Provider, "allowed", strings.Join(model.Providers, ", "))
}

// DetectRemote is origin's address, or empty for a folder that has none.
func DetectRemote(folder string) string {
	out, err := git(folder, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return out
}

// gitTimeout bounds one git command. Longer than a local one elsewhere in the
// application: a push and a fetch cross the network.
const gitTimeout = 3 * time.Minute

func git(folder string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", folder}, args...)...)
	// A push that wants a password would wait for one on a terminal nobody
	// sees; failing with git's own words is the answer that can be acted on.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
