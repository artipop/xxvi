// Package hosting is where a project pushes to, and the handful of questions
// the application asks there: open a merge request, find one, say how it
// stands, list the ones waiting on a review, leave a verdict.
//
// Pushing is not here: that is git, run with the credentials a person already
// has, the same way the diff is read (docs/system.md §12.7). This package is
// the hosting's API, over plain HTTP with a token. `glab` and `gh` are not
// required for it: five requests are not worth installing a program for, and
// the hosting's answer is the answer either way.
package hosting

import (
	"net/url"
	"strings"

	"github.com/artipop/xxvi/internal/model"
)

// Remote is a project's origin, taken apart: which server, and which
// repository on it.
type Remote struct {
	// Web is the server as a browser opens it — scheme and host, with the port
	// only when the remote was reached over HTTP on one. It is also what a token
	// is kept under: one token per server, not per project.
	Web string `json:"web"`
	// Path is the repository on the server, «group/subgroup/repo», without
	// «.git». GitLab nests groups, so this is not always two parts.
	Path string `json:"path"`
}

// IsZero reports a remote that could not be read.
func (r Remote) IsZero() bool { return r.Web == "" || r.Path == "" }

// Host is the server's name without the scheme.
func (r Remote) Host() string {
	if u, err := url.Parse(r.Web); err == nil {
		return u.Host
	}
	return r.Web
}

// ParseRemote reads the forms git accepts for a remote: scp-like
// «git@host:group/repo.git», «ssh://git@host:2222/group/repo.git» and
// «https://host/group/repo.git». A remote over ssh is served over https: the
// ssh port says nothing about where the web interface is.
func ParseRemote(raw string) (Remote, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Remote{}, false
	}
	var host, path, scheme string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return Remote{}, false
		}
		scheme, path = u.Scheme, u.Path
		switch u.Scheme {
		case "http", "https":
			host = u.Host
		case "ssh", "git", "git+ssh":
			host, scheme = u.Hostname(), "https"
		default:
			return Remote{}, false
		}
	} else {
		// scp-like: [user@]host:path. A Windows path «C:\…» has a colon too,
		// and a one-letter host is how to tell it apart.
		at, rest, ok := strings.Cut(raw, ":")
		if !ok || len(at) < 2 || strings.ContainsAny(at, `/\`) {
			return Remote{}, false
		}
		if i := strings.LastIndex(at, "@"); i >= 0 {
			at = at[i+1:]
		}
		host, path, scheme = at, rest, "https"
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if host == "" || path == "" || !strings.Contains(path, "/") {
		return Remote{}, false
	}
	return Remote{Web: scheme + "://" + host, Path: path}, true
}

// GuessProvider says which hosting a server is, when its name says so. A
// company's own GitLab is often called something else entirely, which is why
// the answer is stored on the project and a person can correct it.
func GuessProvider(r Remote) string {
	host := strings.ToLower(r.Host())
	if strings.Contains(host, "gitlab") {
		return model.ProviderGitLab
	}
	return ""
}
