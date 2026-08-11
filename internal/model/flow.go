// Package model is the domain: what a card, a flow, an agent and a source are,
// and everything about them that is a question rather than an action.
//
// Nothing here touches a database, spawns a process or logs. That is deliberate
// — the decisions a flow makes are the part worth testing without a machine,
// and keeping them pure is what makes that possible.
package model

import (
	"fmt"
	"strings"
)

// A flow is the route a card takes: a graph of stages (a place a card stands,
// and the work done there) and edges (which event moves the card on, and where).
//
// The set of edge triggers is closed and implemented in Go (see Triggers): the
// graph says *what* connects to what, the code decides *when*. Nothing stored
// is interpreted as a script.

// Stage actions: what runs when a card enters a stage.
const (
	// ActionNone runs nothing. The card stands and waits for an event —
	// normally a person's answer. This is where a human fork lives.
	ActionNone = "none"
	// ActionAgent runs an ACP agent session on the card's task.
	ActionAgent = "agent"
)

// Actions is every accepted action, in the order the editor offers them.
var Actions = []string{ActionNone, ActionAgent}

// Edge triggers. The outcome ones are produced by the stage's own session; the
// last one is a person setting something on the card.
const (
	TriggerSuccess = "success"
	TriggerFailure = "failure"
	TriggerBlocked = "blocked"

	// TriggerCardChanged fires when a property is set on the card while it
	// stands on the stage — a person marking «Одобрено», say. Which property
	// and value is the edge's own condition, so the trigger kind stays closed
	// and the flow decides only the vocabulary.
	TriggerCardChanged = "card.changed"
)

// Trigger sources, which decide who can produce an event. There is no polling
// source in this application yet; the field exists because adding one (git,
// github) must not change the engine or the editor — see docs/poc.md.
const (
	SourceOutcome = "outcome" // the stage's own session finished
	SourceHuman   = "human"   // the card itself changed; pushed, never polled
)

// Trigger describes one edge trigger. The list doubles as the editor's
// dropdown, so the UI can never offer a trigger the engine does not implement.
type Trigger struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Label  string `json:"label"`
}

// Triggers is the closed set, in the order the editor shows them.
var Triggers = []Trigger{
	{Kind: TriggerSuccess, Source: SourceOutcome, Label: "шаг прошёл"},
	{Kind: TriggerFailure, Source: SourceOutcome, Label: "шаг упал"},
	{Kind: TriggerBlocked, Source: SourceOutcome, Label: "шаг сделать не удалось"},
	{Kind: TriggerCardChanged, Source: SourceHuman, Label: "на карточке выбрано"},
}

// TriggerByKind looks a trigger up in the closed set.
func TriggerByKind(kind string) (Trigger, bool) {
	for _, t := range Triggers {
		if t.Kind == kind {
			return t, true
		}
	}
	return Trigger{}, false
}

// TriggerLabel is the human phrasing used in comments and on edges.
func TriggerLabel(kind string) string {
	if t, ok := TriggerByKind(kind); ok {
		return t.Label
	}
	return kind
}

// IsOutcome reports whether the trigger is produced by the stage's own session.
func IsOutcome(kind string) bool {
	t, ok := TriggerByKind(kind)
	return ok && t.Source == SourceOutcome
}

// Flow is one named route.
type Flow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// EntryStage is where a card taken into work starts. It is stated rather
	// than inferred: a graph with a loop has no stage without incoming edges,
	// so guessing the start breaks a flow at its first backward transition.
	EntryStage string  `json:"entryStage"`
	Stages     []Stage `json:"stages"`
	Edges      []Edge  `json:"edges"`
}

// Stage is one node: the place a card stands, and the work done there.
//
// In XCIII a stage referenced a board column and the column carried the
// behaviour. There is no board here, so a stage is the only place a card can
// stand and there is nothing left to split off — see docs/system.md §1.
type Stage struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	Action string `json:"action"`
	// Prompt is what the agent is told about this step, on top of its own
	// prompt and the card's task.
	Prompt string `json:"prompt,omitempty"`
	// Crew is who may work this stage. Not an assignment but a list of who is
	// allowed at all: the card chooses among them (see PickAgent).
	Crew []string `json:"crew,omitempty"`
	// MaxRunning bounds how many cards this stage works at once. Zero means no
	// limit of its own; the machine-wide limit still applies.
	MaxRunning int `json:"maxRunning,omitempty"`
	// Final says a card reaching this stage is done. It runs nothing and leads
	// nowhere.
	Final bool `json:"final,omitempty"`

	// X and Y are where the editor left the stage on its canvas. Absent means
	// "lay it out for me" — a flow written by hand never has to place anything.
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`
}

// Edge is one transition.
type Edge struct {
	ID   string `json:"id,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
	On   string `json:"on"`

	// If makes the transition conditional. Several conditional edges may share
	// one (From, On) — the first whose condition holds wins, and an edge with
	// no condition is the fallback. For TriggerCardChanged the condition is not
	// a guard but the event itself: which value firing it means.
	If *Cond `json:"if,omitempty"`
}

// Cond is what a conditional transition asks about, in exactly one of two
// forms. Both are questions about the card, not scripts.
type Cond struct {
	// Property/Value: the card carries this property with this value.
	Property string `json:"property,omitempty"`
	Value    string `json:"value,omitempty"`

	// CommentContains: the agent's closing words contain this text — how a
	// stage lets the agent itself route the card («ГОТОВО К ДЕПЛОЮ»).
	CommentContains string `json:"commentContains,omitempty"`
}

// Holds evaluates the condition against the card's properties and, for the
// comment form, the agent's closing words. A nil condition always holds — an
// unconditional edge is the fallback.
func (c *Cond) Holds(props map[string]string, agentText string) bool {
	if c == nil {
		return true
	}
	if c.CommentContains != "" {
		return containsFold(agentText, c.CommentContains)
	}
	return strings.EqualFold(strings.TrimSpace(PropValue(props, c.Property)), strings.TrimSpace(c.Value))
}

// Describe is the condition in the reader's language, for edge captions and
// card comments.
func (c *Cond) Describe() string {
	if c == nil {
		return ""
	}
	if c.CommentContains != "" {
		return fmt.Sprintf("в ответе агента есть «%s»", c.CommentContains)
	}
	return fmt.Sprintf("«%s» = «%s»", c.Property, c.Value)
}

// IsZero reports a condition that asks nothing.
func (c *Cond) IsZero() bool {
	return c == nil || (c.Property == "" && c.Value == "" && c.CommentContains == "")
}

// PropValue looks a card property up the way every name is matched here:
// case-insensitively.
func PropValue(props map[string]string, name string) string {
	if props == nil {
		return ""
	}
	if v, ok := props[name]; ok {
		return v
	}
	for k, v := range props {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// Stage returns the stage with the given id.
func (f Flow) Stage(id string) (Stage, bool) {
	for _, s := range f.Stages {
		if s.ID == id {
			return s, true
		}
	}
	return Stage{}, false
}

// StageByName returns the stage with a name, matched case-insensitively — how a
// person names one.
func (f Flow) StageByName(name string) (Stage, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Stage{}, false
	}
	for _, s := range f.Stages {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return Stage{}, false
}

// Entry is the stage a card taken into work starts on.
func (f Flow) Entry() (Stage, bool) {
	return f.Stage(f.EntryStage)
}

// Next returns the stage an event moves the card to: among the edges for this
// event, the first whose condition holds against the card — and an edge with no
// condition holds always, which makes it the fallback however the editor
// ordered it.
func (f Flow) Next(stageID, on string, props map[string]string, agentText string) (Stage, *Cond, bool) {
	var fallback *Edge
	for i, e := range f.Edges {
		if e.From != stageID || e.On != on {
			continue
		}
		if e.If.IsZero() {
			if fallback == nil {
				fallback = &f.Edges[i]
			}
			continue
		}
		if e.If.Holds(props, agentText) {
			stage, ok := f.Stage(e.To)
			return stage, e.If, ok
		}
	}
	if fallback != nil {
		stage, ok := f.Stage(fallback.To)
		return stage, nil, ok
	}
	return Stage{}, nil, false
}

// HasEdge reports whether the stage has any transition for an event at all —
// what "this stage listens for X" means, before any condition is asked.
func (f Flow) HasEdge(stageID, on string) bool {
	for _, e := range f.Edges {
		if e.From == stageID && e.On == on {
			return true
		}
	}
	return false
}

// WatchesProperty reports whether the stage has a card.changed edge naming this
// property. Setting «Приоритет» must not wake a stage waiting on «Одобрено»,
// and must not leave a "nothing matched" comment either — the change was simply
// not addressed to it.
func (f Flow) WatchesProperty(stageID, property string) bool {
	for _, e := range f.Edges {
		if e.From == stageID && e.On == TriggerCardChanged && e.If != nil &&
			strings.EqualFold(strings.TrimSpace(e.If.Property), strings.TrimSpace(property)) {
			return true
		}
	}
	return false
}

// WaitDescriptions is what a parked card says it is waiting on, conditions
// included — «на карточке выбрано «Одобрено» = «Да»», not just the kind. Stage
// outcomes are left out: a card is not "waiting" for its own session.
func (f Flow) WaitDescriptions(stageID string) []string {
	var out []string
	for _, e := range f.Edges {
		if e.From != stageID || IsOutcome(e.On) {
			continue
		}
		label := TriggerLabel(e.On)
		if desc := e.If.Describe(); desc != "" {
			label += " " + desc
		}
		out = append(out, label)
	}
	return out
}

// OutgoingFrom is every edge leaving a stage, in order. The editor draws these.
func (f Flow) OutgoingFrom(stageID string) []Edge {
	var out []Edge
	for _, e := range f.Edges {
		if e.From == stageID {
			out = append(out, e)
		}
	}
	return out
}
