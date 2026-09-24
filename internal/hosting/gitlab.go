package hosting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/artipop/xxvi/internal/msg"
)

// GitLab is the REST API v4, on gitlab.com or on a company's own server —
// the same API at «<server>/api/v4».
type GitLab struct {
	web    string
	token  string
	client *http.Client
}

// NewGitLab talks to one server with one token.
func NewGitLab(web, token string, client *http.Client) *GitLab {
	if client == nil {
		client = http.DefaultClient
	}
	return &GitLab{web: strings.TrimRight(web, "/"), token: token, client: client}
}

type glMR struct {
	IID          int       `json:"iid"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	WebURL       string    `json:"web_url"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	SHA          string    `json:"sha"`
	State        string    `json:"state"`
	Draft        bool      `json:"draft"`
	UpdatedAt    time.Time `json:"updated_at"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
}

func (m glMR) mr() MR {
	state := StateOpen
	switch m.State {
	case "merged":
		state = StateMerged
	case "closed":
		state = StateClosed
	}
	return MR{
		IID: m.IID, Title: m.Title, Body: m.Description, URL: m.WebURL,
		Source: m.SourceBranch, Target: m.TargetBranch, Head: m.SHA,
		State: state, Draft: m.Draft, Author: m.Author.Username, At: m.UpdatedAt,
	}
}

func (g *GitLab) Me(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := g.do(ctx, http.MethodGet, "/user", nil, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

func (g *GitLab) OpenMR(ctx context.Context, repo, source string) (MR, bool, error) {
	var list []glMR
	q := url.Values{"state": {"opened"}, "source_branch": {source}}
	if err := g.do(ctx, http.MethodGet, project(repo)+"/merge_requests?"+q.Encode(), nil, &list); err != nil {
		return MR{}, false, err
	}
	if len(list) == 0 {
		return MR{}, false, nil
	}
	return list[0].mr(), true, nil
}

func (g *GitLab) CreateMR(ctx context.Context, repo string, mr NewMR) (MR, error) {
	var out glMR
	err := g.do(ctx, http.MethodPost, project(repo)+"/merge_requests", map[string]any{
		"source_branch": mr.Source, "target_branch": mr.Target,
		"title": mr.Title, "description": mr.Body,
	}, &out)
	return out.mr(), err
}

func (g *GitLab) UpdateMRBody(ctx context.Context, repo string, iid int, body string) error {
	return g.do(ctx, http.MethodPut, mrPath(repo, iid), map[string]any{"description": body}, nil)
}

func (g *GitLab) MR(ctx context.Context, repo string, iid int) (MR, error) {
	var out glMR
	err := g.do(ctx, http.MethodGet, mrPath(repo, iid), nil, &out)
	return out.mr(), err
}

func (g *GitLab) ReviewQueue(ctx context.Context, repo, account string) ([]MR, error) {
	var list []glMR
	q := url.Values{"state": {"opened"}, "reviewer_username": {account}, "per_page": {"100"}}
	if err := g.do(ctx, http.MethodGet, project(repo)+"/merge_requests?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	out := make([]MR, 0, len(list))
	for _, m := range list {
		out = append(out, m.mr())
	}
	return out, nil
}

func (g *GitLab) Approve(ctx context.Context, repo string, iid int, head string) error {
	body := map[string]any{}
	if head != "" {
		body["sha"] = head
	}
	err := g.do(ctx, http.MethodPost, mrPath(repo, iid)+"/approve", body, nil)
	// GitLab answers 409 when «sha» is no longer the head: somebody pushed
	// after the review, and what would be approved is not what was read.
	var he *httpError
	if errors.As(err, &he) && he.status == http.StatusConflict {
		return msg.Err("hosting.headMoved", "mr", "!"+strconv.Itoa(iid))
	}
	return err
}

func (g *GitLab) RequestChanges(ctx context.Context, repo string, iid int, remarks string) error {
	// Not approved yet is the ordinary case, and GitLab says so with an
	// error; only being turned away is worth stopping for.
	err := g.do(ctx, http.MethodPost, mrPath(repo, iid)+"/unapprove", nil, nil)
	var he *httpError
	if errors.As(err, &he) && he.status == http.StatusUnauthorized {
		return err
	}
	if strings.TrimSpace(remarks) == "" {
		return nil
	}
	return g.do(ctx, http.MethodPost, mrPath(repo, iid)+"/notes", map[string]any{"body": remarks}, nil)
}

// project is the repository in the form GitLab addresses it: the whole path,
// escaped, standing in for the numeric id.
func project(repo string) string { return "/projects/" + url.PathEscape(repo) }

func mrPath(repo string, iid int) string {
	return project(repo) + "/merge_requests/" + strconv.Itoa(iid)
}

type httpError struct {
	status int
	text   string
}

func (e *httpError) Error() string { return fmt.Sprintf("%d %s", e.status, e.text) }

// requestTimeout bounds one call. The hosting is somebody else's server on the
// other side of a network, and a stage waiting on it must not wait forever.
const requestTimeout = 30 * time.Second

func (g *GitLab) do(ctx context.Context, method, path string, body any, into any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.web+"/api/v4"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return msg.Wrap(err, "hosting.unreachable", "server", g.web)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		he := &httpError{status: resp.StatusCode, text: gitlabMessage(data)}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return msg.Wrap(he, "hosting.unauthorized", "server", g.web)
		case http.StatusForbidden:
			return msg.Wrap(he, "hosting.forbidden", "server", g.web)
		case http.StatusNotFound:
			return msg.Wrap(he, "hosting.notFound", "server", g.web)
		}
		return msg.Wrap(he, "hosting.failed", "server", g.web, "status", strconv.Itoa(resp.StatusCode), "text", he.text)
	}
	if into == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("read the answer of %s: %w", g.web, err)
	}
	return nil
}

// gitlabMessage is what GitLab said went wrong. It says it as «message» —
// a string, a list, or a map of fields to lists — or as «error».
func gitlabMessage(data []byte) string {
	var body struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return strings.TrimSpace(string(data))
	}
	switch m := body.Message.(type) {
	case string:
		return m
	case nil:
		return body.Error
	default:
		out, _ := json.Marshal(m)
		return string(out)
	}
}
