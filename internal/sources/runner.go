package sources

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/artipop/xxvi/internal/inbox"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/secrets"
	"github.com/artipop/xxvi/internal/sources/plugin"
	"github.com/artipop/xxvi/internal/store"
)

// Running a plugin: one goroutine per source, started with the app and stopped
// with it. Polling is on a schedule because a laptop behind NAT has nowhere for
// a webhook to land; a plugin that would rather push says so in its
// capabilities and is left to talk when it has something.

// Schedule bounds. A source that has just failed is not worth asking again at
// once, and one that keeps failing is not worth asking often.
const (
	defaultInterval = 5 * time.Minute
	maxBackoff      = 30 * time.Minute
	pollTimeout     = 2 * time.Minute
)

// minInterval is the floor under a source's schedule, so a plugin that answers
// instantly is not asked in a tight loop. A variable so a test can watch
// several polls without waiting for each.
var minInterval = 30 * time.Second

// EventStatus says a source's status changed.
const EventStatus = "sourceStatus"

// State is what a source is doing.
type State string

const (
	StateOff         State = "off"
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateNeedsReauth State = "needs-reauth"
	StateError       State = "error"
)

// Status is one source's state, as the source list shows it: a silent
// integration is only debuggable if it says what it last did.
type Status struct {
	Source   string     `json:"source"`
	State    State      `json:"state"`
	LastPoll *time.Time `json:"lastPoll,omitempty"`
	LastItem *time.Time `json:"lastItem,omitempty"`
	Error    *msg.Msg   `json:"error,omitempty"`
	// Field is the setting a plugin refused, so the form can point at it.
	Field string `json:"field,omitempty"`
}

// Ingester files what a source brought: the inbox pipeline.
type Ingester interface {
	Ingest(source string, items []model.Item) ([]inbox.Result, error)
}

// TokenKey is where a source's token is kept.
func TokenKey(source string) string { return "source:" + source }

// conn is the part of a plugin the runner uses — an interface so a test can
// drive the runner without a process.
type conn interface {
	Capabilities() plugin.Capabilities
	Poll(ctx context.Context, cursor string) (plugin.PollResult, error)
	Close()
}

type dialer func(ctx context.Context, src model.Source, manifest Manifest, cred plugin.Credentials, handler plugin.Handler) (conn, error)

// dialPlugin starts the manifest's command. Which protocol it speaks is the
// manifest's business and nobody else's: past this function nothing can tell
// an MCP server from a plugin written against sources/protocol.
func dialPlugin(ctx context.Context, src model.Source, manifest Manifest, cred plugin.Credentials, handler plugin.Handler) (conn, error) {
	if manifest.IsMCP() {
		return dialMCP(ctx, src, manifest, cred)
	}
	env, err := manifest.RenderEnv(src)
	if err != nil {
		return nil, err
	}
	argv, err := manifest.Argv(src)
	if err != nil {
		return nil, err
	}
	return plugin.Dial(ctx, plugin.Spec{
		Command:     argv,
		Dir:         manifest.Dir,
		Env:         envList(env),
		Source:      plugin.SourceInfo{Name: src.Name, Config: src.Config},
		Credentials: cred,
		Host:        plugin.HostInfo{Name: "XXVI"},
	}, handler)
}

// Runner keeps every enabled plugin source running.
type Runner struct {
	store   *store.Store
	catalog *Catalog
	ingest  Ingester
	tokens  secrets.Store
	emit    func(event string, payload any)
	log     *slog.Logger
	dial    dialer

	mu      sync.Mutex
	root    context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	running map[string]*loopHandle
	status  map[string]*Status
}

type loopHandle struct {
	stop context.CancelFunc
	// kick asks for a poll now. Buffered by one: a second «read now» while the
	// first is still waiting to be seen asks for nothing more.
	kick chan struct{}
}

func NewRunner(st *store.Store, catalog *Catalog, ingest Ingester, tokens secrets.Store, emit func(string, any), log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	if tokens == nil {
		tokens = &secrets.Memory{}
	}
	return &Runner{
		store: st, catalog: catalog, ingest: ingest, tokens: tokens, emit: emit, log: log,
		dial: dialPlugin, running: map[string]*loopHandle{}, status: map[string]*Status{},
	}
}

// Catalog is the manifests this runner starts sources from.
func (r *Runner) Catalog() *Catalog { return r.catalog }

// Owns reports whether a source is one of the runner's: it names a plugin the
// catalog has. The demo file and the review queue are read elsewhere.
func (r *Runner) Owns(src model.Source) bool {
	_, ok := r.catalog.Get(src.Plugin)
	return src.Plugin != "" && ok
}

// Start brings up every enabled source the runner owns.
func (r *Runner) Start() {
	r.mu.Lock()
	if r.root != nil {
		r.mu.Unlock()
		return
	}
	r.root, r.stop = context.WithCancel(context.Background())
	r.mu.Unlock()

	sources, err := r.store.Sources()
	if err != nil {
		r.log.Error("could not read sources", "err", err)
		return
	}
	for _, src := range sources {
		if src.Enabled && r.Owns(src) {
			r.run(src)
		}
	}
}

// Stop ends every plugin and waits for them, as long as grace allows.
func (r *Runner) Stop(grace time.Duration) {
	r.mu.Lock()
	stop := r.stop
	r.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
	}
}

// Restart brings one source up again under whatever the registry now says: it
// was just added, edited, given a token. A source that is gone, off or not a
// plugin is stopped instead. Adding a source and being told to restart the app
// to see it work is not a feature that works.
func (r *Runner) Restart(name string) {
	src, err := r.store.Source(name)
	if err != nil || !src.Enabled || !r.Owns(src) {
		r.stopSource(name)
		r.setStatus(name, func(s *Status) { *s = Status{Source: name, State: StateOff} })
		return
	}
	r.run(src)
}

// PollNow asks a running source for what is new without waiting out its
// interval. What it brings arrives the way everything does, through the inbox.
func (r *Runner) PollNow(name string) error {
	r.mu.Lock()
	h, ok := r.running[name]
	r.mu.Unlock()
	if !ok {
		// Stopped after a failure it could not get over: asking again is
		// starting again.
		r.Restart(name)
		return nil
	}
	select {
	case h.kick <- struct{}{}:
	default:
	}
	return nil
}

// SetToken keeps a source's token and starts the source again with it.
func (r *Runner) SetToken(name, token string) error {
	var err error
	if token == "" {
		err = r.tokens.DeleteToken(TokenKey(name))
	} else {
		err = r.tokens.SetToken(TokenKey(name), token)
	}
	if err != nil {
		return err
	}
	r.Restart(name)
	return nil
}

// HasToken reports whether a source has a token kept, without saying what it
// is: the form shows that one is there, never the token.
func (r *Runner) HasToken(name string) bool {
	token, err := r.tokens.Token(TokenKey(name))
	return err == nil && token != ""
}

// Forget stops a deleted source and drops its token.
func (r *Runner) Forget(name string) {
	r.stopSource(name)
	_ = r.tokens.DeleteToken(TokenKey(name))
	r.mu.Lock()
	delete(r.status, name)
	r.mu.Unlock()
}

// Statuses is the state of every source the runner has heard of.
func (r *Runner) Statuses() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.status))
	for _, s := range r.status {
		out = append(out, *s)
	}
	return out
}

func (r *Runner) run(src model.Source) {
	r.mu.Lock()
	if r.root == nil {
		// Not started yet: Start brings this one up with the rest.
		r.mu.Unlock()
		return
	}
	if h, ok := r.running[src.Name]; ok {
		h.stop()
	}
	ctx, stop := context.WithCancel(r.root)
	h := &loopHandle{stop: stop, kick: make(chan struct{}, 1)}
	r.running[src.Name] = h
	r.mu.Unlock()

	r.setStatus(src.Name, func(s *Status) {
		s.State, s.Error, s.Field = StateStarting, nil, ""
	})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.release(src.Name, h)
		r.loop(ctx, src, h.kick)
	}()
}

// release forgets a finished loop, unless a newer one has already taken its
// place — a restart stops the old loop, and the old loop ending must not
// unregister the new one.
func (r *Runner) release(name string, h *loopHandle) {
	h.stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[name] == h {
		delete(r.running, name)
	}
}

func (r *Runner) stopSource(name string) {
	r.mu.Lock()
	h, ok := r.running[name]
	delete(r.running, name)
	r.mu.Unlock()
	if ok {
		h.stop()
	}
}

func (r *Runner) loop(ctx context.Context, src model.Source, kick <-chan struct{}) {
	manifest, ok := r.catalog.Get(src.Plugin)
	if !ok {
		r.fail(src.Name, msg.Err("source.unknownPlugin", "source", src.Name, "plugin", src.Plugin))
		return
	}
	src = manifest.WithDefaults(src)

	var cred plugin.Credentials
	if token, err := r.tokens.Token(TokenKey(src.Name)); err == nil {
		cred.AccessToken = token
	}
	if manifest.NeedsToken() && cred.AccessToken == "" {
		r.setStatus(src.Name, func(s *Status) {
			s.State = StateNeedsReauth
			e := msg.New("source.noToken", "source", src.Name)
			s.Error = &e
		})
		return
	}

	handler := &pluginHandler{runner: r, source: src.Name}
	// A plugin that did not start, or died, is started again after a pause
	// rather than given up on: the likeliest reason is another application
	// that is not open yet — XCIII, for one — and a source that stays broken
	// after it opens is one somebody has to notice and restart by hand.
	wait := time.Duration(0)
	for {
		if wait > 0 && !sleep(ctx, kick, wait) {
			return
		}
		client, err := r.dial(ctx, src, manifest, cred, handler)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.fail(src.Name, err)
			wait = backoff(wait, src)
			continue
		}
		redial := r.serve(ctx, src, client, handler, kick)
		client.Close()
		if !redial {
			return
		}
		wait = backoff(wait, src)
	}
}

// serve polls one running plugin until the source is stopped, it needs a
// person, or the plugin is gone — and reports whether to start it again.
func (r *Runner) serve(ctx context.Context, src model.Source, client conn, handler *pluginHandler, kick <-chan struct{}) bool {
	caps := client.Capabilities()
	r.setStatus(src.Name, func(s *Status) {
		s.State, s.Error, s.Field = StateRunning, nil, ""
	})
	if !caps.Poll {
		// A plugin that only pushes talks when it has something; the loop only
		// keeps it alive until the app stops.
		<-ctx.Done()
		return false
	}

	// The first poll is immediate: a source that was just switched on has been
	// waiting for whoever switched it on, not for the schedule.
	wait := time.Duration(0)
	for {
		if wait > 0 && !sleep(ctx, kick, wait) {
			return false
		} else if ctx.Err() != nil {
			return false
		}

		pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
		result, err := client.Poll(pollCtx, handler.Cursor())
		cancel()
		if ctx.Err() != nil {
			return false
		}
		if err != nil {
			if gone(err) {
				r.fail(src.Name, err)
				return true
			}
			next, stop := r.afterPollError(src, err, wait)
			if stop {
				return false
			}
			wait = next
			continue
		}
		if caps.Cursor {
			handler.SetCursor(result.Cursor)
		}
		r.setStatus(src.Name, func(s *Status) {
			now := time.Now()
			s.LastPoll, s.State, s.Error, s.Field = &now, StateRunning, nil, ""
		})
		r.deliver(src.Name, result.Items)

		wait = interval(src)
		if result.RetryAfterSeconds > 0 {
			// The service said when to come back, and that beats our schedule.
			wait = time.Duration(result.RetryAfterSeconds) * time.Second
		}
	}
}

// gone reports a plugin whose process is no longer there to ask.
func gone(err error) bool {
	return msg.Is(err, "plugin.exited") || msg.Is(err, "plugin.stopped")
}

// sleep waits out a pause, cut short by «read now». False when the source is
// being stopped.
func sleep(ctx context.Context, kick <-chan struct{}, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-kick:
		return true
	case <-time.After(d):
		return true
	}
}

// backoff doubles a pause, starting from the source's schedule and never
// beyond maxBackoff.
func backoff(wait time.Duration, src model.Source) time.Duration {
	next := wait * 2
	if next < interval(src) {
		next = interval(src)
	}
	if next > maxBackoff {
		next = maxBackoff
	}
	return next
}

// afterPollError decides what a failed poll costs: how long to wait, and
// whether to stop until a person does something.
func (r *Runner) afterPollError(src model.Source, err error, wait time.Duration) (time.Duration, bool) {
	var perr *plugin.Error
	errors.As(err, &perr)
	switch {
	case perr != nil && perr.NeedsReauth():
		// Asking again with a dead token is noise the service can see.
		r.setStatus(src.Name, func(s *Status) {
			s.State = StateNeedsReauth
			e := msg.Of(err)
			s.Error = &e
		})
		return 0, true
	case perr != nil && !perr.Retryable():
		// A bad field cannot fix itself either, but the plugin is alive, so the
		// loop waits rather than exits: the setting may be corrected.
		r.setStatus(src.Name, func(s *Status) {
			s.State = StateError
			e := msg.Of(err)
			s.Error, s.Field = &e, perr.Field
		})
		return maxBackoff, false
	}
	r.log.Warn("source poll failed", "source", src.Name, "err", err)
	r.setStatus(src.Name, func(s *Status) {
		s.State = StateError
		e := msg.Of(err)
		s.Error = &e
	})
	// The backoff doubles the schedule, not the wait that failed: the first
	// poll waits nothing, and doubling nothing is still nothing.
	return backoff(wait, src), false
}

// deliver turns what a plugin sent into items and files them. One payload that
// does not parse costs itself, not the batch.
func (r *Runner) deliver(source string, raw []json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	items := make([]model.Item, 0, len(raw))
	for _, payload := range raw {
		var item model.Item
		if err := json.Unmarshal(payload, &item); err != nil {
			r.log.Warn("source item not parsed", "source", source, "err", err)
			continue
		}
		// Where a card works is a person's choice, or a built-in source's that
		// knows better (hosting's review queue). A plugin is somebody else's
		// code and does not get to pick a repository on this machine.
		item.Workspace = nil
		if len(item.Raw) == 0 {
			item.Raw = payload
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return
	}
	if _, err := r.ingest.Ingest(source, items); err != nil {
		r.log.Warn("source items not filed", "source", source, "err", err)
		return
	}
	r.setStatus(source, func(s *Status) {
		now := time.Now()
		s.LastItem = &now
	})
}

func (r *Runner) fail(source string, err error) {
	r.log.Warn("source did not start", "source", source, "err", err)
	r.setStatus(source, func(s *Status) {
		s.State = StateError
		e := msg.Of(err)
		s.Error = &e
	})
}

func (r *Runner) setStatus(source string, apply func(*Status)) {
	r.mu.Lock()
	current, ok := r.status[source]
	if !ok {
		current = &Status{Source: source, State: StateOff}
		r.status[source] = current
	}
	apply(current)
	snapshot := *current
	r.mu.Unlock()
	if r.emit != nil {
		r.emit(EventStatus, snapshot)
	}
}

func interval(src model.Source) time.Duration {
	if src.IntervalSeconds <= 0 {
		return defaultInterval
	}
	d := time.Duration(src.IntervalSeconds) * time.Second
	if d < minInterval {
		return minInterval
	}
	return d
}

// pluginHandler is what a plugin says without being asked. It holds the cursor
// too: a plugin that both polls and pushes moves it from two goroutines, and a
// shared variable there would be a source quietly re-reading or skipping part
// of its history.
type pluginHandler struct {
	runner *Runner
	source string

	mu     sync.Mutex
	cursor string
}

func (h *pluginHandler) Cursor() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cursor
}

func (h *pluginHandler) SetCursor(cursor string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cursor = cursor
}

func (h *pluginHandler) Items(items []json.RawMessage, cursor string) {
	if cursor != "" {
		h.SetCursor(cursor)
	}
	h.runner.deliver(h.source, items)
}

func (h *pluginHandler) Log(level, message string) {
	h.runner.log.Info("source plugin says", "source", h.source, "level", level, "message", message)
}

func (h *pluginHandler) NeedsReauth(reason string) {
	h.runner.setStatus(h.source, func(s *Status) {
		s.State = StateNeedsReauth
		e := msg.New("source.needsReauth", "source", h.source, "reason", reason)
		s.Error = &e
	})
}
