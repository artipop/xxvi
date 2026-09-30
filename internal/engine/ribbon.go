package engine

import (
	"fmt"
	"strconv"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
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
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Title is what the stage called the screen. Empty when it did not, and on
	// an agent's own screen, which the UI names by its kind and Agent.
	Title string `json:"title,omitempty"`
	// Agent is whose run the screen shows, on an agent's own screen.
	Agent string `json:"agent,omitempty"`
	// Ref is what to open, with the card's properties already put in.
	Ref string `json:"ref,omitempty"`
	// Waiting names the properties that had no value yet. The screen stands
	// blank and says so rather than opening nothing and staying silent.
	Waiting []string `json:"waiting,omitempty"`
	// SessionID is set on the screen that belongs to an agent's run: the
	// terminal it was worked in, or the stream of a session.
	SessionID string `json:"sessionId,omitempty"`
	// Report is what the run said its step came to. A terminal has nowhere
	// else to show it: the agent hands it over in a tool call, not on screen.
	Report *msg.Msg `json:"report,omitempty"`
	// Paused is set on the last run of the stage the card stands on when the
	// application closed on it: the screen offers to continue it (Continue).
	Paused bool `json:"paused,omitempty"`
}

// Notice is a journal entry the strip shows: why the card stands here, or why
// the step broke.
type Notice struct {
	// Kind tells a problem from a reviewer's remarks and from news of the
	// source: all three explain the segment, but one is the application saying
	// it stopped, one a person saying what to fix, and one the MR having moved
	// under the person reading it.
	Kind   model.EntryKind `json:"kind"`
	Msg    *msg.Msg        `json:"msg,omitempty"`
	Text   string          `json:"text,omitempty"`
	Author string          `json:"author,omitempty"`
	At     time.Time       `json:"at"`
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
	Detail    *msg.Msg  `json:"detail,omitempty"`
	EnteredAt time.Time `json:"enteredAt"`
	Current   bool      `json:"current"`
	// Gone says the stage was removed from the flow while the card was on it.
	// The segment stays — what happened happened — and says why it is empty.
	Gone    bool         `json:"gone,omitempty"`
	Screens []ScreenView `json:"screens,omitempty"`
	// Marks are the moves a person can make from here, and only the segment
	// the card stands in has any: a step already over is not waiting for an
	// answer.
	//
	// They are on the strip because the strip is the whole window (docs/system.md
	// §12.5). A review stage shows what it is asking about — the diff, the
	// preview — and sending the person to the card screen to answer would mean
	// leaving the thing they are answering about.
	Marks []model.Mark `json:"marks,omitempty"`
	// Problems are why the card stood or broke during this visit, oldest first.
	Problems []Notice `json:"problems,omitempty"`
}

// RibbonView is one card in work, shown as a strip.
type RibbonView struct {
	// ID is the ribbon's identity for the UI, which reconciles the stack by it.
	// It is the card's id: one card in work is one ribbon, and there is no
	// other kind.
	ID       string `json:"id"`
	CardID   string `json:"cardId"`
	Title    string `json:"title"`
	FlowID   string `json:"flowId"`
	FlowName string `json:"flowName"`
	// Project is the card's project, the workspace the ribbon is shown in.
	Project string `json:"project,omitempty"`
	// StageName and Running are what a ribbon says about itself from outside —
	// enough for the indicator without reading the strip.
	StageName string    `json:"stageName,omitempty"`
	Running   bool      `json:"running,omitempty"`
	Segments  []Segment `json:"segments"`
	// FocusID is where the ribbon flies when a step ends: the first screen of
	// the segment the card stands in.
	FocusID string `json:"focusId,omitempty"`
	// Returnable is a card taken off its flow into the inbox, which can go
	// back to the stage it left (Engine.Return).
	Returnable bool `json:"returnable,omitempty"`
}

// Ribbons is every card in work, whole, in the order the work screen shows
// them.
//
// Whole rather than summarised because ribbons are stacked and scrolled
// through rather than picked from a list: the one below has to already be
// there when somebody scrolls onto it, or the move lands on a blank and then
// fills in.
func (e *Engine) Ribbons() ([]RibbonView, error) {
	cards, err := e.store.CardsInState(model.StateFlow)
	if err != nil {
		return nil, err
	}
	out := make([]RibbonView, 0, len(cards))
	for _, c := range cards {
		view, err := e.Ribbon(c.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
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
	view := RibbonView{ID: card.ID, CardID: card.ID, Title: card.Title, Project: card.Project}
	if flow, err := e.CardFlowFor(cardID); err == nil && flow != nil {
		view.Running = flow.Running
		for _, s := range flow.Stages {
			if s.Current {
				view.StageName = s.Name
			}
		}
	}
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
	view.Returnable = card.State == model.StateInbox

	state, onFlow, err := e.store.FlowState(cardID)
	if err != nil {
		return RibbonView{}, err
	}
	sessions, err := e.store.SessionsForCard(cardID)
	if err != nil {
		return RibbonView{}, err
	}
	journal, err := e.store.Journal(cardID)
	if err != nil {
		return RibbonView{}, err
	}
	// Which visit each run belongs to, so that an entry written for a run lands
	// where the run is — even when it was written after the card had moved on,
	// as a cancelled run's is.
	visitOf := map[string]int64{}

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
		if seg.Current {
			seg.Marks = flow.MarksFrom(stage.ID)
		}

		// A run belongs to the visit it started during: matching by stage alone
		// would hang every run of a looping stage on its first visit.
		// What the agent's own screen is, is what the run was: a terminal shows
		// the CLI the person and the agent talked in, a session shows the
		// stream, which is all a session ever had. The run says which — not the
		// stage, which may have been edited since (docs/system.md §12.2).
		runs := sessionsIn(sessions, stage.ID, ev.CreatedAt, entryAfter(events, i))
		for n, s := range runs {
			visitOf[s.ID] = ev.ID
			kind := "agent"
			if s.Work == model.WorkTerminal {
				kind = "agentTerminal"
			}
			seg.Screens = append(seg.Screens, ScreenView{
				ID:        screenID(ev.ID, kind, s.ID),
				Kind:      kind,
				Agent:     s.AgentName,
				SessionID: s.ID,
				Report:    lastReport(journal, s.ID),
				Paused:    seg.Current && n == len(runs)-1 && s.Status == store.StatusPaused,
			})
		}
		for n, sc := range stage.Screens {
			ref, waiting := model.ResolveRef(sc.Ref, card.Props)
			seg.Screens = append(seg.Screens, ScreenView{
				ID:      screenID(ev.ID, strconv.Itoa(n), ""),
				Kind:    sc.Kind,
				Title:   sc.Title,
				Ref:     ref,
				Waiting: waiting,
			})
		}
		view.Segments = append(view.Segments, seg)
	}

	placeProblems(view.Segments, events, journal, sessions, visitOf)

	for _, seg := range view.Segments {
		if seg.Current && len(seg.Screens) > 0 {
			view.FocusID = seg.Screens[0].ID
		}
	}
	return view, nil
}

// screenID is «<event>|<what>[|<whose>]». Derived from the journal row rather
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

func lastReport(journal []model.JournalEntry, sessionID string) *msg.Msg {
	for i := len(journal) - 1; i >= 0; i-- {
		if e := journal[i]; e.Kind == model.EntryReport && e.SessionID == sessionID {
			if e.Msg != nil {
				return e.Msg
			}
			m := msg.New(msg.CodeText, "text", e.Text)
			return &m
		}
	}
	return nil
}

// placeProblems hangs each problem on the visit it happened in: by its run when
// it has one, by the transition it was written in when it has that, and by time
// only for the entries older than both.
//
// A problem that came before a run started in the same visit is left off: it
// explained a wait that is over — the stage was full, the card was a person's —
// and a plaque saying the stage is busy over an agent at work is a plaque that
// is wrong.
func placeProblems(segs []Segment, events []model.FlowEvent, journal []model.JournalEntry,
	sessions []store.Session, visitOf map[string]int64) {
	at := make(map[int64]int, len(segs))
	for i, seg := range segs {
		at[seg.EventID] = i
	}
	started := map[int64]time.Time{}
	for _, s := range sessions {
		if v, ok := visitOf[s.ID]; ok && s.StartedAt.After(started[v]) {
			started[v] = s.StartedAt
		}
	}
	for _, e := range journal {
		if e.Kind != model.EntryProblem && e.Kind != model.EntryReview && e.Kind != model.EntrySource {
			continue
		}
		var visit int64
		switch {
		case e.SessionID != "":
			visit = visitOf[e.SessionID]
		case e.EventID != 0:
			visit = e.EventID
		default:
			visit = visitByTime(events, e.CreatedAt)
		}
		i, ok := at[visit]
		if !ok {
			continue
		}
		if e.SessionID == "" && e.CreatedAt.Before(started[visit]) {
			continue
		}
		segs[i].Problems = append(segs[i].Problems, Notice{Kind: e.Kind, Msg: e.Msg, Text: e.Text, Author: e.Author, At: e.CreatedAt})
	}
}

// visitByTime is the transition in force at t: entry ≤ t < next entry.
func visitByTime(events []model.FlowEvent, t time.Time) int64 {
	var visit int64
	for _, ev := range events {
		if ev.CreatedAt.After(t) {
			break
		}
		visit = ev.ID
	}
	return visit
}
