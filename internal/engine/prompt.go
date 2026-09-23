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
//
// After them come the three things the *route* knows and the agent cannot: what
// earlier stages left on the card (StageInputs), what this stage owes back
// (StageOutputs), and — for a card that came back here — why (arrival).
func ComposePrompt(card model.Card, flow model.Flow, stage model.Stage, agent model.Agent, arrival string) string {
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

	// A card with a branch of its own is already on it when the agent starts:
	// the application made it. An agent that cut another one — which a stage
	// asking for «Ветка» invites — would leave the work where nothing looks.
	if card.WorkMode != model.WorkModeFolder {
		b.WriteString("\nТы уже на ветке этой задачи — новую ветку не заводи и не переключайся, коммить сюда.")
		if card.Branch != "" {
			fmt.Fprintf(&b, " Ветка: %s.", card.Branch)
		}
		b.WriteString("\n")
	}

	// Why this card is in front of this agent. Only worth saying when it is
	// news: a card arriving on a failure has a reason, and telling the next
	// session what went wrong is the difference between a loop that converges
	// and a loop that repeats.
	if arrival = strings.TrimSpace(arrival); arrival != "" {
		fmt.Fprintf(&b, "\n%s\n", arrival)
	}

	// What earlier stages left, valued, and what this stage owes back.
	if inputs := StageInputs(card, flow, stage); inputs != "" {
		b.WriteString("\n")
		b.WriteString(inputs)
		b.WriteString("\n")
	}
	if outputs := StageOutputs(stage); outputs != "" {
		b.WriteString("\n")
		b.WriteString(outputs)
		b.WriteString("\n")
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

// StageInputs is the stage's reads, valued: what an earlier stage wrote onto the
// card — a preview address, a reviewer's verdict — handed to the agent in its
// brief instead of hoping it goes looking for it.
//
// A read with no value is named as empty rather than dropped, so the agent knows
// the property exists and that nobody filled it. Which properties those are is
// the flow's answer and not the stage's alone: a stage that declares no reads is
// handed whatever the stages ahead of it declare they write (Flow.ReadsFor).
func StageInputs(card model.Card, flow model.Flow, stage model.Stage) string {
	reads := flow.ReadsFor(stage.ID)
	if len(reads) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("С карточки:")
	for _, name := range reads {
		fmt.Fprintf(&b, "\n- %s: %s", name, model.PropValue(card.Props, name))
	}
	return b.String()
}

// StageOutputs is the stage's declared writes, said as the contract they are.
//
// The agent has no tool to put a value on the card here — it has its closing
// words, which is already how a stage lets the agent route the card
// (outcomeHint). So the contract is stated in the same currency: end the message
// with one `Свойство: значение` line per declared output, and the engine reads
// them off before the flow decides anything.
//
// Saying it at all is the point. An agent asked for a verdict it was never told
// about failed a required write on its first attempt to finish, and found out
// what the stage wanted by being refused.
func StageOutputs(stage model.Stage) string {
	if len(stage.Writes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Эта стадия должна записать на карточку. Закончи сообщение строками вида «Свойство: значение», по одной на каждое:")
	for _, w := range stage.Writes {
		fmt.Fprintf(&b, "\n- %s", w.Property)
		if w.Required {
			b.WriteString(" (обязательно — без него шаг не будет закончен)")
		}
	}
	return b.String()
}

// ArrivalNote is why the card is here, for the stage it has just entered. Empty
// for a card moving on: progress needs no explanation, and a note on every
// transition is a note nobody reads.
//
// A card that came back does need one. This is «send what failed back to the
// agent»: without it the next session opens on the same task with no idea that
// it is the second attempt, which is a loop with nothing new in it.
//
// "Came back" is read off the card's own history rather than off the graph —
// revisit is true when the card has stood on this stage before. The graph cannot
// answer it: a route loops on purpose, so every stage of a loop is both ahead of
// and behind every other one. The card's history has one answer.
func ArrivalNote(flow model.Flow, event model.FlowEvent, revisit bool) string {
	if event.FromStage == "" || (event.On != model.TriggerFailure && !revisit) {
		return ""
	}
	from, ok := flow.Stage(event.FromStage)
	if !ok {
		return ""
	}
	detail := strings.TrimSpace(event.Detail)
	if detail == "" {
		detail = model.TriggerLabel(event.On)
	}
	if revisit {
		return fmt.Sprintf("Карточка вернулась сюда со стадии «%s»: %s. Учти это в работе.", from.Name, detail)
	}
	return fmt.Sprintf("Карточка пришла со стадии «%s»: %s. Учти это в работе.", from.Name, detail)
}
