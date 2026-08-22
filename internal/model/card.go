package model

import (
	"sort"
	"strings"
	"time"
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

	// Props are the card's own named values. A flow condition asks about these,
	// and a person answers a waiting stage by setting one.
	Props map[string]string `json:"props,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Prop reads a property case-insensitively.
func (c Card) Prop(name string) string { return PropValue(c.Props, name) }

// Comment is one line of a card's history: what a session said, what a flow
// decided, what a person answered.
type Comment struct {
	ID     int64  `json:"id"`
	CardID string `json:"cardId"`
	// Author is who spoke: an agent's name, a source's name, or empty for the
	// application itself.
	Author    string    `json:"author,omitempty"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

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
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// InboxGroup is the inbox as it is read: one source and its cards. Cards
// grouped by what brought them is the whole of the inbox screen.
type InboxGroup struct {
	Source string `json:"source"`
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
