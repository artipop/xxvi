package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// What an agent is told, composed in one place so the runner has nothing to
// decide and so the order is a rule rather than a habit.
//
// A stage in the background gets it all in one message (ComposePrompt): nobody
// reads it but the agent. A stage in a terminal is a person and a CLI at one
// table, and there the first message is what the person would have typed
// (TerminalOpening) — everything the route needs from the agent goes to the
// stage's MCP server as instructions (TerminalBrief), where the CLI reads it
// without it standing in the conversation as somebody's words.
//
// ComposePrompt's order:
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
//
// Last, the language the person reads the app in (lang, empty for none). It is
// the app's and not the agent's: the same agent is read in whatever language
// the screen is showing, and a rule every agent needs is not a line to copy
// into each of them.
func ComposePrompt(card model.Card, flow model.Flow, stage model.Stage, agent model.Agent, arrival, lang string) string {
	var b strings.Builder
	write := func(text string) {
		if text = strings.TrimSpace(text); text != "" {
			b.WriteString(text)
			b.WriteString("\n\n")
		}
	}
	write(agent.Prompt)
	write(stage.Prompt)

	fmt.Fprintf(&b, "Task: %s\n", strings.TrimSpace(card.Title))
	if body := strings.TrimSpace(card.Body); body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	if url := strings.TrimSpace(card.URL); url != "" {
		fmt.Fprintf(&b, "\nSource: %s\n", url)
	}
	if props := describeProps(card.Props); props != "" {
		fmt.Fprintf(&b, "\nCard properties:\n%s", props)
	}

	if note := workModeNote(card); note != "" {
		b.WriteString("\n")
		b.WriteString(note)
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
	if note := languageNote(lang); note != "" {
		b.WriteString("\n")
		b.WriteString(note)
	}
	return strings.TrimSpace(b.String()) + "\n"
}

// TerminalOpening is the first message a terminal stage's CLI gets: what a
// person would have typed into it. A task typed in the ribbon is sent as it was
// typed, and one typed without a word opens the CLI with nothing said. A card
// that arrived — from a source, or written into the inbox — is sent as it was
// written there. A card sent back to this stage also says why: that is news the
// agent has to answer, not a rule of the route.
func TerminalOpening(card model.Card, arrival string) string {
	var parts []string
	if card.Typed {
		parts = append(parts, strings.TrimSpace(card.Body))
	} else {
		parts = append(parts, strings.TrimSpace(card.Title), strings.TrimSpace(card.Body), strings.TrimSpace(card.URL))
	}
	parts = append(parts, strings.TrimSpace(arrival))
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}

// TerminalBrief is the rest of what ComposePrompt would say, for the stage's
// MCP server to hand the CLI as instructions: the agent's and the stage's
// prompts, the card's properties and branch, what earlier stages left, which
// closing words route the card, the person's language. How the step ends and
// what it must bring is the server's own part of those instructions
// (stagemcp), so it is not repeated here.
func TerminalBrief(card model.Card, flow model.Flow, stage model.Stage, agent model.Agent, lang string) string {
	var parts []string
	add := func(text string) {
		if text = strings.TrimSpace(text); text != "" {
			parts = append(parts, text)
		}
	}
	add(agent.Prompt)
	add(stage.Prompt)
	if props := describeProps(card.Props); props != "" {
		add("Card properties:\n" + props)
	}
	add(workModeNote(card))
	add(StageInputs(card, flow, stage))
	add(outcomeHint(flow, stage))
	add(languageNote(lang))
	return strings.Join(parts, "\n\n")
}

// workModeNote says where the card's work already is. A card with a branch of
// its own is on it when the agent starts: the application made it. An agent
// that cut another one — which a stage asking for «Branch» invites — would
// leave the work where nothing looks.
func workModeNote(card model.Card) string {
	switch {
	case card.WorkMode == model.WorkModeReview:
		return "This is somebody else's merge request, checked out at its last commit in a working tree of its own. " +
			"Read it and run it; do not commit, push or switch branches — the work is theirs.\n"
	case card.WorkMode != model.WorkModeFolder:
		note := "You are already on this task's branch — do not create another one or switch away; commit here."
		if card.Branch != "" {
			note += fmt.Sprintf(" Branch: %s.", card.Branch)
		}
		return note + "\n"
	}
	return ""
}

// languageNote covers messages only: a repository says in what language its
// code, comments and commits are written, and a brief overriding that would be
// wrong in every repository with a rule of its own.
func languageNote(lang string) string {
	if lang = strings.TrimSpace(lang); lang == "" {
		return ""
	}
	return fmt.Sprintf("Write your messages to the person in %s; code, comments and commits follow the repository's own conventions.\n", lang)
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
		"Where the card goes next depends on your last message: "+
			"end it with the words %s if that is the case — otherwise the card takes another branch.\n",
		strings.Join(phrases, " or "))
}

// StageInputs is the stage's reads, valued: what an earlier stage wrote onto the
// card — a preview address, a reviewer's verdict — handed to the agent in its
// brief instead of hoping it goes looking for it.
//
// A read with no value is left out: on a route that loops, the stages after
// this one count as ahead of it too, and a first visit listed every value a
// later check would write, all of them empty. Which properties those are is
// the flow's answer and not the stage's alone: a stage that declares no reads is
// handed whatever the stages ahead of it declare they write (Flow.ReadsFor).
func StageInputs(card model.Card, flow model.Flow, stage model.Stage) string {
	var b strings.Builder
	for _, name := range flow.ReadsFor(stage.ID) {
		if value := model.PropValue(card.Props, name); strings.TrimSpace(value) != "" {
			fmt.Fprintf(&b, "\n- %s: %s", name, value)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "From the card:" + b.String()
}

// StageOutputs is the stage's declared writes, said as the contract they are.
//
// The agent has no tool to put a value on the card here — it has its closing
// words, which is already how a stage lets the agent route the card
// (outcomeHint). So the contract is stated in the same currency: end the message
// with one `Property: value` line per declared output, and the engine reads
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
	b.WriteString("This stage has to put values on the card. End your message with lines of the form «Property: value», one for each:")
	for _, w := range stage.Writes {
		fmt.Fprintf(&b, "\n- %s", w.Property)
		if w.Required {
			b.WriteString(" (required — the step does not finish without it)")
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
	detail := DescribeMove(event.Detail, event.On)
	if revisit {
		return fmt.Sprintf("The card came back here from the stage «%s»: %s. Take this into account.", from.Name, detail)
	}
	return fmt.Sprintf("The card came from the stage «%s»: %s. Take this into account.", from.Name, detail)
}

// DescribeMove is why a card moved, for an agent to read. The UI words the same
// message in the person's language; an agent is told in English, which is what
// every prompt here is written in, so the codes a move can carry are spelled out
// once more here rather than borrowed from a screen.
func DescribeMove(m *msg.Msg, on string) string {
	if m == nil || m.IsZero() {
		return describeTrigger(on)
	}
	var text string
	switch m.Code {
	case "move.taken":
		text = "taken into work"
	case "move.byHand":
		text = "moved by hand"
	case "move.cardChanged":
		text = fmt.Sprintf("«%s» was set to «%s» on the card", m.Arg("property"), m.Arg("value"))
	case "move.on":
		text = describeTrigger(m.Arg("on"))
	case "outcome.agentDone":
		text = "the agent finished its work"
	case "outcome.terminalFailed":
		text = "the step in the terminal failed"
	case "outcome.sessionFailed":
		text = "the agent's session failed"
	case "outcome.reported":
		text = "the step was reported"
	case "outcome.notStarted":
		text = "the step could not be started"
	case "outcome.requiredMissing":
		text = "required values were not written: " + m.Arg("properties")
	case "outcome.published":
		text = "the branch was pushed and the MR " + m.Arg("mr") + " is open"
	case "outcome.approved":
		text = "the MR " + m.Arg("mr") + " was approved"
	case "outcome.changesRequested":
		text = "changes were requested in the MR " + m.Arg("mr")
	case "outcome.hostingFailed":
		text = "the hosting step failed"
	case msg.CodeText:
		text = m.Arg("text")
	default:
		text = describeTrigger(on)
	}
	switch {
	case m.Arg("ifComment") != "":
		text += fmt.Sprintf(", the agent's answer contains «%s»", m.Arg("ifComment"))
	case m.Arg("ifProperty") != "":
		text += fmt.Sprintf(", «%s» = «%s»", m.Arg("ifProperty"), m.Arg("ifValue"))
	}
	return text
}

func describeTrigger(on string) string {
	switch on {
	case model.TriggerSuccess:
		return "the step passed"
	case model.TriggerFailure:
		return "the step failed"
	case model.TriggerCardChanged:
		return "a value was set on the card"
	case model.TriggerMRMerged:
		return "the MR was merged"
	case model.TriggerMRClosed:
		return "the MR was closed without merging"
	case model.TriggerMRUpdated:
		return "new commits arrived in the MR"
	}
	return on
}
