package engine

import (
	"fmt"
	"strconv"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/store"
)

// The ribbon is the card's journal of transitions made visible (docs/system.md
// §11). There is no ribbon stored anywhere and none is needed: every entry onto
// a stage is already a flow_event, including the first one, so the journal read
// in order is the strip read left to right.
//
// That is the whole reason to build it here rather than to keep a second
// bookkeeping of it: a strip assembled from something other than the journal
// could disagree with where the card actually stands, and then two answers to
// one question would have to be reconciled by whoever noticed.

// ScreenView is one window on the strip.
type ScreenView struct {
	// ID is derived rather than stored, and stable across re-reads: the UI
	// keys its panes by it, and a pane that loses its identity is an iframe
	// that reloads and a cursor that jumps out of the notes.
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Ref is what to open, with the card's properties already put in.
	Ref string `json:"ref,omitempty"`
	// Waiting names the properties that had no value yet. The screen stands
	// blank and says so rather than opening nothing and staying silent.
	Waiting []string `json:"waiting,omitempty"`
	// SessionID is set on the screens that belong to an agent's run — its
	// stream, and later the terminals of the commands it ran.
	SessionID string `json:"sessionId,omitempty"`
}

// Segment is one visit to one stage: everything that happened between this
// transition and the next.
type Segment struct {
	// ID is the segment's identity for the UI, which reconciles the strip by
	// it: without one, a re-read would rebuild every pane and the iframe of a
	// running preview would reload on every step the agent takes.
	ID string `json:"id"`
	// EventID is the flow_event this segment is, and the first half of every
	// screen id under it.
	EventID   int64     `json:"eventId"`
	StageID   string    `json:"stageId"`
	StageName string    `json:"stageName"`
	On        string    `json:"on,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	EnteredAt time.Time `json:"enteredAt"`
	Current   bool      `json:"current"`
	// Gone says the stage was removed from the flow while the card was on it.
	// The segment stays — what happened happened — and says why it is empty.
	Gone    bool         `json:"gone,omitempty"`
	Screens []ScreenView `json:"screens,omitempty"`
}

// RibbonView is one card in work, shown as a strip.
type RibbonView struct {
	CardID   string    `json:"cardId"`
	Title    string    `json:"title"`
	FlowID   string    `json:"flowId"`
	FlowName string    `json:"flowName"`
	Segments []Segment `json:"segments"`
	// FocusID is where the ribbon flies when a step ends: the first screen of
	// the segment the card stands in.
	FocusID string `json:"focusId,omitempty"`
}

// RibbonSummary is one ribbon in the switcher.
type RibbonSummary struct {
	CardID    string `json:"cardId"`
	Title     string `json:"title"`
	FlowName  string `json:"flowName"`
	StageName string `json:"stageName"`
	Running   bool   `json:"running,omitempty"`
}

// Ribbons is every card in work, in the order the work screen shows them. One
// card in work is one ribbon; there is no other kind.
func (e *Engine) Ribbons() ([]RibbonSummary, error) {
	cards, err := e.store.CardsInState(model.StateFlow)
	if err != nil {
		return nil, err
	}
	out := make([]RibbonSummary, 0, len(cards))
	for _, c := range cards {
		row := RibbonSummary{CardID: c.ID, Title: c.Title}
		if flow, err := e.CardFlowFor(c.ID); err == nil && flow != nil {
			row.FlowName, row.Running = flow.FlowName, flow.Running
			for _, s := range flow.Stages {
				if s.Current {
					row.StageName = s.Name
				}
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// Ribbon assembles one card's strip.
//
// A stage entered twice gets two segments rather than one reused, because the
// check that sent the card back and the check after the fix are two steps with
// two results, and showing them as one screen would erase the first.
func (e *Engine) Ribbon(cardID string) (RibbonView, error) {
	card, err := e.store.Card(cardID)
	if err != nil {
		return RibbonView{}, err
	}
	events, err := e.store.FlowEvents(cardID)
	if err != nil {
		return RibbonView{}, err
	}
	view := RibbonView{CardID: card.ID, Title: card.Title}
	if len(events) == 0 {
		return view, nil
	}

	// The flow is the one the card is on now; the journal may reach back into
	// an earlier one if the card was taken off and put on another, and those
	// segments keep their place and say their stage is gone.
	flowID := events[len(events)-1].FlowID
	flow, err := e.store.Flow(flowID)
	if err != nil {
		return RibbonView{}, err
	}
	view.FlowID, view.FlowName = flow.ID, flow.Name

	state, onFlow, err := e.store.FlowState(cardID)
	if err != nil {
		return RibbonView{}, err
	}
	sessions, err := e.store.SessionsForCard(cardID)
	if err != nil {
		return RibbonView{}, err
	}

	for i, ev := range events {
		seg := Segment{
			ID: strconv.FormatInt(ev.ID, 10), EventID: ev.ID, StageID: ev.ToStage, On: ev.On,
			Detail: ev.Detail, EnteredAt: ev.CreatedAt,
		}
		// The last event is where the card stands — but only while it is still
		// on a flow: a card taken off keeps its journal and stops having a
		// place in it.
		seg.Current = onFlow && i == len(events)-1 && ev.ToStage == state.StageID

		stage, ok := flow.Stage(ev.ToStage)
		if !ok {
			seg.Gone = true
			seg.StageName = ev.ToStage
			view.Segments = append(view.Segments, seg)
			continue
		}
		seg.StageName = stage.Name

		// A run belongs to the visit it started during: matching by stage alone
		// would hang every run of a looping stage on its first visit.
		for _, s := range sessionsIn(sessions, stage.ID, ev.CreatedAt, entryAfter(events, i)) {
			seg.Screens = append(seg.Screens, ScreenView{
				ID:        screenID(ev.ID, "agent", s.ID),
				Kind:      "agent",
				Title:     "Ход агента · " + s.AgentName,
				SessionID: s.ID,
			})
		}
		for n, sc := range stage.Screens {
			ref, waiting := model.ResolveRef(sc.Ref, card.Props)
			title := sc.Title
			if title == "" {
				title = model.ScreenKindLabel(sc.Kind)
			}
			seg.Screens = append(seg.Screens, ScreenView{
				ID:      screenID(ev.ID, strconv.Itoa(n), ""),
				Kind:    sc.Kind,
				Title:   title,
				Ref:     ref,
				Waiting: waiting,
			})
		}
		view.Segments = append(view.Segments, seg)
	}

	for _, seg := range view.Segments {
		if seg.Current && len(seg.Screens) > 0 {
			view.FocusID = seg.Screens[0].ID
		}
	}
	return view, nil
}

// screenID is «<событие>|<что>[|<чей>]». Derived from the journal row rather
// than generated, so two reads of an unchanged ribbon produce the same ids and
// the panes that did not change are not rebuilt.
func screenID(eventID int64, what, whose string) string {
	if whose == "" {
		return fmt.Sprintf("%d|%s", eventID, what)
	}
	return fmt.Sprintf("%d|%s|%s", eventID, what, whose)
}

// entryAfter is when the card left this visit — the next transition, or the
// zero time while it is still standing here.
func entryAfter(events []model.FlowEvent, i int) time.Time {
	if i+1 < len(events) {
		return events[i+1].CreatedAt
	}
	return time.Time{}
}

// sessionsIn is the runs that started during one visit, oldest first.
// SessionsForCard hands them back newest first, which is right for a card's
// history and backwards for a strip that reads left to right.
func sessionsIn(sessions []store.Session, stageID string, from, until time.Time) []store.Session {
	var out []store.Session
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		if s.StageID != stageID || s.StartedAt.Before(from) {
			continue
		}
		if !until.IsZero() && !s.StartedAt.Before(until) {
			continue
		}
		out = append(out, s)
	}
	return out
}
