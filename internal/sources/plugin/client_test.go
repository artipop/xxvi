package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artipop/gandvik/internal/msg"
	"github.com/artipop/gandvik/sources/protocol"
	"github.com/artipop/gandvik/sources/sdk"
)

// The plugin these tests talk to is this test binary, re-invoked. That is the
// only way to prove the thing that actually breaks — a real process, real
// stdio, real line framing — without depending on a toolchain being installed
// to build a second program.

const pluginModeEnv = "GANDVIK_TEST_PLUGIN"

func TestMain(m *testing.M) {
	if mode := os.Getenv(pluginModeEnv); mode != "" {
		// The modes beginning with sdk- are plugins written the way an author
		// would write them, against the SDK. Everything else is hand-rolled,
		// so a mistake the SDK cannot make can still be tested for.
		if strings.HasPrefix(mode, "sdk-") {
			runSDKPlugin(mode)
			return
		}
		runTestPlugin(mode)
		return
	}
	os.Exit(m.Run())
}

// runSDKPlugin is a plugin as somebody else would write one: a few lines
// against sources/sdk. It is what the checker is run against below, so the two
// halves of the contract meet in a test.
func runSDKPlugin(mode string) {
	switch mode {
	case "sdk-good":
		sdk.Serve(sdk.Source{
			Capabilities: protocol.Capabilities{Poll: true, Cursor: true},
			Poll: func(_ context.Context, req sdk.PollRequest) (sdk.PollResult, error) {
				if req.Cursor != "" {
					return sdk.PollResult{Cursor: req.Cursor}, nil
				}
				return sdk.PollResult{
					Items:  []sdk.Item{{ExternalID: "n1", Title: "Доставка завтра"}},
					Cursor: "c1",
				}, nil
			},
		})
	case "sdk-sloppy":
		// The mistakes that cost items quietly: no id, no title, and a cursor
		// promised but never returned.
		sdk.Serve(sdk.Source{
			Capabilities: protocol.Capabilities{Poll: true, Cursor: true},
			Poll: func(context.Context, sdk.PollRequest) (sdk.PollResult, error) {
				return sdk.PollResult{Items: []sdk.Item{{Title: ""}}}, nil
			},
		})
	case "sdk-idle":
		// Claims nothing, so it can never bring anything.
		sdk.Serve(sdk.Source{})
	}
}

// runTestPlugin is the other side of the protocol: a plugin small enough to
// read in one sitting, which is also what the SDK will have to make easy.
func runTestPlugin(mode string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), maxLine)
	out := json.NewEncoder(os.Stdout)
	send := func(v any) { _ = out.Encode(v) }

	if mode == "chatty" {
		// What a Node plugin does to itself the first time somebody runs it.
		fmt.Println("added 42 packages in 3s")
	}
	for in.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case MethodInitialize:
			version := Version
			if mode == "newer" {
				version = Version + 1
			}
			send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": InitializeResult{
				ProtocolVersion: version,
				Capabilities:    Capabilities{Poll: true, Cursor: true, Push: mode == "push", Noisy: true},
			}})
			if mode == "push" {
				// Unasked, and immediately: a watcher has no schedule.
				send(map[string]any{"jsonrpc": "2.0", "method": NotifyItems, "params": ItemsNotification{
					Items: []json.RawMessage{json.RawMessage(`{"id":"pushed","title":"Пришло само"}`)},
				}})
			}
		case MethodPoll:
			var params PollParams
			_ = json.Unmarshal(req.Params, &params)
			switch {
			case mode == "broken":
				send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{
					"code": -32000, "message": "сервис не отвечает",
					"data": map[string]string{"kind": KindRetryable},
				}})
			case params.Cursor == "":
				send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": PollResult{
					Items:  []json.RawMessage{json.RawMessage(`{"id":"n1","title":"Первое"}`)},
					Cursor: "c1",
				}})
			default:
				// The cursor came back, so there is nothing new to report.
				send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": PollResult{Cursor: params.Cursor}})
			}
		case MethodShutdown:
			send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
			return
		}
	}
}

// recorder collects what a plugin said without being asked.
type recorder struct {
	mu    sync.Mutex
	items [][]json.RawMessage
	logs  []string
}

func (r *recorder) Items(items []json.RawMessage, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, items)
}

func (r *recorder) Log(level, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, level+": "+message)
}

func (r *recorder) NeedsReauth(string) {}

func (r *recorder) pushed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

func dialTestPlugin(t *testing.T, mode string, handler Handler) (*Client, error) {
	t.Helper()
	return Dial(context.Background(), Spec{
		Command: []string{os.Args[0]},
		Env:     []string{pluginModeEnv + "=" + mode},
		Source:  SourceInfo{Name: "телефон", Config: map[string]string{"label": "INBOX"}},
		Host:    HostInfo{Name: "Gandvik"},
	}, handler)
}

func TestAPluginIntroducesItselfAndIsAskedOnlyForWhatItClaims(t *testing.T) {
	client, err := dialTestPlugin(t, "ok", &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	caps := client.Capabilities()
	if !caps.Poll || !caps.Cursor || caps.Push {
		t.Fatalf("capabilities: %+v", caps)
	}
}

func TestTheCursorGoesBackToThePluginThatIssuedIt(t *testing.T) {
	client, err := dialTestPlugin(t, "ok", &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()

	first, err := client.Poll(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Cursor != "c1" {
		t.Fatalf("first poll: %+v", first)
	}

	// The app stores the cursor and hands it back; what it means is the
	// plugin's business and nothing here reads it.
	second, err := client.Poll(ctx, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 0 {
		t.Fatalf("second poll: %+v", second)
	}
}

// A push plugin has no schedule: it says so at the handshake and then talks
// whenever it has something.
func TestAPushPluginDeliversWithoutBeingAsked(t *testing.T) {
	rec := &recorder{}
	client, err := dialTestPlugin(t, "push", rec)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if !client.Capabilities().Push {
		t.Fatal("the plugin said it pushes and the capabilities say otherwise")
	}
	deadline := time.Now().Add(5 * time.Second)
	for rec.pushed() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rec.pushed() != 1 {
		t.Fatalf("nothing arrived: %+v", rec.items)
	}
}

// The kind is what the app decides from: a network failure is worth coming back
// for, a bad field is not.
func TestAPluginsRefusalCarriesWhatToDoAboutIt(t *testing.T) {
	client, err := dialTestPlugin(t, "broken", &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.Poll(context.Background(), "")
	var pluginErr *Error
	if !asPluginError(err, &pluginErr) {
		t.Fatalf("error %v is not the plugin's", err)
	}
	if !pluginErr.Retryable() || pluginErr.NeedsReauth() {
		t.Fatalf("kind %q", pluginErr.Kind)
	}
	if !strings.Contains(pluginErr.Error(), "сервис не отвечает") {
		t.Fatalf("the plugin's own words were lost: %q", pluginErr.Error())
	}
}

// A plugin built against a newer protocol is refused at the handshake, with a
// message somebody can act on, rather than misunderstood three messages later.
func TestAPluginFromANewerBuildIsRefusedAtOnce(t *testing.T) {
	client, err := dialTestPlugin(t, "newer", &recorder{})
	if err == nil {
		client.Close()
		t.Fatal("a newer protocol version was accepted")
	}
	if !msg.Is(err, "plugin.newerProtocol") {
		t.Fatalf("the error does not say what to do: %v", err)
	}
}

// asPluginError is errors.As without the import, kept local to the test so the
// production code is not shaped by it.
func asPluginError(err error, target **Error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

// Something on stdout that is not a message is the likeliest thing to go wrong
// with a plugin somebody else wrote — a stray console.log, a banner from a
// package manager. It costs that line and nothing else.
func TestAStrayLineOnStdoutCostsOnlyThatLine(t *testing.T) {
	rec := &recorder{}
	client, err := dialTestPlugin(t, "chatty", rec)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	got, err := client.Poll(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("poll: %+v", got)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var warned bool
	for _, line := range rec.logs {
		if strings.Contains(line, "not a message") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("the stray line was swallowed without a word: %+v", rec.logs)
	}
}
