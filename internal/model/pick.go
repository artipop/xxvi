package model

import (
	"strings"

	"github.com/artipop/xxvi/internal/msg"
)

// Who picks a card up on a stage. This is a pure decision — given the
// registry and the card there is exactly one answer — so it lives here rather
// than inside the engine, where it could only be tested with processes running.

// TakenByHumanError is why a stage did not start: the card is somebody's.
// It is not a failure — the work is being done, only not by us — so the card
// waits where it stands rather than taking a failure edge.
type TakenByHumanError struct {
	Who string
}

func (e TakenByHumanError) Error() string {
	return "card is assigned to " + e.Who
}

// PickAgent decides who runs a stage's session for a card.
//
// A stage is a place where an agent works, with a prompt of its own; which
// agent is not the stage's to say. So it is the card's: its assignee, any
// registered agent, is how a person says "let this agent do it". A card that
// names none is run by the only registered agent, and with several there is
// nobody to guess.
func PickAgent(card Card, agents []Agent) (Agent, error) {
	// Asked first: a card somebody took for themselves is theirs, and there is
	// no point working out which agent would not be running it.
	if who := HumanAssignee(card, agents); who != "" {
		return Agent{}, TakenByHumanError{Who: who}
	}

	if assignee := strings.TrimSpace(card.Assignee); assignee != "" {
		for _, a := range agents {
			if SameAgentName(assignee, a.Name) {
				return a, nil
			}
		}
	}

	switch len(agents) {
	case 0:
		return Agent{}, msg.Err("agent.noneRegistered")
	case 1:
		return agents[0], nil
	default:
		return Agent{}, msg.Err("agent.cannotPick", "agents", AgentNames(agents))
	}
}

// HumanAssignee is who took the card, when that is a person rather than an
// agent. Assigning yourself is how you say "this one is mine": an agent picking
// the same card up would do the same work twice and — on a flow — would move
// the card on the moment it decided it was finished.
//
// An assignee that *is* a registered agent means the opposite (that agent runs
// it), so a card with one is not somebody's in this sense.
func HumanAssignee(card Card, agents []Agent) string {
	who := strings.TrimSpace(card.Assignee)
	if who == "" || IsAgentName(agents, who) {
		return ""
	}
	return who
}
