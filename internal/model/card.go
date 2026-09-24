package model

import (
	"sort"
	"strings"
	"time"

	"github.com/artipop/xxvi/internal/msg"
)

// CardState is where a card is in its life. Exactly four, and a card in state
// StateFlow always stands on some stage — "on a flow but nowhere" is an
// invariant, not a convention (docs/system.md §3).
type CardState string

const (
	StateInbox   CardState = "inbox"
	StateFlow    CardState = "flow"
	StateDone    CardState = "done"
	StateDropped CardState = "dropped"
)

// Valid reports whether the state is one this application knows.
func (s CardState) Valid() bool {
	switch s {
	case StateInbox, StateFlow, StateDone, StateDropped:
		return true
	}
	return false
}

// Card is one unit of work: something a source brought, or something a person
// typed.
type Card struct {
	ID string `json:"id"`

	// Source is what brought the card, and it is what the inbox groups by.
	// Empty means a person made it here.
	Source string `json:"source,omitempty"`
	// ExternalID and ItemVersion are which item of the source this card was
	// made from, and the state of that item when it was. Together they are what
	// stops the same letter becoming a second card.
	ExternalID  string `json:"externalId,omitempty"`
	ItemVersion string `json:"itemVersion,omitempty"`

	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// URL is the way back to the original.
	URL string `json:"url,omitempty"`

	State CardState `json:"state"`
	// Assignee is who the card is for. An agent name means "let this agent
	// work it"; anything else means a person took it, and then no agent starts
	// (see PickAgent).
	Assignee string `json:"assignee,omitempty"`
	// Project is where the work happens: the id of a registry entry, so the
	// project can be renamed without the card noticing. Empty is a real answer —
	// a task that starts from a blank page gets a folder of its own.
	Project string `json:"project,omitempty"`
	// WorkMode is how the card works in its project when that is a repository
	// (workmode.go). A person's answer, like the project itself.
	WorkMode string `json:"workMode,omitempty"`
	// Branch, Base and Worktree are what that answer came to, written the first
	// time the card's work needed a folder: the card's branch, what it was cut
	// from, and — for a separate working tree — where that tree is. Empty until
	// then. Once written they are the fact, and the mode no longer changes:
	// the work is on that branch.
	Branch   string `json:"branch,omitempty"`
	Base     string `json:"base,omitempty"`
	Worktree string `json:"worktree,omitempty"`

	// Props are the card's own named values. A flow condition asks about these,
	// and a person answers a waiting stage by setting one.
	Props map[string]string `json:"props,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Prop reads a property case-insensitively.
func (c Card) Prop(name string) string { return PropValue(c.Props, name) }

// JournalEntry is one line of a card's journal: who did what to it, and when.
// The journal is an audit — written always, read when somebody goes looking —
// and what a person watches the work by is the ribbon (docs/system.md §12).
// The table is still card_comment: a migration step is never edited.
type JournalEntry struct {
	ID     int64     `json:"id"`
	CardID string    `json:"cardId"`
	Kind   EntryKind `json:"kind"`
	// Author is who spoke: an agent's name, a source's name, or empty for the
	// application itself.
	Author string `json:"author,omitempty"`
	// Msg is what the application itself says, for the UI to word. Text is
	// words somebody else wrote — an agent's report, a person's answer — and
	// the entries from before the application spoke in codes.
	Msg  *msg.Msg `json:"msg,omitempty"`
	Text string   `json:"text,omitempty"`
	// SessionID is the agent run the entry was written for, when there was
	// one. It ties a step's report and its failure to their own screen: a
	// report is written the moment before the card moves on, in the same
	// millisecond as the next transition, and by time it would land on the
	// wrong segment.
	SessionID string `json:"sessionId,omitempty"`
	// EventID is the transition the card stood in when the entry was written —
	// the segment it belongs to, for the entries no run is behind. Zero on the
	// entries written before it was recorded.
	EventID   int64     `json:"eventId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// EntryKind is what a journal entry is. A closed set: the ribbon picks what it
// shows by it, and a kind it does not know is a kind it silently drops. Empty
// on the entries written before kinds were recorded: those are journal only.
type EntryKind string

const (
	// EntryMove: the card entered a stage, finished its flow, left it, or an
	// agent opened its terminal. Journal only — on the ribbon these are the
	// segments themselves.
	EntryMove EntryKind = "move"
	// EntryProblem: why the card stands, or why a step broke. A plaque on its
	// segment.
	EntryProblem EntryKind = "problem"
	// EntryReport: what an agent said its step came to. Under its screen.
	EntryReport EntryKind = "report"
	// EntryAsk: an agent's question and the answer to it. Journal only — on
	// the ribbon they are part of the agent's stream.
	EntryAsk EntryKind = "ask"
	// EntryProps: a stage put values on the card. Journal only — the values
	// are on the card.
	EntryProps EntryKind = "props"
	// EntrySource: the source the card came from changed its item.
	EntrySource EntryKind = "source"
	// EntryReview: a person sent the work back and said what is wrong with
	// it. A plaque on the segment it was said on, because it is the reason the
	// next one exists.
	EntryReview EntryKind = "review"
)

// FlowState is where a card stands on its route.
type FlowState struct {
	CardID    string    `json:"cardId"`
	FlowID    string    `json:"flowId"`
	StageID   string    `json:"stageId"`
	EnteredAt time.Time `json:"enteredAt"`
	// Visited is every stage the card has already been through. A flow may
	// loop, so this is a set and not a trail.
	Visited []string `json:"visited,omitempty"`
}

// FlowEvent is one recorded transition — the answer to "why is this card here".
type FlowEvent struct {
	ID        int64     `json:"id"`
	CardID    string    `json:"cardId"`
	FlowID    string    `json:"flowId"`
	FromStage string    `json:"fromStage,omitempty"`
	ToStage   string    `json:"toStage"`
	On        string    `json:"on,omitempty"`
	Detail    *msg.Msg  `json:"detail,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// InboxGroup is the inbox as it is read: one source and its cards. Cards
// grouped by what brought them is the whole of the inbox screen.
type InboxGroup struct {
	Source string `json:"source"`
	// Plugin is what kind of source it is, for the screen to word.
	Plugin string `json:"plugin,omitempty"`
	Cards  []Card `json:"cards"`
}

// GroupBySource arranges cards into inbox groups, newest card first inside each
// group and the busiest source first. Cards a person made carry no source and
// are grouped under an empty name, which the UI names in its own language.
func GroupBySource(cards []Card) []InboxGroup {
	bySource := map[string][]Card{}
	for _, c := range cards {
		bySource[c.Source] = append(bySource[c.Source], c)
	}
	out := make([]InboxGroup, 0, len(bySource))
	for source, group := range bySource {
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].CreatedAt.After(group[j].CreatedAt)
		})
		out = append(out, InboxGroup{Source: source, Cards: group})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Cards) != len(out[j].Cards) {
			return len(out[i].Cards) > len(out[j].Cards)
		}
		return strings.ToLower(out[i].Source) < strings.ToLower(out[j].Source)
	})
	return out
}
