// Package gitlabtest is a GitLab that answers the few requests this
// application makes, kept in memory: enough to walk an MR from opened to
// approved in a test without a network or an account.
package gitlabtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MR is one merge request as the fake keeps it.
type MR struct {
	Repo      string
	IID       int
	Title     string
	Body      string
	Source    string
	Target    string
	SHA       string
	State     string // opened | merged | closed
	Author    string
	Reviewers []string
	Approved  []string
	Notes     []string
	UpdatedAt time.Time
}

// Server is the fake. Token is the one it accepts; User is who that is.
type Server struct {
	*httptest.Server
	Token string
	User  string

	mu  sync.Mutex
	mrs []*MR
}

func New() *Server {
	s := &Server{Token: "glpat-test", User: "me"}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Add puts an MR on the server and returns it; IID is assigned when zero.
func (s *Server) Add(mr MR) *MR {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mr.IID == 0 {
		mr.IID = s.nextIID(mr.Repo)
	}
	if mr.State == "" {
		mr.State = "opened"
	}
	if mr.UpdatedAt.IsZero() {
		mr.UpdatedAt = time.Now().UTC()
	}
	m := mr
	s.mrs = append(s.mrs, &m)
	return &m
}

// Change edits an MR under the lock, as another person would on the server.
func (s *Server) Change(repo string, iid int, fn func(*MR)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.find(repo, iid); m != nil {
		fn(m)
		m.UpdatedAt = time.Now().UTC()
	}
}

// Get is a copy of an MR as it now stands.
func (s *Server) Get(repo string, iid int) (MR, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.find(repo, iid); m != nil {
		return *m, true
	}
	return MR{}, false
}

// All is a copy of every MR.
func (s *Server) All() []MR {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MR, 0, len(s.mrs))
	for _, m := range s.mrs {
		out = append(out, *m)
	}
	return out
}

func (s *Server) nextIID(repo string) int {
	n := 0
	for _, m := range s.mrs {
		if m.Repo == repo && m.IID > n {
			n = m.IID
		}
	}
	return n + 1
}

func (s *Server) find(repo string, iid int) *MR {
	for _, m := range s.mrs {
		if m.Repo == repo && m.IID == iid {
			return m
		}
	}
	return nil
}

func (s *Server) json(m *MR) map[string]any {
	return map[string]any{
		"iid": m.IID, "title": m.Title, "description": m.Body,
		"web_url":       s.URL + "/" + m.Repo + "/-/merge_requests/" + strconv.Itoa(m.IID),
		"source_branch": m.Source, "target_branch": m.Target, "sha": m.SHA,
		"state": m.State, "updated_at": m.UpdatedAt.Format(time.RFC3339),
		"author": map[string]any{"username": m.Author},
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("PRIVATE-TOKEN") != s.Token {
		reply(w, http.StatusUnauthorized, map[string]any{"message": "401 Unauthorized"})
		return
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")
	if path == "/user" {
		reply(w, http.StatusOK, map[string]any{"username": s.User})
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "projects" || parts[2] != "merge_requests" {
		reply(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
		return
	}
	repo, _ := url.PathUnescape(parts[1])
	var body map[string]string
	if r.Body != nil {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		body = map[string]string{}
		for k, v := range raw {
			if str, ok := v.(string); ok {
				body[k] = str
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(parts) == 3 {
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			var out []map[string]any
			for _, m := range s.mrs {
				if m.Repo != repo {
					continue
				}
				if st := q.Get("state"); st != "" && st != m.State {
					continue
				}
				if b := q.Get("source_branch"); b != "" && b != m.Source {
					continue
				}
				if who := q.Get("reviewer_username"); who != "" && !contains(m.Reviewers, who) {
					continue
				}
				out = append(out, s.json(m))
			}
			if out == nil {
				out = []map[string]any{}
			}
			reply(w, http.StatusOK, out)
		case http.MethodPost:
			m := &MR{
				Repo: repo, IID: s.nextIID(repo), Title: body["title"], Body: body["description"],
				Source: body["source_branch"], Target: body["target_branch"], State: "opened",
				Author: s.User, UpdatedAt: time.Now().UTC(),
			}
			s.mrs = append(s.mrs, m)
			reply(w, http.StatusCreated, s.json(m))
		}
		return
	}

	iid, _ := strconv.Atoi(parts[3])
	m := s.find(repo, iid)
	if m == nil {
		reply(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
		return
	}
	if len(parts) == 4 {
		switch r.Method {
		case http.MethodGet:
			reply(w, http.StatusOK, s.json(m))
		case http.MethodPut:
			if d, ok := body["description"]; ok {
				m.Body = d
			}
			reply(w, http.StatusOK, s.json(m))
		}
		return
	}
	switch parts[4] {
	case "approve":
		if sha := body["sha"]; sha != "" && sha != m.SHA {
			reply(w, http.StatusConflict, map[string]any{"message": "SHA does not match HEAD of source branch"})
			return
		}
		if !contains(m.Approved, s.User) {
			m.Approved = append(m.Approved, s.User)
		}
		reply(w, http.StatusCreated, map[string]any{})
	case "unapprove":
		if !contains(m.Approved, s.User) {
			reply(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
			return
		}
		m.Approved = remove(m.Approved, s.User)
		reply(w, http.StatusCreated, map[string]any{})
	case "notes":
		m.Notes = append(m.Notes, body["body"])
		reply(w, http.StatusCreated, map[string]any{})
	default:
		reply(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func remove(list []string, s string) []string {
	var out []string
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}
