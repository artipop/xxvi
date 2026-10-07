package sources

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/sources/kaiten"
	"github.com/artipop/xxvi/internal/sources/plugin"
)

// The MCP server these tests talk to is this test binary, re-invoked — the same
// arrangement internal/sources/plugin uses, and for the same reason: what
// actually breaks is a real process with real stdio, and proving it must not
// depend on a toolchain being installed to build a second program.

const mcpModeEnv = "XXVI_TEST_MCP"

func TestMain(m *testing.M) {
	if mode := os.Getenv(mcpModeEnv); mode != "" {
		serveFakeMCP(mode)
		return
	}
	if mode := os.Getenv(pluginModeEnv); mode != "" {
		servePlugin(mode)
		return
	}
	// The shipped Kaiten manifest starts `$self mcp kaiten`, and in a test $self
	// is this binary.
	if len(os.Args) > 2 && os.Args[1] == "mcp" && os.Args[2] == kaiten.ServerName {
		if err := kaiten.ServeStdio(context.Background(), kaiten.Config{
			Site: os.Getenv(kaiten.EnvSite), Token: os.Getenv(kaiten.EnvToken),
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

// serveFakeMCP is an MCP server in twenty lines: enough of the protocol to be
// dialled, listed and called.
func serveFakeMCP(mode string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil || msg.ID == nil {
			continue
		}
		reply := func(result any) {
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": result})
		}
		switch msg.Method {
		case "initialize":
			reply(map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "0.1.0"},
			})
		case "tools/list":
			if mode == "no-tool" {
				reply(map[string]any{"tools": []any{map[string]any{"name": "get_card"}}})
				continue
			}
			if mode == "options" {
				reply(map[string]any{"tools": []any{map[string]any{"name": "list_boards"}, map[string]any{"name": "list_columns"}}})
				continue
			}
			reply(map[string]any{"tools": []any{map[string]any{"name": "list_my_cards"}}})
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			switch mode {
			case "options":
				text := `{"boards":[{"id":"b1","title":"Работа"},{"id":"b2","title":"Дом"},{"id":"b3","title":""}]}`
				if params.Name == "list_columns" {
					// The board asked for is part of the answer, so a test can
					// see what the question carried.
					text = fmt.Sprintf(`{"columns":[{"name":"Входящие (%v)"},{"name":"Готово"}]}`, params.Arguments["board"])
				}
				reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
			case "refuses":
				reply(map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "Error: API returned HTTP 401"}},
					"isError": true,
				})
			case "not-json":
				reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": "OK"}}})
			case "echo-token":
				reply(map[string]any{"content": []any{map[string]any{"type": "text",
					"text": fmt.Sprintf(`{"cards": [{"id": 1, "title": %q}]}`, os.Getenv("FAKE_TOKEN"))}}})
			default:
				// The arguments are echoed back as a card title, so a test can
				// prove what the tool was actually asked for.
				body, _ := json.Marshal(params.Arguments)
				reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf(`{
					"cards": [
					  {"id": 41, "title": "Починить логин", "updated": "v2",
					   "url": "https://kaiten.example/card/41",
					   "description": "падает на пустом пароле",
					   "column": {"title": "В работе"}, "tags": "срочно, логин",
					   "arguments": %s},
					  {"id": 42, "title": "Обновить зависимости"},
					  {"id": 43}
					]}`, body)}}})
			}
		}
	}
}

func fakeMCPManifest(mode string) Manifest {
	return Manifest{
		Name:    "fake",
		Kind:    KindMCP,
		Command: os.Args[0],
		Args:    []string{"-test.run=TestNothing"},
		Env:     map[string]string{mcpModeEnv: mode},
		MCP: &MCPSpec{
			Tool:      "list_my_cards",
			Arguments: map[string]string{"boardId": "{{.Config.boardId}}", "mine": "true", "column": "3 очередь"},
			ItemsAt:   "cards",
			Item: ItemTemplate{
				ID:      "{{.id}}",
				Version: "{{.updated}}",
				Title:   "{{.title}}",
				Body:    "{{.description}}",
				URL:     "{{.url}}",
				Props:   map[string]string{"Колонка": "{{.column.title}}"},
				Labels:  []string{"{{.tags}}"},
			},
		},
	}
}

// TestNothing is what the re-invoked process is told to run: the fake server
// never reaches the test runner, but the flag has to name something real.
func TestNothing(t *testing.T) {}

func mcpEntry() model.Source {
	return model.Source{
		Name: "kaiten", Plugin: "fake", Enabled: true,
		Config: map[string]string{"boardId": "77"},
	}
}

// The whole bridge over a real process: dial, call the tool, read its answer as
// items. What comes out is what every other plugin hands the runner, which is
// the point — nothing downstream knows this was MCP.
func TestAnMCPToolIsReadAsAFeedOfItems(t *testing.T) {
	manifest, err := fakeMCPManifest("ok").Validate()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialMCP(context.Background(), mcpEntry(), manifest, pluginCredentials())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	res, err := conn.Poll(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	// Three rows came back and one of them has no title: a row that cannot be
	// read costs that row, not the poll.
	if len(res.Items) != 2 {
		t.Fatalf("items: %d", len(res.Items))
	}

	var first model.Item
	if err := json.Unmarshal(res.Items[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.ExternalID != "41" || first.Version != "v2" || first.Title != "Починить логин" {
		t.Fatalf("item: %+v", first)
	}
	if first.URL != "https://kaiten.example/card/41" || first.Body != "падает на пустом пароле" {
		t.Fatalf("item: %+v", first)
	}
	// A nested field, a property and a comma-separated list of labels: the three
	// shapes a service's row actually comes in.
	if first.Props["Колонка"] != "В работе" {
		t.Fatalf("props: %+v", first.Props)
	}
	if len(first.Labels) != 2 || first.Labels[0] != "срочно" || first.Labels[1] != "логин" {
		t.Fatalf("labels: %+v", first.Labels)
	}
	// The row itself is kept, because the day the service changes shape the
	// only way to find out what it now sends is to look at what it sent.
	if !strings.Contains(string(first.Raw), "Починить логин") {
		t.Fatalf("raw: %s", first.Raw)
	}
}

// What a person typed into the source dialog has to reach the tool, or a
// manifest could never be written for a service with more than one board.
func TestTheToolIsCalledWithWhatTheSourceWasConfiguredWith(t *testing.T) {
	manifest, err := fakeMCPManifest("ok").Validate()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialMCP(context.Background(), mcpEntry(), manifest, pluginCredentials())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	res, err := conn.Poll(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var first model.Item
	if err := json.Unmarshal(res.Items[0], &first); err != nil {
		t.Fatal(err)
	}
	var row struct {
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(first.Raw, &row); err != nil {
		t.Fatal(err)
	}
	// A form has only strings in it, and an MCP tool is schema-checked on the
	// server's side — "77" where a number was declared is a refusal, not a
	// coercion. So what looks like a number or a boolean is sent as one.
	if row.Arguments["boardId"] != float64(77) {
		t.Fatalf("boardId: %#v", row.Arguments["boardId"])
	}
	if row.Arguments["mine"] != true {
		t.Fatalf("mine: %#v", row.Arguments["mine"])
	}
	// And everything else stays the string it was, including what merely starts
	// with a digit.
	if row.Arguments["column"] != "3 очередь" {
		t.Fatalf("column: %#v", row.Arguments["column"])
	}
}

// A manifest is typed by hand, so the tool it names is checked once at dial —
// with the list of what there is, which is the answer to the mistake.
func TestAManifestNamingAToolThatIsNotThereSaysWhatThereIs(t *testing.T) {
	manifest, err := fakeMCPManifest("no-tool").Validate()
	if err != nil {
		t.Fatal(err)
	}
	_, err = dialMCP(context.Background(), mcpEntry(), manifest, pluginCredentials())
	if err == nil {
		t.Fatal("a tool that does not exist must be refused at dial")
	}
	if !strings.Contains(err.Error(), "get_card") {
		t.Fatalf("the error should list what there is: %v", err)
	}
}

// An MCP tool reports its own failure inside a successful call — that is what
// isError is for — so a refusal has to be read out of the result rather than
// waited for as a protocol error.
func TestAToolThatRefusesIsAFailedPollAndNotAnEmptyOne(t *testing.T) {
	manifest, err := fakeMCPManifest("refuses").Validate()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialMCP(context.Background(), mcpEntry(), manifest, pluginCredentials())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Poll(context.Background(), ""); err == nil {
		t.Fatal("a refused tool must fail the poll")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("the error should carry what the tool said: %v", err)
	}
}

// Every MCP server that returns data returns JSON in a text block, and that is
// a convention rather than a rule — so a tool that returns prose says so
// instead of producing nothing and looking like an empty feed.
func TestAToolThatDoesNotReturnJSONSaysSo(t *testing.T) {
	manifest, err := fakeMCPManifest("not-json").Validate()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialMCP(context.Background(), mcpEntry(), manifest, pluginCredentials())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Poll(context.Background(), ""); err == nil || !msg.Is(err, "mcp.notJSON") {
		t.Fatalf("err: %v", err)
	}
}

// The mapping is the whole of what a new MCP source has to write, so what it
// refuses matters as much as what it accepts.
func TestAMappingThatCannotProduceAnItemIsRefused(t *testing.T) {
	cases := map[string]MCPSpec{
		"без инструмента": {Item: ItemTemplate{Title: "{{.title}}"}},
		"без заголовка":   {Tool: "list"},
		"версия без id":   {Tool: "list", Item: ItemTemplate{Title: "{{.title}}", Version: "{{.updated}}"}},
	}
	for name, spec := range cases {
		if _, err := spec.Validate(); err == nil {
			t.Errorf("%s: должно быть отклонено", name)
		}
	}
	// An id may be left out: an item without one is identified by the hash of
	// what it says, like every other source with no ids of its own.
	if _, err := (MCPSpec{Tool: "list", Item: ItemTemplate{Title: "{{.title}}"}}).Validate(); err != nil {
		t.Fatalf("mapping without an id: %v", err)
	}
}

// Where the list is inside the answer is named rather than guessed: a tool that
// returns one object with three arrays in it is the ordinary case.
func TestTheListIsFoundWhereTheManifestSaysItIs(t *testing.T) {
	payload := json.RawMessage(`{"data": {"items": [{"id": 1}, {"id": 2}]}, "other": [{"id": 9}]}`)

	rows, err := rowsAt(payload, "data.items")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows: %+v, err %v", rows, err)
	}
	if _, err := rowsAt(payload, "data.nothing"); err == nil {
		t.Fatal("a path that is not there must say so")
	}
	// A tool that returns the array itself, and one that returns a single
	// object: both are feeds.
	if rows, err := rowsAt(json.RawMessage(`[{"id": 1}]`), ""); err != nil || len(rows) != 1 {
		t.Fatalf("bare array: %+v, err %v", rows, err)
	}
	if rows, err := rowsAt(json.RawMessage(`{"id": 1}`), ""); err != nil || len(rows) != 1 {
		t.Fatalf("single object: %+v, err %v", rows, err)
	}
}

// pluginCredentials is what the runner would hand a plugin. Empty here: the
// fake server wants no token, and what happens to a real one is the manifest's
// tokenEnv, proven in TestACredentialReachesAnMCPServerAsItsOwnVariable.
func pluginCredentials() plugin.Credentials { return plugin.Credentials{} }

// An MCP server has nowhere in the protocol to be given a credential, so it
// reads one from an environment variable it names itself — which is why the
// manifest names the variable and this app does not guess it.
func TestACredentialReachesAnMCPServerAsItsOwnVariable(t *testing.T) {
	manifest := fakeMCPManifest("echo-token")
	manifest.TokenEnv = "FAKE_TOKEN"
	manifest, err := manifest.Validate()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialMCP(context.Background(), mcpEntry(), manifest,
		plugin.Credentials{AccessToken: "секрет-123"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	res, err := conn.Poll(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var item model.Item
	if err := json.Unmarshal(res.Items[0], &item); err != nil {
		t.Fatal(err)
	}
	if item.Title != "секрет-123" {
		t.Fatalf("the server was not given the token: %+v", item)
	}
}
