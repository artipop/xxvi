package sources

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/artipop/gandvik/internal/inbox"
	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
	"github.com/artipop/gandvik/internal/secrets"
)

// optionsManifest is a feed whose form offers boards and, for the one chosen,
// its columns.
func optionsManifest() Manifest {
	m := fakeMCPManifest("options")
	m.Name = "boards"
	m.Fields = []Field{
		{Key: "board", Options: &Options{Tool: "list_boards", At: "boards", Value: "{{.title}}"}},
		{Key: "column", Options: &Options{
			Tool: "list_columns", Arguments: map[string]string{"board": "{{.Config.board}}"},
			At: "columns", Value: "{{.name}}", Label: "колонка {{.name}}", DependsOn: []string{"board"},
		}},
	}
	return m
}

func optionsRunner(t *testing.T) *Runner {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(optionsManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "boards.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(dir, demoManifest)
	if problems := catalog.Problems(); len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	return NewRunner(nil, catalog, inbox.NewPipeline(nil, nil, nil), &secrets.Memory{}, nil, nil)
}

// A field's list is asked of the server: a row without a value is left out,
// and the label is what the manifest made of the row.
func TestAFieldIsOfferedTheValuesTheServerHas(t *testing.T) {
	r := optionsRunner(t)
	got, err := r.FieldOptions(context.Background(), model.Source{Plugin: "boards"}, "", "board")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Option{Value: "Работа", Label: "Работа"}) || got[1].Value != "Дом" {
		t.Fatalf("boards: %+v", got)
	}
}

// The columns are those of the board chosen, and of none before one is.
func TestTheColumnsFollowTheBoardChosen(t *testing.T) {
	r := optionsRunner(t)
	got, err := r.FieldOptions(context.Background(), model.Source{Plugin: "boards"}, "", "column")
	if err != nil || got != nil {
		t.Fatalf("without a board: %+v, %v", got, err)
	}
	got, err = r.FieldOptions(context.Background(), model.Source{
		Plugin: "boards", Config: map[string]string{"board": "Работа"},
	}, "", "column")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Value != "Входящие (Работа)" || got[0].Label != "колонка Входящие (Работа)" {
		t.Fatalf("columns: %+v", got)
	}
}

func TestAFieldWithoutAListSaysSo(t *testing.T) {
	r := optionsRunner(t)
	_, err := r.FieldOptions(context.Background(), model.Source{Plugin: "boards"}, "", "nothing")
	if !msg.Is(err, "source.noOptions") {
		t.Fatalf("err: %v", err)
	}
	_, err = r.FieldOptions(context.Background(), model.Source{Plugin: "demo"}, "", "path")
	if !msg.Is(err, "source.unknownPlugin") {
		t.Fatalf("a built-in source has nothing to ask: %v", err)
	}
}

// A source that needs a token is asked nothing without one — and with the one
// typed into the form it is, though none is kept yet.
func TestTheListWantsTheTokenTheSourceNeeds(t *testing.T) {
	m := optionsManifest()
	m.Auth = &AuthSpec{Type: AuthToken}
	raw, _ := json.Marshal(m)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boards.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(nil, LoadCatalog(dir), inbox.NewPipeline(nil, nil, nil), &secrets.Memory{}, nil, nil)
	if _, err := r.FieldOptions(context.Background(), model.Source{Plugin: "boards"}, "", "board"); !msg.Is(err, "source.noToken") {
		t.Fatalf("err: %v", err)
	}
	if got, err := r.FieldOptions(context.Background(), model.Source{Plugin: "boards"}, "typed", "board"); err != nil || len(got) != 2 {
		t.Fatalf("with a token: %+v, %v", got, err)
	}
}

// What the application reads itself is in the list like the rest, and no file
// can take its name.
func TestABuiltinSourceIsInTheCatalogAndNoFileTakesItOver(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.json"), []byte(`{"name":"demo","command":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claims.json"), []byte(`{"name":"claims","kind":"builtin"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(dir, demoManifest)
	m, ok := catalog.Get("demo")
	if !ok || !m.IsBuiltin() || m.Title != "Demo file" {
		t.Fatalf("demo: %+v, %v", m, ok)
	}
	if _, ok := catalog.Get("claims"); ok {
		t.Fatal("a file made itself a built-in source")
	}
	if len(catalog.Problems()) != 2 {
		t.Fatalf("problems: %v", catalog.Problems())
	}
	r := NewRunner(nil, catalog, inbox.NewPipeline(nil, nil, nil), &secrets.Memory{}, nil, nil)
	if r.Owns(model.Source{Plugin: "demo"}) {
		t.Fatal("the runner took a source the application reads itself")
	}
}

func TestAListThatCannotBeAskedForIsRefused(t *testing.T) {
	cases := map[string]Manifest{
		"plugin.optionsNeedMCP": {Name: "p", Command: "x", Fields: []Field{{Key: "a", Options: &Options{Tool: "t", Value: "v"}}}},
		"plugin.optionsNeedString": func() Manifest {
			m := optionsManifest()
			m.Fields[0].Type = FieldBool
			return m
		}(),
		"plugin.optionsIncomplete": func() Manifest {
			m := optionsManifest()
			m.Fields[0].Options.Value = " "
			return m
		}(),
		"plugin.optionsDependsOnItself": func() Manifest {
			m := optionsManifest()
			m.Fields[0].Options.DependsOn = []string{"board"}
			return m
		}(),
	}
	for code, m := range cases {
		if _, err := m.Validate(); !msg.Is(err, code) {
			t.Errorf("%s: %v", code, err)
		}
	}
}

// A token a plugin may take and does not need is not waited for: the Pourquoi Pas?
// bridge on this machine finds its own secret, and a server is given one.
func TestAnOptionalTokenIsNotWaitedFor(t *testing.T) {
	m := optionsManifest()
	m.Auth = &AuthSpec{Type: AuthToken, Optional: true}
	if m.NeedsToken() || !m.TakesToken() {
		t.Fatalf("needs %v, takes %v", m.NeedsToken(), m.TakesToken())
	}
	raw, _ := json.Marshal(m)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boards.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(nil, LoadCatalog(dir), inbox.NewPipeline(nil, nil, nil), &secrets.Memory{}, nil, nil)
	if got, err := r.FieldOptions(context.Background(), model.Source{Plugin: "boards"}, "", "board"); err != nil || len(got) != 2 {
		t.Fatalf("without a token: %+v, %v", got, err)
	}
}
