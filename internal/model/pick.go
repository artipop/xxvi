package model

import (
	"errors"
	"fmt"
	"strings"
)

// Who picks a card up on a stage. This is a pure decision — given the stage's
// crew, the registry, the card and who is busy, there is exactly one answer —
// so it lives here rather than inside the engine, where it could only be tested
// with processes running.

// ErrCrewBusy is not a failure: every member of the stage's crew is already
// working, so the card waits for a free one instead of taking its failure edge.
var ErrCrewBusy = errors.New("состав стадии занят")

// TakenByHumanError is why a stage did not start: the card is somebody's.
// It is not a failure — the work is being done, only not by us — so the card
// waits where it stands rather than taking a failure edge.
type TakenByHumanError struct {
	Who string
}

func (e TakenByHumanError) Error() string {
	return fmt.Sprintf("карточка назначена на %s", e.Who)
}

// PickAgent decides who runs a stage's session for a card.
//
// crew is the stage's own list. It is not a pin but a membership list: it says
// who may work this stage at all, and the card chooses among them. Order:
//
//  1. the card's own choice — its assignee — narrowed to the crew when there is
//     one. That is how a person says "let this agent do it";
//  2. the crew itself: the first member with nothing else running, in the order
//     the crew was listed, so the choice is repeatable and the first name is
//     the one that normally works;
//  3. the single registered agent, when the crew is empty and exactly one
//     exists.
//
// busy is the set of agent names with a live session, folded through Username.
func PickAgent(card Card, crew []string, agents []Agent, busy map[string]bool) (Agent, error) {
	// Asked first: a card somebody took for themselves is theirs, and there is
	// no point working out which agent would not be running it.
	if who := HumanAssignee(card, agents); who != "" {
		return Agent{}, TakenByHumanError{Who: who}
	}

	roster, err := CrewOf(crew, agents)
	if err != nil {
		return Agent{}, err
	}

	if assignee := strings.TrimSpace(card.Assignee); assignee != "" {
		for _, a := range roster {
			if SameAgentName(assignee, a.Name) {
				return a, nil
			}
		}
	}

	if len(crew) > 0 {
		for _, a := range roster {
			if !busy[Username(a.Name)] {
				return a, nil
			}
		}
		return Agent{}, ErrCrewBusy
	}

	switch len(agents) {
	case 0:
		return Agent{}, fmt.Errorf("не зарегистрирован ни один агент")
	case 1:
		return agents[0], nil
	default:
		return Agent{}, fmt.Errorf(
			"не удалось выбрать агента: назначьте агента исполнителем карточки или задайте состав стадии (доступно: %s)",
			AgentNames(agents))
	}
}

// CrewOf resolves the names of a stage's crew against the registry. Unknown
// names are skipped — a registry edited after the stage was configured should
// not stop the flow — but a crew where nobody is left is a mistake worth
// reporting rather than quietly ignoring.
func CrewOf(crew []string, agents []Agent) ([]Agent, error) {
	if len(crew) == 0 {
		return agents, nil
	}
	roster := make([]Agent, 0, len(crew))
	for _, name := range crew {
		for _, a := range agents {
			if SameAgentName(name, a.Name) {
				roster = append(roster, a)
				break
			}
		}
	}
	if len(roster) == 0 {
		return nil, fmt.Errorf("состав стадии (%s) не найден в реестре агентов (%s)",
			strings.Join(crew, ", "), AgentNames(agents))
	}
	return roster, nil
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
