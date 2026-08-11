package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/artipop/xxvi/internal/model"
)

// What an agent is told, composed in one place so the runner has nothing to
// decide and so the order is a rule rather than a habit:
//
//  1. the agent's own prompt — who it is;
//  2. the stage's prompt — what is being done at this step;
//  3. the card's task — what it is being done to.
//
// Only the third is obligatory. The first two are how a person shapes the work
// without touching the card.
func ComposePrompt(card model.Card, flow model.Flow, stage model.Stage, agent model.Agent) string {
	var b strings.Builder
	write := func(text string) {
		if text = strings.TrimSpace(text); text != "" {
			b.WriteString(text)
			b.WriteString("\n\n")
		}
	}
	write(agent.Prompt)
	write(stage.Prompt)

	fmt.Fprintf(&b, "Задача: %s\n", strings.TrimSpace(card.Title))
	if body := strings.TrimSpace(card.Body); body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	if url := strings.TrimSpace(card.URL); url != "" {
		fmt.Fprintf(&b, "\nИсточник: %s\n", url)
	}
	if props := describeProps(card.Props); props != "" {
		fmt.Fprintf(&b, "\nСвойства карточки:\n%s", props)
	}

	// What the flow will do with the answer, so an agent whose stage routes on
	// its closing words knows that it does. Saying it here rather than making
	// every stage prompt repeat it is the difference between a rule and a
	// convention somebody forgets.
	if hint := outcomeHint(flow, stage); hint != "" {
		b.WriteString("\n")
		b.WriteString(hint)
	}
	return strings.TrimSpace(b.String()) + "\n"
}

// describeProps lists the card's properties in a stable order — a prompt that
// reshuffles itself between runs is a prompt nobody can compare.
func describeProps(props map[string]string) string {
	if len(props) == 0 {
		return ""
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "- %s: %s\n", name, props[name])
	}
	return b.String()
}

// outcomeHint tells the agent which of its own words the flow is listening for.
// Only conditions on this stage's outcomes are its business: a condition on a
// card property is somebody else's answer, and a condition on another stage is
// not this turn.
func outcomeHint(flow model.Flow, stage model.Stage) string {
	var phrases []string
	seen := map[string]bool{}
	for _, e := range flow.OutgoingFrom(stage.ID) {
		if !model.IsOutcome(e.On) || e.If == nil || e.If.CommentContains == "" {
			continue
		}
		if seen[e.If.CommentContains] {
			continue
		}
		seen[e.If.CommentContains] = true
		phrases = append(phrases, fmt.Sprintf("«%s»", e.If.CommentContains))
	}
	if len(phrases) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"Дальнейший маршрут карточки зависит от твоего последнего сообщения: "+
			"закончи его словами %s, если это так — иначе карточка поедет по другой ветке.\n",
		strings.Join(phrases, " или "))
}
