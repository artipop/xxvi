package sdk

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/sources/protocol"
)

// The SDK is the half a plugin author writes against, so what is worth pinning
// is what a plugin looks like from the app's side of the pipe: the bytes.

// drive runs a source against a script of requests and returns the lines it
// wrote back.
func drive(t *testing.T, source Source, requests ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out strings.Builder
	serve(source, in, &out)

	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("плагин написал не сообщение: %q (%v)", line, err)
		}
		messages = append(messages, msg)
	}
	return messages
}

const initRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":1,"source":{"name":"проверка","config":{"label":"INBOX"}},` +
	`"credentials":{"accessToken":"t0k"},"host":{"name":"XXVI"}}}`

func TestAPluginAnswersTheHandshakeWithWhatItCanDo(t *testing.T) {
	got := drive(t, Source{Capabilities: protocol.Capabilities{Poll: true, Cursor: true}}, initRequest)

	if len(got) != 1 {
		t.Fatalf("messages: %+v", got)
	}
	result := got[0]["result"].(map[string]any)
	if result["protocolVersion"].(float64) != float64(protocol.Version) {
		t.Fatalf("version: %+v", result)
	}
	caps := result["capabilities"].(map[string]any)
	if caps["poll"] != true || caps["cursor"] != true {
		t.Fatalf("capabilities: %+v", caps)
	}
}

// The config a person filled in and the token the app holds reach the plugin
// through the handshake, not through the poll — a plugin should not have to
// remember them itself.
func TestWhatWasConfiguredReachesThePoll(t *testing.T) {
	var seen PollRequest
	source := Source{
		Capabilities: protocol.Capabilities{Poll: true},
		Poll: func(_ context.Context, req PollRequest) (PollResult, error) {
			seen = req
			return PollResult{Items: []Item{{ExternalID: "n1", Title: "Первое"}}}, nil
		},
	}

	got := drive(t, source, initRequest, `{"jsonrpc":"2.0","id":2,"method":"poll","params":{"cursor":"c1"}}`)

	if seen.Config["label"] != "INBOX" || seen.Credentials.AccessToken != "t0k" || seen.Cursor != "c1" {
		t.Fatalf("poll request: %+v", seen)
	}
	result := got[1]["result"].(map[string]any)
	items := result["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["title"] != "Первое" {
		t.Fatalf("items: %+v", result)
	}
}

// A refusal has to carry its kind, because that is what the app decides from:
// come back later, stop and ask a person, or stop and wait for the config.
func TestARefusalCarriesItsKind(t *testing.T) {
	source := Source{
		Capabilities: protocol.Capabilities{Poll: true},
		Poll: func(context.Context, PollRequest) (PollResult, error) {
			return PollResult{}, BadConfig("label", "ярлыка %q нет в этом ящике", "INBOX")
		},
	}

	got := drive(t, source, initRequest, `{"jsonrpc":"2.0","id":2,"method":"poll"}`)

	failure := got[1]["error"].(map[string]any)
	data := failure["data"].(map[string]any)
	if data["kind"] != protocol.KindBadConfig || data["field"] != "label" {
		t.Fatalf("error: %+v", failure)
	}
	if !strings.Contains(failure["message"].(string), "ярлыка") {
		t.Fatalf("the plugin's own words were lost: %+v", failure)
	}
}

// A watcher never answers a poll; it talks when it has something.
func TestAWatchingPluginPushesThroughItsSession(t *testing.T) {
	source := Source{
		Capabilities: protocol.Capabilities{Push: true},
		Start: func(_ context.Context, s *Session) error {
			s.Items([]Item{{ExternalID: "n1", Title: "Пришло само"}}, "c1")
			s.Log("info", "слушаю")
			return nil
		},
	}

	got := drive(t, source, initRequest)

	if len(got) != 3 {
		t.Fatalf("messages: %+v", got)
	}
	if got[1]["method"] != protocol.NotifyItems || got[2]["method"] != protocol.NotifyLog {
		t.Fatalf("notifications: %+v", got[1:])
	}
}

// A refreshed token arrives without a restart, and the next poll has to use it.
func TestARefreshedTokenIsUsedByTheNextPoll(t *testing.T) {
	var seen string
	source := Source{
		Capabilities: protocol.Capabilities{Poll: true},
		Poll: func(_ context.Context, req PollRequest) (PollResult, error) {
			seen = req.Credentials.AccessToken
			return PollResult{}, nil
		},
	}

	drive(t, source,
		initRequest,
		`{"jsonrpc":"2.0","id":2,"method":"credentials/update","params":{"accessToken":"новый"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"poll"}`)

	if seen != "новый" {
		t.Fatalf("token: %q", seen)
	}
}

// The app may be killed rather than say goodbye, and a plugin's own cleanup
// still has to run.
func TestClosingThePipeStopsThePluginProperly(t *testing.T) {
	stopped := make(chan struct{})
	source := Source{
		Capabilities: protocol.Capabilities{Poll: true},
		Shutdown:     func() { close(stopped) },
	}

	go serve(source, strings.NewReader(""), io.Discard)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("плагин не завершился, когда закрыли ввод")
	}
}

func TestRetryAfterIsSentInSeconds(t *testing.T) {
	source := Source{
		Capabilities: protocol.Capabilities{Poll: true},
		Poll: func(context.Context, PollRequest) (PollResult, error) {
			return PollResult{RetryAfter: 90 * time.Second}, nil
		},
	}

	got := drive(t, source, initRequest, `{"jsonrpc":"2.0","id":2,"method":"poll"}`)

	result := got[1]["result"].(map[string]any)
	if result["retryAfterSeconds"].(float64) != 90 {
		t.Fatalf("retryAfterSeconds: %+v", result)
	}
}
