package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artipop/gandvik/internal/inbox"
	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/secrets"
	"github.com/artipop/gandvik/internal/sources/plugin"
	"github.com/artipop/gandvik/internal/store"
	"github.com/artipop/gandvik/sources/protocol"
	"github.com/artipop/gandvik/sources/sdk"
)

// The plugin these tests run is this test binary again, serving the SDK: a
// real process on real stdio, the way a plugin somebody wrote would run.
const pluginModeEnv = "GANDVIK_TEST_SOURCE_PLUGIN"

func servePlugin(mode string) {
	if mode == "dies" {
		fmt.Fprintln(os.Stderr, "Pourquoi Pas? не запущен: откройте его")
		os.Exit(1)
	}
	sdk.Serve(sdk.Source{
		Capabilities: protocol.Capabilities{Poll: true},
		Poll: func(_ context.Context, req sdk.PollRequest) (sdk.PollResult, error) {
			if mode == "bad-config" {
				return sdk.PollResult{}, sdk.BadConfig("board", "no board %q", req.Config["board"])
			}
			return sdk.PollResult{Items: []sdk.Item{{
				ExternalID: "1", Version: "v1",
				Title: "Задача с доски " + req.Config["board"],
				Body:  "токен: " + req.Credentials.AccessToken,
			}}}, nil
		},
	})
}

type fixture struct {
	store  *store.Store
	runner *Runner
	tokens *secrets.Memory
}

func setup(t *testing.T, manifest string) fixture {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fake.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(dir, demoManifest)
	if problems := catalog.Problems(); len(problems) > 0 {
		t.Fatalf("manifests: %v", problems)
	}
	tokens := &secrets.Memory{}
	r := NewRunner(st, catalog, inbox.NewPipeline(st, nil, nil), tokens, nil, nil)
	t.Cleanup(func() { r.Stop(5 * time.Second) })
	return fixture{store: st, runner: r, tokens: tokens}
}

func pluginManifest(mode string, auth bool) string {
	m := map[string]any{
		"name": "fake", "command": os.Args[0], "args": []string{"-test.run=TestNothing"},
		"env":    map[string]string{pluginModeEnv: mode},
		"fields": []map[string]string{{"key": "board", "default": "Работа"}},
	}
	if auth {
		m["auth"] = map[string]string{"type": "token"}
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func (f fixture) addSource(t *testing.T, config map[string]string) {
	t.Helper()
	if _, err := f.store.SaveSource(model.Source{Name: "Трекер", Plugin: "fake", Enabled: true, Config: config}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) inbox(t *testing.T) []model.Card {
	t.Helper()
	cards, err := f.store.CardsInState(model.StateInbox)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("never happened: %s", what)
}

func (f fixture) status(name string) Status {
	for _, s := range f.runner.Statuses() {
		if s.Source == name {
			return s
		}
	}
	return Status{Source: name, State: StateOff}
}

// The whole path: a plugin process is started for the source, its answer goes
// through the inbox pipeline, and asking again does not make a second card.
func TestAPluginSourceFilesWhatItBringsIntoTheInbox(t *testing.T) {
	f := setup(t, pluginManifest("ok", false))
	f.addSource(t, nil)
	f.runner.Start()

	eventually(t, "the item became a card", func() bool { return len(f.inbox(t)) == 1 })
	card := f.inbox(t)[0]
	// The field nobody filled in means what the form showed: its default.
	if card.Source != "Трекер" || card.Title != "Задача с доски Работа" {
		t.Fatalf("card: %+v", card)
	}

	polled := f.status("Трекер").LastPoll
	if err := f.runner.PollNow("Трекер"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second poll", func() bool {
		last := f.status("Трекер").LastPoll
		return last != nil && polled != nil && last.After(*polled)
	})
	if n := len(f.inbox(t)); n != 1 {
		t.Fatalf("the same item came back as %d cards", n)
	}
}

// A source that cannot work without a token is not started without one — it
// says so and waits — and starts the moment the token is given, with it.
func TestASourceWaitsForItsTokenAndStartsWithIt(t *testing.T) {
	f := setup(t, pluginManifest("ok", true))
	f.addSource(t, map[string]string{"board": "Дом"})
	f.runner.Start()

	eventually(t, "asked for a token", func() bool { return f.status("Трекер").State == StateNeedsReauth })
	if len(f.inbox(t)) != 0 {
		t.Fatal("a source without its token brought something")
	}

	if err := f.runner.SetToken("Трекер", "t0k"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the item became a card", func() bool { return len(f.inbox(t)) == 1 })
	if body := f.inbox(t)[0].Body; body != "токен: t0k" {
		t.Fatalf("the plugin was not given the token: %q", body)
	}
	if !f.runner.HasToken("Трекер") {
		t.Fatal("the token is not kept")
	}
	f.runner.Forget("Трекер")
	if f.runner.HasToken("Трекер") {
		t.Fatal("a deleted source's token outlived it")
	}
}

// A refused setting names the field, so the form can point at it.
func TestABadSettingIsReportedAgainstItsField(t *testing.T) {
	f := setup(t, pluginManifest("bad-config", false))
	f.addSource(t, nil)
	f.runner.Start()

	eventually(t, "the refusal reached the status", func() bool { return f.status("Трекер").State == StateError })
	if s := f.status("Трекер"); s.Field != "board" || s.Error == nil {
		t.Fatalf("status: %+v", s)
	}
}

// A plugin is somebody else's code: it does not get to pick where on this
// machine a card works.
func TestAPluginCannotChooseWhereACardWorks(t *testing.T) {
	f := setup(t, pluginManifest("ok", false))
	f.addSource(t, nil)
	var polls atomic.Int32
	f.runner.dial = func(context.Context, model.Source, Manifest, plugin.Credentials, plugin.Handler) (conn, error) {
		return fakeConn{poll: func() plugin.PollResult {
			polls.Add(1)
			raw, _ := json.Marshal(model.Item{ExternalID: "x", Title: "Чужой проект",
				Workspace: &model.ItemWorkspace{Project: "secret", WorkMode: "inPlace"}})
			return plugin.PollResult{Items: []json.RawMessage{raw}}
		}}, nil
	}
	f.runner.Start()

	eventually(t, "the item became a card", func() bool { return len(f.inbox(t)) == 1 })
	if card := f.inbox(t)[0]; card.Project != "" || card.WorkMode != "" {
		t.Fatalf("the plugin chose the card's workspace: %+v", card)
	}
}

// Switching a source off stops it, and its status says so.
func TestADisabledSourceStops(t *testing.T) {
	f := setup(t, pluginManifest("ok", false))
	f.addSource(t, nil)
	f.runner.Start()
	eventually(t, "running", func() bool { return f.status("Трекер").State == StateRunning })

	src, _ := f.store.Source("Трекер")
	src.Enabled = false
	if _, err := f.store.SaveSource(src); err != nil {
		t.Fatal(err)
	}
	f.runner.Restart("Трекер")
	if s := f.status("Трекер"); s.State != StateOff {
		t.Fatalf("status: %+v", s)
	}
	f.runner.mu.Lock()
	_, running := f.runner.running["Трекер"]
	f.runner.mu.Unlock()
	if running {
		t.Fatal("the loop is still registered")
	}
}

type fakeConn struct{ poll func() plugin.PollResult }

func (c fakeConn) Capabilities() plugin.Capabilities { return plugin.Capabilities{Poll: true} }
func (c fakeConn) Poll(context.Context, string) (plugin.PollResult, error) {
	return c.poll(), nil
}
func (fakeConn) Close() {}

// A file in the data directory wins over a shipped manifest of the same name,
// and one by a built-in source's name is refused rather than taking it over.
func TestTheCatalogPrefersFilesAndKeepsBuiltInNames(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("kaiten.json", `{"name": "kaiten", "title": "Мой Kaiten", "command": "/bin/true"}`)
	write("demo.json", `{"name": "demo", "command": "/bin/true"}`)
	write("broken.json", `{"name": `)

	c := LoadCatalog(dir, demoManifest)
	if m, ok := c.Get("kaiten"); !ok || m.Title != "Мой Kaiten" {
		t.Fatalf("kaiten: %+v", m)
	}
	if m, ok := c.Get("demo"); !ok || !m.IsBuiltin() {
		t.Fatalf("a manifest took over the demo source: %+v", m)
	}
	if n := len(c.Problems()); n != 2 {
		t.Fatalf("expected the reserved name and the broken file reported, got %d: %v", n, c.Problems())
	}
	if _, ok := LoadCatalog("").Get("kaiten"); !ok {
		t.Fatal("the shipped Kaiten manifest is missing")
	}
}

// The shipped Kaiten manifest end to end: the site typed into the form and the
// token in the keychain reach `gandvik mcp kaiten`, and its answer lands in the
// inbox as cards that link back to Kaiten.
func TestTheShippedKaitenSourceFillsTheInbox(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/latest/users/current":
			_, _ = w.Write([]byte(`{"id": 7}`))
		case "/api/latest/cards":
			if r.URL.Query().Get("responsible_id") == "7" {
				_, _ = w.Write([]byte(`[{"id": 41, "title": "Починить логин", "updated": "u1", "description": "падает"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer api.Close()

	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.SaveSource(model.Source{Name: "Kaiten", Plugin: "kaiten", Enabled: true,
		Config: map[string]string{"site": api.URL}}); err != nil {
		t.Fatal(err)
	}
	tokens := &secrets.Memory{}
	_ = tokens.SetToken(TokenKey("Kaiten"), "t0k")
	r := NewRunner(st, LoadCatalog(""), inbox.NewPipeline(st, nil, nil), tokens, nil, nil)
	r.Start()
	defer r.Stop(5 * time.Second)

	f := fixture{store: st, runner: r}
	eventually(t, "the Kaiten card reached the inbox", func() bool { return len(f.inbox(t)) == 1 })
	card := f.inbox(t)[0]
	if card.Title != "Починить логин" || card.Body != "падает" || card.URL != api.URL+"/ticket/41" || card.ExternalID != "41" {
		t.Fatalf("card: %+v", card)
	}
}

// The shipped Pourquoi Pas? manifest: the form's fields reach the bridge's command
// line, its environment and the tool's arguments, and a row of the feed
// becomes an item whose version is the card's last change.
func TestTheShippedPourquoiPasManifestMapsTheFeed(t *testing.T) {
	m, ok := LoadCatalog("").Get("pourquoipas")
	if !ok {
		t.Fatal("the shipped Pourquoi Pas? manifest is missing")
	}
	src := m.WithDefaults(model.Source{Name: "Доска", Config: map[string]string{
		"board": "Работа", "column": "Сегодня", "where": "server", "url": "https://board.example",
	}})
	argv, err := m.Argv(src)
	if err != nil || argv[0] != "/Applications/pourquoipas.app/Contents/MacOS/pourquoipas" || argv[1] != "mcp" || argv[2] != "feed" {
		t.Fatalf("argv: %v, %v", argv, err)
	}
	env, _ := m.RenderEnv(src)
	if env["PP_URL"] != "https://board.example" {
		t.Fatalf("env: %v", env)
	}
	args, _ := m.MCP.renderArguments(src)
	if args["board"] != "Работа" || args["column"] != "Сегодня" {
		t.Fatalf("arguments: %v", args)
	}

	row := map[string]any{"id": "c1", "title": "Починить логин", "updated": "1700", "column": "Сегодня",
		"url": "https://board.example/team/0/b1/0/c1", "properties": map[string]any{"Проект": "gandvik"}}
	raw, err := m.MCP.Item.render(row)
	if err != nil {
		t.Fatal(err)
	}
	var item model.Item
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	if item.ExternalID != "c1" || item.Version != "1700" || item.Body != "Проект: gandvik" ||
		item.URL != "https://board.example/team/0/b1/0/c1" || len(item.Labels) != 1 || item.Labels[0] != "Сегодня" {
		t.Fatalf("item: %+v", item)
	}

	// On this machine, with no server address, nothing tells the bridge where
	// to go but the file the running app leaves.
	local := m.WithDefaults(model.Source{Config: map[string]string{"board": "Работа"}})
	if env, _ := m.RenderEnv(local); env["PP_URL"] != "" {
		t.Fatalf("an empty address reached the bridge: %v", env)
	}
}

// A plugin that dies before it says anything has said why on stderr, and that
// is what the source's status shows.
func TestAPluginThatDiesSaysWhy(t *testing.T) {
	f := setup(t, pluginManifest("dies", false))
	f.addSource(t, nil)
	f.runner.Start()

	eventually(t, "the failure reached the status", func() bool { return f.status("Трекер").State == StateError })
	if e := f.status("Трекер").Error; e == nil || !strings.Contains(e.Arg("stderr"), "Pourquoi Pas? не запущен") {
		t.Fatalf("status error: %+v", e)
	}
}

// A source whose plugin did not start is tried again, and «read now» does not
// wait out the pause: the application it reads was probably just not open yet.
func TestASourceThatDidNotStartIsTriedAgain(t *testing.T) {
	f := setup(t, pluginManifest("ok", false))
	f.addSource(t, nil)
	var dials atomic.Int32
	f.runner.dial = func(context.Context, model.Source, Manifest, plugin.Credentials, plugin.Handler) (conn, error) {
		if dials.Add(1) == 1 {
			return nil, errors.New("connection refused")
		}
		return fakeConn{poll: func() plugin.PollResult {
			raw, _ := json.Marshal(model.Item{ExternalID: "1", Title: "Пришло"})
			return plugin.PollResult{Items: []json.RawMessage{raw}}
		}}, nil
	}
	f.runner.Start()
	eventually(t, "the first start failed", func() bool { return f.status("Трекер").State == StateError })

	if err := f.runner.PollNow("Трекер"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second start brought the item", func() bool { return len(f.inbox(t)) == 1 })
}

// demoManifest stands for a source the application reads itself, as the app
// describes it to the catalog.
var demoManifest = Manifest{Name: "demo", Title: "Demo file"}
