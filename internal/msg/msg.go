// Package msg is what the application has to tell a person, said as a code and
// the values that go into it — never as a sentence.
//
// The words belong to the UI, which is the only place that knows what language
// the person reads. A sentence composed here would be in one language forever,
// and every refusal, journal entry and label would have to be written twice to
// change that; a code is written once and worded wherever it is shown.
//
// Agents are the exception and are not served from here: what they are told is
// a prompt, not a screen, and it is written in English where it is composed.
package msg

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Msg is one thing to tell a person.
type Msg struct {
	Code string `json:"code"`
	// Args are the values the wording needs — names, paths, counts — keyed by
	// what they are. Never words of their own: a value here is shown as it is.
	Args map[string]string `json:"args,omitempty"`
	// Cause is the reason under this one, when there is one worth telling: a
	// stage that is refused because of one of its screens says both.
	Cause *Msg `json:"cause,omitempty"`
}

// CodeInternal is a failure nobody wrote a code for — the disk, the database,
// a program that printed something. Its text is what that failure said, which
// is the most anybody knows about it, and the UI shows it as a detail.
const CodeInternal = "internal"

// New makes a message from its code and key-value pairs.
func New(code string, kv ...string) Msg {
	m := Msg{Code: code}
	if len(kv) > 1 {
		m.Args = make(map[string]string, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			m.Args[kv[i]] = kv[i+1]
		}
	}
	return m
}

// Because returns the message with err as its cause.
func (m Msg) Because(err error) Msg {
	if err != nil {
		c := Of(err)
		m.Cause = &c
	}
	return m
}

// IsZero reports a message that says nothing.
func (m Msg) IsZero() bool { return m.Code == "" }

// Arg reads one value.
func (m Msg) Arg(key string) string { return m.Args[key] }

// String is the message for a log or a test: the code, its values in a stable
// order, and the cause after a colon. Not for a person — it is not in anybody's
// language.
func (m Msg) String() string {
	var b strings.Builder
	b.WriteString(m.Code)
	if len(m.Args) > 0 {
		keys := make([]string, 0, len(m.Args))
		for k := range m.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("(")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k + "=" + m.Args[k])
		}
		b.WriteString(")")
	}
	if m.Cause != nil {
		b.WriteString(": " + m.Cause.String())
	}
	return b.String()
}

// Parse reads a message back from the column it was stored in. A column
// written before messages were codes holds a sentence, and that sentence is
// what happened, so it comes back as text rather than being dropped.
func Parse(stored string) Msg {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return Msg{}
	}
	var m Msg
	if strings.HasPrefix(stored, "{") && json.Unmarshal([]byte(stored), &m) == nil && m.Code != "" {
		return m
	}
	return New(CodeText, "text", stored)
}

// CodeText is words that were already words: a row from before messages were
// codes, or text somebody else wrote. The UI shows them as they are.
const CodeText = "text"

// Store is the message as a column holds it; empty for no message.
func (m Msg) Store() string {
	if m.IsZero() {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// Error is a refusal, carrying what to tell the person about it.
type Error struct {
	Msg
	wrapped error
}

// Err makes a refusal.
func Err(code string, kv ...string) *Error { return &Error{Msg: New(code, kv...)} }

// Wrap makes a refusal whose cause is err, and which errors.Is still sees
// through to it.
func Wrap(err error, code string, kv ...string) *Error {
	return &Error{Msg: New(code, kv...).Because(err), wrapped: err}
}

// Tag makes a refusal that errors.Is sees through to err without telling the
// person about it: a store's "not found" is how a caller recognizes the case,
// and the message already says what was not found.
func Tag(err error, code string, kv ...string) *Error {
	return &Error{Msg: New(code, kv...), wrapped: err}
}

func (e *Error) Error() string { return e.Msg.String() }

func (e *Error) Unwrap() error { return e.wrapped }

// MarshalJSON is the message, which is all the UI is sent.
func (e *Error) MarshalJSON() ([]byte, error) { return json.Marshal(e.Msg) }

// Of is what an error says to a person: its own message if it carries one —
// found through any wrapping, since a caller adding context for a log does not
// change what the person should read — and otherwise the failure as it came.
func Of(err error) Msg {
	if err == nil {
		return Msg{}
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return New(CodeInternal, "text", err.Error())
}

// Is reports whether err carries a message with this code, itself or as the
// cause of one: a stage refused over one of its screens is both refusals.
func Is(err error, code string) bool {
	if err == nil {
		return false
	}
	for m := Of(err); ; m = *m.Cause {
		if m.Code == code {
			return true
		}
		if m.Cause == nil {
			return false
		}
	}
}
