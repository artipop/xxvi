package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Checking a plugin without installing the app. This is one program used from
// two ends: an author runs it against the plugin they are writing, and our own
// tests run it against a plugin written to be wrong. That it is the same code
// is the point — a checker that lived only on our side would drift from what
// the app actually accepts, and an author would find out from a user.

// Finding is one thing the checker noticed. Fatal means the app would refuse to
// use the plugin at all; the rest are things that will cost items or make the
// source confusing rather than stopping it.
type Finding struct {
	Fatal   bool   `json:"fatal"`
	Step    string `json:"step"`
	Message string `json:"message"`
}

func (f Finding) String() string {
	mark := "—"
	if f.Fatal {
		mark = "✗"
	}
	return fmt.Sprintf("%s %s: %s", mark, f.Step, f.Message)
}

// CheckOptions is what the checker starts the plugin with — the same things the
// app would give it.
type CheckOptions struct {
	Command []string
	Dir     string
	Env     []string
	Config  map[string]string
	Token   string
	Timeout time.Duration
}

// Check walks a plugin through the whole conversation and reports what does not
// hold. It returns nil when everything did.
func Check(ctx context.Context, opts CheckOptions) []Finding {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var findings []Finding
	add := func(fatal bool, step, format string, args ...any) {
		findings = append(findings, Finding{Fatal: fatal, Step: step, Message: fmt.Sprintf(format, args...)})
	}

	collector := newCheckHandler()
	client, err := Dial(ctx, Spec{
		Command:     opts.Command,
		Dir:         opts.Dir,
		Env:         opts.Env,
		Source:      SourceInfo{Name: "проверка", Config: opts.Config},
		Credentials: Credentials{AccessToken: opts.Token},
		Host:        HostInfo{Name: "XXVI", Version: "check"},
	}, collector)
	if err != nil {
		add(true, "initialize", "%v", err)
		return findings
	}
	defer client.Close()

	caps := client.Capabilities()
	if !caps.Poll && !caps.Push {
		add(true, "initialize", "плагин не умеет ни poll, ни push — он никогда ничего не принесёт")
	}
	if caps.Cursor && !caps.Poll {
		add(false, "initialize", "объявлен cursor без poll: курсор передаётся только в poll, так что он ни на что не влияет")
	}

	if caps.Poll {
		findings = append(findings, checkPoll(ctx, client, caps)...)
	} else {
		// A watcher has nothing to be asked for, so the only thing to check is
		// that it says something within a reasonable time.
		if !collector.sawAnything(2 * time.Second) {
			add(false, "push", "плагин объявил push, но за две секунды не прислал ничего — это нормально, если сейчас нечему приходить")
		}
	}

	// Refreshing a token must not require a restart, so every plugin has to
	// accept the message even if it needs no credentials.
	if err := client.UpdateCredentials(ctx, Credentials{AccessToken: opts.Token}); err != nil {
		add(false, "credentials/update", "плагин не принял обновление токена: %v", err)
	}
	return findings
}

// checkPoll is the half worth the most: an item without an id, or a cursor that
// does not come back, is a source that quietly makes duplicates for ever.
func checkPoll(ctx context.Context, client *Client, caps Capabilities) []Finding {
	var findings []Finding
	add := func(fatal bool, step, format string, args ...any) {
		findings = append(findings, Finding{Fatal: fatal, Step: step, Message: fmt.Sprintf(format, args...)})
	}

	first, err := client.Poll(ctx, "")
	if err != nil {
		var pluginErr *Error
		if e, ok := err.(*Error); ok {
			pluginErr = e
		}
		switch {
		case pluginErr == nil:
			add(true, "poll", "%v", err)
		case pluginErr.Kind == "":
			add(false, "poll", "отказ без вида (data.kind): приложение не может решить, приходить ли снова — %v", err)
		default:
			add(false, "poll", "плагин отказал: %v", err)
		}
		return findings
	}

	for i, raw := range first.Items {
		var item struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			add(true, "poll", "запись №%d не разбирается: %v", i+1, err)
			continue
		}
		if strings.TrimSpace(item.ID) == "" {
			add(false, "poll", "у записи №%d нет id — приложение посчитает его по содержимому, и запись, у которой изменился текст, станет второй карточкой", i+1)
		}
		if strings.TrimSpace(item.Title) == "" {
			add(false, "poll", "у записи №%d нет заголовка — карточка будет называться «Без заголовка»", i+1)
		}
	}

	if !caps.Cursor {
		return findings
	}
	if first.Cursor == "" {
		add(false, "poll", "объявлен cursor, но poll его не вернул: приложению нечего передать обратно, и плагин каждый раз будет отдавать всё заново")
		return findings
	}
	// The cursor has to be taken back: a plugin that ignores it re-reads its
	// whole history on every poll, which only shows up as duplicates much later.
	second, err := client.Poll(ctx, first.Cursor)
	if err != nil {
		add(false, "poll", "второй опрос с курсором не удался: %v", err)
		return findings
	}
	if len(second.Items) > 0 && sameItems(first.Items, second.Items) {
		add(false, "poll", "с курсором плагин вернул те же записи: похоже, курсор не учитывается")
	}
	return findings
}

func sameItems(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			return false
		}
	}
	return true
}

// checkHandler collects what the plugin says of its own accord. The channel is
// made up front rather than on first use: it is written from the reader
// goroutine and read from the checker's, and a lazy one would be a race.
type checkHandler struct {
	items chan struct{}
}

func newCheckHandler() *checkHandler {
	return &checkHandler{items: make(chan struct{}, 1)}
}

func (h *checkHandler) Items([]json.RawMessage, string) {
	select {
	case h.items <- struct{}{}:
	default:
	}
}

func (h *checkHandler) Log(string, string) {}

func (h *checkHandler) NeedsReauth(string) {}

func (h *checkHandler) sawAnything(within time.Duration) bool {
	select {
	case <-h.items:
		return true
	case <-time.After(within):
		return false
	}
}
