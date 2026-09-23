package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"time"
)

// Sources turn outside events into cards. Everything entering the pipeline is
// normalized to an Item before anything decides what to do with it, which is
// what keeps a polled source and a pushed one on the same path.

// Item is one thing a source brought: one letter, one notification, one issue.
type Item struct {
	// ExternalID identifies the object in the source's own namespace, and
	// Version identifies its state. A source reports what it can see rather
	// than what changed, so without the pair every poll would create the whole
	// world again.
	ExternalID string    `json:"id"`
	Version    string    `json:"version,omitempty"`
	Title      string    `json:"title"`
	Body       string    `json:"body,omitempty"`
	URL        string    `json:"url,omitempty"`
	At         time.Time `json:"at,omitempty"`

	Props  map[string]string `json:"props,omitempty"`
	Labels []string          `json:"labels,omitempty"`

	// Raw is the payload as it arrived. It is kept because the day a source
	// changes shape, the only way to find out what it now sends is to look at
	// what it sent.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// WithFallbackID gives the item an id when the sender had none to give: the
// hash of what it said. A source that loses the answer to a request repeats it,
// and without a stable key the repeat would be a second card.
func (it Item) WithFallbackID() Item {
	if strings.TrimSpace(it.ExternalID) != "" {
		return it
	}
	sum := sha256.Sum256([]byte(it.Title + "\x00" + it.Body + "\x00" + it.At.UTC().Format(time.RFC3339)))
	it.ExternalID = "sha256:" + hex.EncodeToString(sum[:16])
	return it
}

// Rule actions.
const (
	ActionCard = "card" // create a card
	ActionDrop = "drop" // deliberately ignore
)

// RuleActions lists every accepted action, in the order the UI offers them.
var RuleActions = []string{ActionCard, ActionDrop}

// Match is what a rule looks at. An empty Match matches everything, which is
// how a catch-all rule is written; every field given must match, so a rule with
// two fields is an "and".
type Match struct {
	Title  string            `json:"title,omitempty"`  // regexp
	Body   string            `json:"body,omitempty"`   // regexp
	Labels []string          `json:"labels,omitempty"` // any one of them
	Props  map[string]string `json:"props,omitempty"`  // property → regexp
}

// IsZero reports whether the rule matches everything.
func (m Match) IsZero() bool {
	return m.Title == "" && m.Body == "" && len(m.Labels) == 0 && len(m.Props) == 0
}

// Matches reports whether the item satisfies every field the match names.
func (m Match) Matches(it Item) bool {
	if m.IsZero() {
		return true
	}
	if !matchRegexp(m.Title, it.Title) || !matchRegexp(m.Body, it.Body) {
		return false
	}
	if len(m.Labels) > 0 && !anyLabel(m.Labels, it.Labels) {
		return false
	}
	for prop, expr := range m.Props {
		if !matchRegexp(expr, PropValue(it.Props, prop)) {
			return false
		}
	}
	return true
}

// Rule is what to do with an item. Rules are tried in order and the first match
// wins, so the catch-all goes last.
type Rule struct {
	Name string `json:"name,omitempty"`
	When Match  `json:"when"`
	Then string `json:"then"`

	// Props are card properties, as Go templates over the item:
	// {"Ссылка": "{{.URL}}"}.
	Props map[string]string `json:"props,omitempty"`

	// SuggestFlow prefills the flow in the «В работу» dialog. It is a
	// suggestion and nothing more: taking a card into work is a person's
	// decision (docs/system.md §3), so a rule may say which flow it expects
	// but may not start one.
	SuggestFlow string `json:"suggestFlow,omitempty"`
	// Assignee prefills who the card is for, the same way and for the same
	// reason.
	Assignee string `json:"assignee,omitempty"`
}

// FirstMatch returns the first rule the item satisfies.
func FirstMatch(rules []Rule, it Item) (Rule, bool) {
	for _, r := range rules {
		if r.When.Matches(it) {
			return r, true
		}
	}
	return Rule{}, false
}

// Source is one registered source.
type Source struct {
	Name    string `json:"name"`
	Plugin  string `json:"plugin,omitempty"`
	Enabled bool   `json:"enabled"`

	// Noisy inverts the default for what matched no rule: a stream of
	// notifications is mostly noise, so there a rule is a subscription and
	// everything else is dropped. For an ordinary source the opposite holds —
	// silently losing an item is what makes an integration impossible to debug
	// — so it goes to the inbox instead.
	Noisy bool `json:"noisy,omitempty"`

	// Update says what a changed item does to the card it already has.
	Update string `json:"update,omitempty"` // update (default) | ignore

	Config          map[string]string `json:"config,omitempty"`
	IntervalSeconds int               `json:"intervalSeconds,omitempty"`
	Rules           []Rule            `json:"rules,omitempty"`
}

// Update modes for a changed item. The card is brought up to date rather than
// told about the change in a note: the card is what a person and an agent read
// the task from, and a note underneath saying it is out of date is a task read
// wrong until somebody scrolls down.
const (
	UpdateInPlace = "update"
	UpdateIgnore  = "ignore"
)

// UpdateMode is the source's answer for a changed item, defaulted.
func (s Source) UpdateMode() string {
	if strings.EqualFold(strings.TrimSpace(s.Update), UpdateIgnore) {
		return UpdateIgnore
	}
	return UpdateInPlace
}

// Decision is what the pipeline concluded about one item: the action to take
// and the card it would produce. It is returned rather than performed, so the
// decision can be tested without a database.
type Decision struct {
	Action      string
	Rule        string // which rule decided, empty for the default
	Props       map[string]string
	SuggestFlow string
	Assignee    string
}

// Decide applies the source's rules to an item. An item that matched no rule
// goes to the inbox — or is dropped, if the source is noisy.
func (s Source) Decide(it Item) Decision {
	rule, ok := FirstMatch(s.Rules, it)
	if !ok {
		if s.Noisy {
			return Decision{Action: ActionDrop}
		}
		return Decision{Action: ActionCard}
	}
	action := strings.TrimSpace(strings.ToLower(rule.Then))
	if action == "" {
		action = ActionCard
	}
	return Decision{
		Action:      action,
		Rule:        rule.Name,
		Props:       renderProps(rule.Props, it),
		SuggestFlow: rule.SuggestFlow,
		Assignee:    rule.Assignee,
	}
}

// ValidateSource normalizes and checks a registry entry, including every
// regexp in its rules — a broken expression is worth refusing where it was
// typed rather than silently never matching later.
func ValidateSource(s Source) (Source, error) {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return Source{}, fmt.Errorf("имя источника не может быть пустым")
	}
	s.Plugin = strings.TrimSpace(s.Plugin)
	s.Update = strings.TrimSpace(strings.ToLower(s.Update))
	switch s.Update {
	case "", UpdateInPlace, UpdateIgnore:
	default:
		return Source{}, fmt.Errorf("неизвестный режим обновления «%s» (допустимо: %s, %s)", s.Update, UpdateInPlace, UpdateIgnore)
	}
	if s.IntervalSeconds < 0 {
		return Source{}, fmt.Errorf("интервал опроса не может быть отрицательным")
	}
	for i, r := range s.Rules {
		r.Name = strings.TrimSpace(r.Name)
		r.Then = strings.TrimSpace(strings.ToLower(r.Then))
		if r.Then == "" {
			r.Then = ActionCard
		}
		switch r.Then {
		case ActionCard, ActionDrop:
		default:
			return Source{}, fmt.Errorf("правило %d: неизвестное действие «%s» (допустимо: %s)",
				i+1, r.Then, strings.Join(RuleActions, ", "))
		}
		if err := checkRegexps(r.When); err != nil {
			return Source{}, fmt.Errorf("правило %d: %w", i+1, err)
		}
		for name, tmpl := range r.Props {
			if _, err := template.New(name).Parse(tmpl); err != nil {
				return Source{}, fmt.Errorf("правило %d, свойство «%s»: %w", i+1, name, err)
			}
		}
		s.Rules[i] = r
	}
	return s, nil
}

func checkRegexps(m Match) error {
	for field, expr := range map[string]string{"заголовок": m.Title, "текст": m.Body} {
		if expr == "" {
			continue
		}
		if _, err := compileMatch(expr); err != nil {
			return fmt.Errorf("условие по полю «%s»: %w", field, err)
		}
	}
	for prop, expr := range m.Props {
		if _, err := compileMatch(expr); err != nil {
			return fmt.Errorf("условие по свойству «%s»: %w", prop, err)
		}
	}
	return nil
}

// renderProps expands a rule's property templates over the item. A template
// that fails is left out rather than failing the whole item: the item is real
// and the rule is a convenience.
func renderProps(props map[string]string, it Item) map[string]string {
	if len(props) == 0 {
		return nil
	}
	out := make(map[string]string, len(props))
	for name, text := range props {
		tmpl, err := template.New(name).Parse(text)
		if err != nil {
			continue
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, it); err != nil {
			continue
		}
		if v := strings.TrimSpace(buf.String()); v != "" {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// matchRegexp treats an unparseable expression as "does not match" rather than
// as "matches everything": ValidateSource refused it when it was typed, so a
// broken one here means a hand-edited row, and the safe reading of a broken
// condition is that it did not fire.
func matchRegexp(expr, value string) bool {
	if expr == "" {
		return true
	}
	re, err := compileMatch(expr)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

// compileMatch is the one place a rule's expression becomes a regexp, so
// validation and matching cannot disagree about what it means.
//
// Matching ignores case, like every name comparison here: somebody writing
// «доставк» means the notification that says «Доставка», and a rule that
// silently misses it is worse than no rule. The flag is a prefix rather than a
// wrapper, so a rule that needs case can still say (?-i).
func compileMatch(expr string) (*regexp.Regexp, error) {
	return regexp.Compile("(?i)" + expr)
}

func anyLabel(want, have []string) bool {
	for _, w := range want {
		for _, h := range have {
			if strings.EqualFold(w, h) {
				return true
			}
		}
	}
	return false
}
