package engine

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
)

// Again is a step's terminal moving the card on its own word: the agent sent it
// to a named stage (move_to), or a person came back to a finished step's
// terminal, asked for more, and the step reported again.
type Again struct {
	// Outcome is a report's trigger. Empty with a Target: a move is not an
	// outcome of the step.
	Outcome string
	// Text is the report in the shape ParseWrites reads.
	Text string
	// Target is a stage of the flow, by name or id. Empty: the edges of the
	// step's own stage decide.
	Target string
	// Reported is when this run already reported, zero if it has not.
	Reported time.Time
	// Asked is when a person last spoke in this run's terminal.
	Asked time.Time
}

// Rejoin moves the card for a run that may no longer be its current work. The
// work the card stands in now is not cancelled: every terminal stays where the
// person can go back to it, and only the card moves.
//
// The errors are read by the agent, so they are plain English it can act on.
func (e *Engine) Rejoin(job Job, a Again) error {
	e.mu.Lock()
	left, err := e.rejoinLocked(job, a)
	e.mu.Unlock()
	if left != "" {
		e.DrainStage(left)
	}
	return err
}

func (e *Engine) rejoinLocked(job Job, a Again) (string, error) {
	cardID := job.Card.ID
	card, err := e.store.Card(cardID)
	if err != nil {
		return "", err
	}
	if card.State == model.StateDropped || card.State == model.StateInbox {
		return "", errors.New("the task is no longer in work, so it cannot be moved from here")
	}
	last, ok, err := e.store.LastFlowEvent(cardID)
	if err != nil {
		return "", err
	}
	if !ok || last.FlowID != job.Flow.ID {
		return "", errors.New("the task is on another flow now, so it cannot be moved from here")
	}
	flow, err := e.store.Flow(job.Flow.ID)
	if err != nil {
		return "", err
	}
	from, ok := flow.Stage(job.Stage.ID)
	if !ok {
		return "", fmt.Errorf("the stage «%s» is no longer in the flow", job.Stage.Name)
	}
	// A run that is still the card's work speaks for itself, as finish_step
	// always has. Any other run is a terminal the card has left, and an agent
	// there that decided on its own to finish again would pull the card away
	// from the work in front of the person. What is checked is the person's
	// own word in that terminal after it stopped being the card's work.
	if last.ID != job.Visit || !a.Reported.IsZero() {
		over := a.Reported
		if left, ok := e.visitLeftAt(cardID, job.Visit); ok && left.After(over) {
			over = left
		}
		if a.Asked.IsZero() || !a.Asked.After(over) {
			return "", errors.New("this step is already over, and the person has not asked for anything in this terminal since. " +
				"Do not move the task on your own: tell the person what you would do and wait for them")
		}
	}
	if e.Publishing(cardID) {
		return "", errors.New("a publication of this task is under way; try again when it ends")
	}

	on := a.Outcome
	var next model.Stage
	var detail msg.Msg
	if a.Target != "" {
		next, ok = stageNamed(flow, a.Target)
		if !ok {
			return "", fmt.Errorf("there is no stage «%s» in the flow. The stages are: %s", a.Target, stageNames(flow))
		}
		on = ""
		if delivered := ParseWrites(a.Text, from.Writes); len(delivered) > 0 {
			e.writeDelivered(cardID, from, delivered)
		}
		detail = msg.New("move.byAgent", "stage", from.Name, "agent", job.Agent.Name)
	} else {
		on, detail = e.harvestWritesFor(cardID, from, on, msg.Msg{}, a.Text)
		e.writeOutcome(cardID, on)
		if !flow.HasEdge(from.ID, on) {
			return "", fmt.Errorf("the flow has no way on from «%s» for this result. "+
				"Ask the person where the task goes, then call move_to", from.Name)
		}
		if card, err = e.store.Card(cardID); err != nil {
			return "", err
		}
		var cond *model.Cond
		if next, cond, ok = flow.Next(from.ID, on, card.Props, a.Text); !ok {
			return "", fmt.Errorf("none of the ways on from «%s» fits the task as it stands. "+
				"Ask the person where the task goes, then call move_to", from.Name)
		}
		if detail.IsZero() {
			detail = msg.New("move.again", "stage", from.Name)
		}
		detail = withCond(detail, cond)
	}

	left := ""
	if st, onFlow, err := e.store.FlowState(cardID); err == nil && onFlow {
		left = st.StageID
	}
	e.dequeue(cardID)
	e.enterStage(card, flow, next, on, detail)
	return left, nil
}

// Current reports whether a visit is still the card's current one. A run with
// no visit is judged by nothing else either (FinishedVisit).
func (e *Engine) Current(cardID string, visit int64) bool {
	if visit == 0 {
		return true
	}
	last, ok, err := e.store.LastFlowEvent(cardID)
	return err == nil && ok && last.ID == visit
}

func (e *Engine) live(sessionID string) bool {
	return e.runner != nil && e.runner.Live(sessionID)
}

// visitLeftAt is when the card left a visit: the time of the transition after
// it. Not left while it is still the last one.
func (e *Engine) visitLeftAt(cardID string, visit int64) (time.Time, bool) {
	events, err := e.store.FlowEvents(cardID)
	if err != nil {
		return time.Time{}, false
	}
	for _, ev := range events {
		if ev.ID > visit {
			return ev.CreatedAt, true
		}
	}
	return time.Time{}, false
}

// stageNamed is a stage as an agent names it: by the name the board shows,
// quoted or not, or by id.
func stageNamed(flow model.Flow, name string) (model.Stage, bool) {
	name = strings.Trim(strings.TrimSpace(name), "«»\"'")
	if s, ok := flow.Stage(name); ok {
		return s, true
	}
	return flow.StageByName(name)
}

func stageNames(flow model.Flow) string {
	names := make([]string, 0, len(flow.Stages))
	for _, s := range flow.Stages {
		names = append(names, "«"+s.Name+"»")
	}
	return strings.Join(names, ", ")
}
