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
	ingester Ingester
	trees    Trees
	notify   func(Notice)
	emit     func(event string)
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

// Describe fills in what a project's hosting comes to on this machine: the
// repository on the server, and who we are there.
func (s *Service) Describe(p *model.Project) {
	if p.Server == "" || p.Remote == "" {
		return
	}
	if url, err := RemoteURL(p.Path, p.Remote, p.Server); err == nil {
		p.Repository, _ = RepoPath(p.Server, url)
	}
	p.Account, _ = s.store.Setting(accountKey(p.Server), "")
}

// UseSecrets replaces where tokens are kept. A test uses it to stay out of
// the keychain of the machine it runs on.
func (s *Service) UseSecrets(secrets Secrets) { s.secrets = secrets }

// RemoteOption is one of a repository's remotes, as the connect form offers
// it: the name, the address, and the server and repository it suggests.
type RemoteOption struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Server     string `json:"server,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// Remotes is every remote of a project's repository, «origin» first.
func Remotes(folder string) []RemoteOption {
	out, err := git(folder, "remote")
	if err != nil {
		return nil
	}
	var list []RemoteOption
	for _, name := range strings.Fields(out) {
		url, err := RemoteURL(folder, name, "")
		if err != nil {
			continue
		}
		opt := RemoteOption{Name: name, URL: url}
		if r, ok := ParseRemote(url); ok {
			opt.Server, opt.Repository = r.Web, r.Path
		}
		if name == "origin" {
			list = append([]RemoteOption{opt}, list...)
		} else {
			list = append(list, opt)
		}
	}
	return list
}

// RemoteURL is a remote's address. Git's own answer first, with its
// «insteadOf» rewrites applied; the address as configured when that is not
// one this package can read — a rewrite to a local mirror or a shortcut is
// how git is told where to go, not where the repository lives.
func RemoteURL(folder, name, server string) (string, error) {
	url, err := git(folder, "remote", "get-url", name)
	if err != nil {
		return "", msg.Wrap(err, "hosting.noRemote", "remote", name)
	}
	if _, ok := RepoPath(server, url); ok {
		return url, nil
	}
	if raw, err := git(folder, "config", "--get", "remote."+name+".url"); err == nil {
		if _, ok := RepoPath(server, raw); ok {
			return raw, nil
		}
	}
	return url, nil
}

// Connect ties a project to a server through one of its remotes, with a
// token checked by asking the server who it belongs to. Nothing is kept until
// the server says: a token typed wrong or a server address that is not a
// GitLab is refused here, where it was typed, rather than on the first stage
// that needs it.
func (s *Service) Connect(p model.Project, provider, remote, server, token string) (model.Project, error) {
	server, ok := ServerURL(server)
	if !ok {
		return model.Project{}, msg.Err("hosting.badServer")
	}
	// Changing which remote a connected project goes through does not ask
	// for the token again: the server already has one here.
	token = strings.TrimSpace(token)
	if token == "" {
		token, _ = s.secrets.Token(server)
	}
	if token == "" {
		return model.Project{}, msg.Err("hosting.noToken")
	}
	url, err := RemoteURL(p.Path, remote, server)
	if err != nil {
		return model.Project{}, err
	}
	if _, ok := RepoPath(server, url); !ok {
		return model.Project{}, msg.Err("hosting.unreadableRemote", "remote", remote, "url", url)
	}
	p.Remote, p.Server, p.Provider = remote, server, provider
	api, err := s.api(p, token)
	if err != nil {
		return model.Project{}, err
	}
	account, err := api.Me(context.Background())
	if err != nil {
		return model.Project{}, err
	}
	if err := s.secrets.SetToken(server, token); err != nil {
		return model.Project{}, msg.Wrap(err, "hosting.keychainFailed")
	}
	if err := s.store.SetSetting(accountKey(server), account); err != nil {
		return model.Project{}, err
	}
	return s.store.SaveProject(p)
}

// Disconnect unties a project from its hosting. The server's token goes
// only with the last project on that server: it was never this project's.
func (s *Service) Disconnect(p model.Project) (model.Project, error) {
	server := p.Server
	p.Remote, p.Server, p.Provider = "", "", ""
	saved, err := s.store.SaveProject(p)
	if err != nil || server == "" {
		return saved, err
	}
	projects, err := s.store.Projects()
	if err != nil {
		return saved, err
	}
	for _, other := range projects {
		if other.Server == server {
			return saved, nil
		}
	}
	if err := s.secrets.DeleteToken(server); err != nil {
		return saved, msg.Wrap(err, "hosting.keychainFailed")
	}
	return saved, s.store.SetSetting(accountKey(server), "")
}

// For is the API of a project's hosting, with the token kept for its server,
// and where the project is on that server.
func (s *Service) For(p model.Project) (Remote, Provider, error) {
	if p.Server == "" || p.Provider == "" {
		return Remote{}, nil, msg.Err("hosting.notConnected", "project", p.Name)
	}
	token, err := s.secrets.Token(p.Server)
	if err != nil {
		return Remote{}, nil, msg.Wrap(err, "hosting.keychainFailed")
	}
	if token == "" {
		return Remote{}, nil, msg.Err("hosting.notConnected", "project", p.Name)
	}
	url, err := RemoteURL(p.Path, p.Remote, p.Server)
	if err != nil {
		return Remote{}, nil, err
	}
	path, ok := RepoPath(p.Server, url)
	if !ok {
		return Remote{}, nil, msg.Err("hosting.unreadableRemote", "remote", p.Remote, "url", url)
	}
	api, err := s.api(p, token)
	if err != nil {
		return Remote{}, nil, err
	}
	return Remote{Web: p.Server, Path: path}, api, nil
}

func (s *Service) api(p model.Project, token string) (Provider, error) {
	switch p.Provider {
	case model.ProviderGitLab:
		return NewGitLab(p.Server, token, s.client), nil
	}
	return nil, msg.Err("project.unknownProvider", "provider", p.Provider, "allowed", strings.Join(model.Providers, ", "))
}

// RemoteName is the remote a project pushes to and fetches from: the one its
// hosting was connected through.
func RemoteName(p model.Project) string {
	if p.Remote != "" {
		return p.Remote
	}
	return "origin"
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
