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

// Screen kinds: what a stage puts in front of the person while a card stands on
// it (docs/system.md §11.2).
//
// The set is closed for the same reason the triggers are: the flow says *what*
// to show, the code decides *how*. A kind the editor can offer and nothing can
// render is the same trap as a trigger nothing fires.
const (
	// ScreenNotes is a markdown file inside the card's working folder — the
	// same folder the agent works in, so a plan it writes and a plan a person
	// edits are one file rather than two copies of one intention.
	ScreenNotes = "notes"
	// ScreenTerminal is a command. An empty one is a shell in the card's
	// working folder.
	ScreenTerminal = "terminal"
	// ScreenBrowser is an address.
	ScreenBrowser = "browser"
)

// ScreenKinds is every accepted screen kind, in the order the editor offers them.
var ScreenKinds = []string{ScreenNotes, ScreenTerminal, ScreenBrowser}

// ScreenKindLabel names a kind for a person. The editor shows these; the flow
// stores the constant.
func ScreenKindLabel(kind string) string {
	switch kind {
	case ScreenNotes:
		return "заметки"
	case ScreenTerminal:
		return "терминал"
	case ScreenBrowser:
		return "браузер"
	}
	return kind
}

// Edge triggers. The outcome ones are produced by the stage's own session; the
// last one is a person setting something on the card.
//
// The set is closed and every member of it is something the engine actually
// fires. There is deliberately no third outcome for "the agent could not do
// this": a session ends done or failed, and an agent that wants to say more
// than that says it in its closing words, which an edge condition can ask
// about — a trigger the editor offers and nothing ever produces is a trap.
const (
	TriggerSuccess = "success"
	TriggerFailure = "failure"

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

// The card's own field for how a stage ended, and the two values it takes.
//
// It exists because how a stage ended used to be expressible only as *where the
// card went*: a flow grew a «Не прошло» stage whose whole content was one fact
// about the step before it, and that fact then read as a place where work
// happens. An outcome belongs to the card, and a stage is for work.
//
// Written by the engine for every stage without anybody declaring it — which is
// what makes it the app's own field rather than one of the stage's outputs, and
// why the editor never offers it among them. A condition may still ask about
// it: that is the whole point of it being a closed set.
//
// Binary, and a third value was tried and taken out. What the card carries here
// is whether the step worked, and every question anybody asks of it is that
// question. «Заблокировано» is not a third answer to it — it is a reason, and
// the reason is a sentence in the card's comments, where it can be one.
const (
	OutcomeProperty = "Исход"
	OutcomePassed   = "прошло"
	OutcomeFailed   = "не прошло"
)

// OutcomeValues is the closed set, for the editor and for anything checking a
// person's answer.
var OutcomeValues = []string{OutcomePassed, OutcomeFailed}

// OutcomeValue is what a trigger is called on the card. Empty for anything that
// is not a stage's own outcome — a person's answer is news about the work, not
// a verdict the machine reached.
func OutcomeValue(trigger string) string {
	switch trigger {
	case TriggerSuccess:
		return OutcomePassed
	case TriggerFailure:
		return OutcomeFailed
	default:
		return ""
	}
}

// IsOutcomeProperty reports whether a name is the outcome field, matched the way
// every name is matched here.
func IsOutcomeProperty(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), OutcomeProperty)
}

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
	// ID is unique across every flow, not merely within one: a card records
	// where it stands by stage id, and "which flow is this stage in" has to
	// have one answer. The editor generates them, so this costs nothing; a
	// hand-written flow that reuses an id is refused by name.
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

	// Writes are the properties this stage leaves on the card — its declared
	// outputs, and what makes a transition on a property deterministic rather
	// than hopeful: the edge that asks about «Вердикт» points at the stage that
	// must produce it.
	//
	// The agent delivers them in its closing words, in the shape the brief asks
	// for (see engine.StageOutputs), and a required one is refused without: the
	// stage cannot end until the value stands.
	Writes []PropertyWrite `json:"writes,omitempty"`

	// Reads are the properties whose values open this stage's brief: what an
	// earlier stage wrote — a preview address, a reviewer's verdict — handed to
	// the agent instead of hoping it goes looking. Empty is not "nothing": it
	// falls back to whatever the stages ahead of this one on the route declare
	// they write (UpstreamWrites), because a value already on the card is on the
	// card, and asking somebody to tick it again on every later stage is asking
	// for the same fact twice. A list of its own is how a stage says "not all of
	// it", and clearing it gives the graph's answer back.
	Reads []string `json:"reads,omitempty"`

	// Screens are what a person looking at this step sees: the notes, the
	// terminal, the browser that belong to it. They are declared by any stage,
	// including one that runs nothing — unlike Writes and Reads, and that is a
	// decision rather than an oversight (docs/system.md §11.4). The subject
	// differs: an output is about the card and there is nobody to produce one
	// on a waiting stage, while a screen is about the person, and the review
	// stage is exactly where the preview has to be open.
	//
	// The agent's own screens — its stream, and the terminals of the commands
	// it ran — are not declared: they exist whenever what they show exists.
	Screens []Screen `json:"screens,omitempty"`

	// X and Y are where the editor left the stage on its canvas. Absent means
	// "lay it out for me" — a flow written by hand never has to place anything.
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`
}

// Screen is one window inside a ribbon segment: what kind it is, what to call
// it, and what it points at — a file path, a command line or an address,
// depending on the kind.
//
// Ref may name card properties as «{Превью}». That is the same currency as a
// stage's declared inputs (docs/system.md §4.3): the stage that writes the
// preview address is the stage the browser screen below it opens.
type Screen struct {
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	Ref   string `json:"ref,omitempty"`
}

// PropertyWrite is one property a stage puts on the card: its name, and whether
// the stage may finish without it. The value is not here — the agent supplies it
// when it ends.
type PropertyWrite struct {
	Property string `json:"property"`
	Required bool   `json:"required,omitempty"`
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

// UpstreamWrites is every property a stage can count on finding on the card
// when it starts: what the stages ahead of it on this route declare as outputs.
// It is what a stage's reads fall back to, so declaring a write once carries the
// value on by itself instead of being ticked again on each later stage.
//
// The walk is backwards over the incoming edges, with a visited set, because the
// graph has cycles by design: a failed check sends the card back to the agent,
// and that arrow is an ordinary part of a route rather than a mistake. A stage
// never reads its own output back. The order is the order the properties are met
// in, so two runs of one stage produce the same brief.
func (f Flow) UpstreamWrites(stageID string) []string {
	stages := make(map[string]Stage, len(f.Stages))
	for _, s := range f.Stages {
		stages[s.ID] = s
	}
	seen := map[string]bool{stageID: true}
	taken := map[string]bool{}
	var out []string
	for queue := []string{stageID}; len(queue) > 0; {
		at := queue[0]
		queue = queue[1:]
		for _, e := range f.Edges {
			if e.To != at || seen[e.From] {
				continue
			}
			seen[e.From] = true
			queue = append(queue, e.From)
			for _, w := range stages[e.From].Writes {
				key := strings.ToLower(strings.TrimSpace(w.Property))
				if key == "" || taken[key] {
					continue
				}
				taken[key] = true
				out = append(out, w.Property)
			}
		}
	}
	return out
}

// ReadsFor is what this stage is handed on the way in: its own list, and — when
// it declares none — whatever the stages ahead of it write. See Stage.Reads for
// why the graph is the second answer rather than nothing.
func (f Flow) ReadsFor(stageID string) []string {
	if s, ok := f.Stage(stageID); ok && len(s.Reads) > 0 {
		return s.Reads
	}
	return f.UpstreamWrites(stageID)
}

// WrittenProperties is every property any stage of the flow declares it writes,
// folded for comparison.
func (f Flow) WrittenProperties() map[string]bool {
	out := map[string]bool{}
	for _, s := range f.Stages {
		for _, w := range s.Writes {
			if key := strings.ToLower(strings.TrimSpace(w.Property)); key != "" {
				out[key] = true
			}
		}
	}
	return out
}

// UnwrittenConditions is the dataflow check: every conditional edge of the flow
// whose property no stage of the flow declares it writes.
//
// Not an error — the value may be a person's own answer, which is what the
// card.changed wait is for, and the outcome field is written by the engine for
// every stage without anybody declaring it. But a route built on a property
// nothing produces is a route that quietly never moves, and that is worth a
// sentence where the flow is edited rather than a card that mysteriously stands
// still.
func (f Flow) UnwrittenConditions() []string {
	written := f.WrittenProperties()
	var out []string
	seen := map[string]bool{}
	for _, e := range f.Edges {
		if e.If == nil || e.If.Property == "" {
			continue
		}
		// A person's answer is not a stage's output, and neither is the field
		// the engine fills in for every stage.
		if e.On == TriggerCardChanged || IsOutcomeProperty(e.If.Property) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(e.If.Property))
		if written[key] || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e.If.Property)
	}
	return out
}

// ScreenRefs is every property name a screen reference names, in the order they
// are met. «{Превью}» is one; the braces are the whole syntax, because anything
// richer would be a script and nothing stored here is interpreted as one.
func ScreenRefs(ref string) []string {
	var out []string
	seen := map[string]bool{}
	for rest := ref; ; {
		open := strings.Index(rest, "{")
		if open < 0 {
			return out
		}
		rest = rest[open+1:]
		close := strings.Index(rest, "}")
		if close < 0 {
			return out
		}
		name := strings.TrimSpace(rest[:close])
		rest = rest[close+1:]
		if key := strings.ToLower(name); name != "" && !seen[key] {
			seen[key] = true
			out = append(out, name)
		}
	}
}

// ResolveRef puts the card's property values into a screen reference and says
// which names had nothing to put.
//
// A missing value is not filled with emptiness and forgotten: the screen stands
// blank and says which property it is waiting for. Opening about:blank and
// staying silent would be the same screen with the reason taken out.
func ResolveRef(ref string, props map[string]string) (string, []string) {
	var waiting []string
	out := ref
	for _, name := range ScreenRefs(ref) {
		value := strings.TrimSpace(PropValue(props, name))
		if value == "" {
			waiting = append(waiting, name)
			continue
		}
		out = strings.ReplaceAll(out, "{"+name+"}", value)
	}
	return out, waiting
}

// UnresolvedScreenRefs is the dataflow check for screens, next to
// UnwrittenConditions and for the same reason: a screen pointing at a property
// no stage of the flow declares it writes is a screen that quietly stays blank.
//
// Not an error — a person may well set the value by hand — but worth a sentence
// where the flow is edited rather than an empty window with no explanation.
func (f Flow) UnresolvedScreenRefs() []string {
	written := f.WrittenProperties()
	var out []string
	seen := map[string]bool{}
	for _, s := range f.Stages {
		for _, sc := range s.Screens {
			for _, name := range ScreenRefs(sc.Ref) {
				key := strings.ToLower(strings.TrimSpace(name))
				if written[key] || seen[key] || IsOutcomeProperty(name) {
					continue
				}
				seen[key] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// Mark is a way a person can move a card from where it stands: the value they
// would put in the outcome field, and the stage that value leads to.
//
// It exists because a stage where nobody works and nothing runs waits for a
// person to say how it went — and saying so was finding a field on the card and
// picking a value out of it, three clicks from the card's face. There are two
// answers and the flow already knows where each of them leads, so the card can
// offer them as what they are: forward, and back.
type Mark struct {
	Value string `json:"value"`
	Stage string `json:"stage"`
	// Forward is the answer that carries the work on, as against the one that
	// sends it back. It decides which way the arrow points and nothing else.
	Forward bool `json:"forward"`
}

// MarksFrom is what a person can say from this stage: the flow's own
// card.changed edges that ask about the outcome field, named by where they lead.
// Only those — an edge waiting on somebody's «Одобрено» is this flow's own
// vocabulary and is answered on the card, and an edge on a stage's own outcome
// belongs to whatever runs there.
func (f Flow) MarksFrom(stageID string) []Mark {
	var out []Mark
	for _, e := range f.Edges {
		if e.From != stageID || e.On != TriggerCardChanged || e.If == nil {
			continue
		}
		if !IsOutcomeProperty(e.If.Property) {
			continue
		}
		to, ok := f.Stage(e.To)
		if !ok {
			continue
		}
		out = append(out, Mark{
			Value:   e.If.Value,
			Stage:   to.Name,
			Forward: strings.EqualFold(e.If.Value, OutcomePassed),
		})
	}
	return out
}
