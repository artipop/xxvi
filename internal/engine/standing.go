package engine

import (
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// Why a card in work stands with the next move a person's. A closed set: the UI
// words each one, and a reason it does not know is a row it cannot explain.
const (
	// StandAnswer: the stage waits for a person to say how it went — a review,
	// an approval, any stage whose way on is a value put on the card.
	StandAnswer = "answer"
	// StandStopped: nothing runs and nothing is awaited. The step failed and the
	// flow has no edge for it, the terminal was closed, the application quit
	// under it, a person took the card — the card is where it was left, and
	// only a person moves it.
	StandStopped = "stopped"
	// StandPaused: the application closed on the stage's terminal and saved
	// its conversation; it goes on when a person says «Continue», and only
	// then (store.StatusPaused).
	StandPaused = "paused"
)

// Standing is a card in work whose next move is a person's, and has been since
// Since. The stage's own questions — an agent asking, a terminal waiting — are
// not here: those are the agents' to report, while this is what the flow
// itself says about where the card stands (docs/system.md §7).
type Standing struct {
	CardID    string    `json:"cardId"`
	CardTitle string    `json:"cardTitle"`
	Stage     string    `json:"stage"`
	Why       string    `json:"why"`
	Problem   *msg.Msg  `json:"problem,omitempty"`
	Since     time.Time `json:"since"`
}

// Standing lists the cards in work that wait on a person. Read off the cards
// every time rather than raised as they stop, so it survives a restart — and a
// restart is exactly when a card is most likely to be left standing.
//
// Everything else in work is somebody else's move: a stage that runs is the
// agent's, a queued card the stage's, and a stage that waits for the hosting is
// the world's. A person can nudge any of those, but none of them is stalled for
// want of one.
func (e *Engine) Standing() ([]Standing, error) {
	cards, err := e.store.CardsInState(model.StateFlow)
	if err != nil {
		return nil, err
	}
	var out []Standing
	for _, c := range cards {
		if s, ok := e.standing(c); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func (e *Engine) standing(card model.Card) (Standing, bool) {
	cf, err := e.CardFlowFor(card.ID)
	if err != nil || cf == nil || cf.Running || cf.Queued {
		return Standing{}, false
	}
	flow, err := e.store.Flow(cf.FlowID)
	if err != nil {
		return Standing{}, false
	}
	stage, ok := flow.Stage(cf.StageID)
	if !ok || stage.Final || flow.WaitsForHosting(stage.ID) {
		return Standing{}, false
	}

	out := Standing{CardID: card.ID, CardTitle: card.Title, Stage: stage.Name, Why: StandStopped, Since: cf.Since}
	for _, w := range cf.WaitingFor {
		if w.On == model.TriggerCardChanged {
			out.Why = StandAnswer
			break
		}
	}

	// What stopped the card, if something did during this visit: the newest
	// problem, or else how its last run ended. Since moves with it — a card
	// that stood on a stage for an hour while its agent worked has been
	// waiting for a person only since the agent stopped.
	if journal, err := e.store.Journal(card.ID); err == nil {
		for _, j := range journal {
			if j.Kind == model.EntryProblem && j.Msg != nil && !j.CreatedAt.Before(cf.Since) {
				out.Problem, out.Since = j.Msg, j.CreatedAt
			}
		}
	}
	var last *store.Session
	if sessions, err := e.store.SessionsForCard(card.ID); err == nil {
		for i, s := range sessions {
			if s.StageID != stage.ID || s.StartedAt.Before(cf.Since) || s.FinishedAt.IsZero() {
				continue
			}
			if s.FinishedAt.After(out.Since) {
				out.Since = s.FinishedAt
			}
			if out.Problem == nil && s.Error != nil {
				out.Problem = s.Error
			}
			if last == nil || s.StartedAt.After(last.StartedAt) {
				last = &sessions[i]
			}
		}
	}
	if last != nil && last.Status == store.StatusPaused {
		// Nothing went wrong, so there is no problem to show: the step is
		// whole, and the row only says it is waiting to be continued.
		out.Why, out.Problem = StandPaused, nil
	}
	return out, true
}
