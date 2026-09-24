package engine

import (
	"time"

	"github.com/artipop/xxvi/internal/model"
)

// What a card can say about itself: which flow it is on, where along it it
// stands, and what it is waiting for. The card shows this instead of making
// somebody read the journal backwards to work out why it has not moved.

// CardStage is one stage of the flow as the card sees it.
type CardStage struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Action  string   `json:"action"`
	Crew    []string `json:"crew,omitempty"`
	Final   bool     `json:"final,omitempty"`
	Current bool     `json:"current"`
	Done    bool     `json:"done"` // the card has already been through it
}

// CardFlow is the whole answer for one card.
type CardFlow struct {
	FlowID   string      `json:"flowId"`
	FlowName string      `json:"flowName"`
	Stages   []CardStage `json:"stages"`
	StageID  string      `json:"stageId"`
	Since    time.Time   `json:"since"`
	// WaitingFor is what the stage is waiting on: the events, and the conditions
	// that make them the ones. Empty on a stage that runs something.
	WaitingFor []model.Wait `json:"waitingFor,omitempty"`
	Queued     bool         `json:"queued,omitempty"`  // waiting for a place on the stage
	Running    bool         `json:"running,omitempty"` // a session of this stage is working now
	// Marks are the moves a person can make from here — see model.Mark. Empty
	// wherever the flow does not offer one, which is every stage that decides
	// for itself.
	Marks []model.Mark `json:"marks,omitempty"`
}

// CardFlowFor describes where a card stands. It returns nothing — and no error
// — for a card that is not on a flow, since most cards are not.
func (e *Engine) CardFlowFor(cardID string) (*CardFlow, error) {
	st, ok, err := e.store.FlowState(cardID)
	if err != nil || !ok {
		return nil, err
	}
	flow, err := e.store.Flow(st.FlowID)
	if err != nil {
		return nil, nil // the flow was deleted; the card says so in its journal
	}

	out := &CardFlow{FlowID: flow.ID, FlowName: flow.Name, StageID: st.StageID, Since: st.EnteredAt}

	// "Done" is what the card's own history says it has been through, not what
	// the graph makes possible: a flow with a loop has no linear order.
	visited := make(map[string]bool, len(st.Visited))
	for _, id := range st.Visited {
		visited[id] = true
	}
	for _, s := range flow.Stages {
		out.Stages = append(out.Stages, CardStage{
			ID: s.ID, Name: s.Name, Action: s.Action, Crew: s.Crew, Final: s.Final,
			Current: s.ID == st.StageID,
			Done:    visited[s.ID] && s.ID != st.StageID,
		})
	}
	out.WaitingFor = flow.Waits(st.StageID)

	if e.runner != nil {
		out.Running = e.runner.RunningOnStage(st.StageID) > 0 && e.cardIsRunning(cardID)
	}
	out.Running = out.Running || e.Publishing(cardID)
	if !out.Running {
		// A stage that is working answers for itself; two buttons beside a
		// running session would be a second way to answer for it.
		out.Marks = flow.MarksFrom(st.StageID)
		out.Queued, _ = e.store.IsQueued(cardID)
	}
	return out, nil
}

// cardIsRunning asks the store rather than the runner: a live session is a row
// with a non-terminal status, and that is one answer rather than two.
func (e *Engine) cardIsRunning(cardID string) bool {
	sessions, err := e.store.SessionsForCard(cardID)
	if err != nil {
		return false
	}
	for _, s := range sessions {
		if !s.Status.Terminal() {
			return true
		}
	}
	return false
}

// StageLoad is how busy one stage is right now.
type StageLoad struct {
	StageID string `json:"stageId"`
	Cards   int    `json:"cards"`   // cards standing on it
	Queued  int    `json:"queued"`  // of those, waiting for a place
	Running int    `json:"running"` // of those, being worked on now
}

// FlowOverview is one flow and where its cards are along it.
type FlowOverview struct {
	Flow   model.Flow  `json:"flow"`
	Stages []StageLoad `json:"stages"`
	Cards  int         `json:"cards"`
}

// Overview answers "where is everything right now" for one flow, from the same
// data the engine runs on rather than a second bookkeeping of it.
func (e *Engine) Overview(flowID string) (FlowOverview, error) {
	flow, err := e.store.Flow(flowID)
	if err != nil {
		return FlowOverview{}, err
	}
	onStage, err := e.store.CardsOnStage(flowID)
	if err != nil {
		return FlowOverview{}, err
	}
	queued, err := e.store.QueuedOnStage(flowID)
	if err != nil {
		return FlowOverview{}, err
	}
	view := FlowOverview{Flow: flow}
	for _, s := range flow.Stages {
		load := StageLoad{StageID: s.ID, Cards: onStage[s.ID], Queued: queued[s.ID]}
		if e.runner != nil {
			load.Running = e.runner.RunningOnStage(s.ID)
		}
		view.Cards += load.Cards
		view.Stages = append(view.Stages, load)
	}
	return view, nil
}
