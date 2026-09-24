package app

import (
	"fmt"

	"github.com/artipop/xxvi/internal/model"
)

// What a first run finds: an agent and three flows worth reading as examples.
// No sources: the only ones there were are demos, and an inbox filled by a
// demo is an inbox of tasks nobody set.
//
// It runs only on an empty database. Seeding what somebody has already edited
// would be an application overruling its user, and "restore the examples" is a
// button, not a startup rule.
func (a *App) seed() error {
	empty, err := a.Store.IsEmpty()
	if err != nil {
		return err
	}
	if !empty {
		return nil
	}
	a.log.Info("first run: creating examples", "dir", a.DataDir)

	if _, err := a.Store.SaveAgent(defaultAgent()); err != nil {
		return fmt.Errorf("register the agent: %w", err)
	}
	for _, flow := range SeedFlows() {
		if _, err := a.Store.SaveFlow(flow); err != nil {
			return fmt.Errorf("create the flow %q: %w", flow.Name, err)
		}
	}
	return nil
}

// defaultAgent is the one a fresh install has. Whether it can actually run on
// this machine is a separate question, and the agents screen answers it
// (acp.AdapterStatuses) rather than this deciding for a person.
func defaultAgent() model.Agent {
	return model.Agent{
		Name: "Claude",
		Kind: model.KindClaude,
		Prompt: "You are working on a task from XXVI. " +
			"End your message with a short summary of what was done.",
	}
}

// SeedFlows are the example routes. Together they use every trigger this
// application has, which is what makes them worth reading: «Development» is the
// one where a stage owes the card a value and the next arrow reads it, «Triage
// and decision» is the one where the agent chooses the branch with its own
// words, and «Page and check» is the short one somebody can walk end to end — by hand
// or through the tools this application offers outside (internal/appmcp).
func SeedFlows() []model.Flow {
	return []model.Flow{
		{
			Name: "Development",
			Description: "An agent does the work, an agent checks it, a person decides. " +
				"Whatever fails the check goes back to the agent, not to the person.",
			EntryStage: "dev-work",
			Stages: []model.Stage{
				{
					ID: "dev-work", Name: "In progress", Action: model.ActionAgent, Crew: []string{"Claude"},
					// In the terminal, because this is the step somebody sits
					// at: «ask» means the agent asks in its own interface and
					// is answered in the same window (docs/system.md §4.1.1).
					Work:   model.WorkTerminal,
					Prompt: "Do what the card asks. If something is missing, ask.",
					// What this stage leaves on the card. Not required: a task
					// that needed no branch still finished.
					Writes: []model.PropertyWrite{{Property: "Branch"}},
					// What the ribbon shows while this step runs: the plan the
					// agent keeps, in the card's own folder, so the file it
					// writes is the file a person edits.
					Screens: []model.Screen{{Kind: model.ScreenNotes, Title: "Plan", Ref: "plan.md"}},
					X:       80, Y: 160,
				},
				{
					ID: "dev-check", Name: "Check", Action: model.ActionAgent, Crew: []string{"Claude"},
					// A session: nobody watches a check, and there is nobody for
					// it to talk to. Its verdict is read by the fork below.
					Work: model.WorkSession,
					Prompt: "Check the work. Answer «pass» if it is fine and «fail» if it is not — " +
						"and write what exactly is wrong.",
					// A verdict the fork below reads, and a required one: the
					// stage cannot end without it, because an edge branching on
					// a value nobody set would send the card down the fallback.
					Writes: []model.PropertyWrite{
						{Property: "Verdict", Required: true},
						{Property: "Preview"},
					},
					// The address this stage writes is the address the screen
					// beside it opens — declared output and declared screen are
					// the same currency (docs/system.md §12.3).
					Screens: []model.Screen{
						{Kind: model.ScreenBrowser, Title: "Preview", Ref: "{Preview}"},
						{Kind: model.ScreenTerminal},
					},
					X: 360, Y: 160,
				},
				// A stage where nothing runs still shows something: this is
				// where somebody looks at the preview and decides by it, which
				// is why screens are not tied to an action (docs/system.md §12.4).
				//
				// The diff comes first and points at nothing, which is how it
				// says «what is in the working copy and not in the last commit»
				// — the agent's work, before anybody committed it. The preview
				// is beside it: one screen shows what was written, the other
				// what it does.
				{
					ID: "dev-review", Name: "In review", Action: model.ActionNone,
					Screens: []model.Screen{
						{Kind: model.ScreenDiff, Title: "Changes"},
						{Kind: model.ScreenBrowser, Title: "Preview", Ref: "{Preview}"},
					},
					X: 640, Y: 160,
				},
				{ID: "dev-done", Name: "Done", Final: true, X: 900, Y: 160},
			},
			Edges: []model.Edge{
				{From: "dev-work", To: "dev-check", On: model.TriggerSuccess},

				// The fork reads what the stage before it was obliged to write.
				// Conditional first, fallback second — the order the editor
				// draws them in, though Next does not depend on it.
				{
					From: "dev-check", To: "dev-work", On: model.TriggerSuccess,
					If: &model.Cond{Property: "Verdict", Value: "fail"},
				},
				{From: "dev-check", To: "dev-review", On: model.TriggerSuccess},

				// A review that says no is the one arrow a person draws. Nothing
				// runs on «In review», so it has no failure of its own to leave
				// by — the signal is the reviewer marking the card, in the same
				// field a stage that *does* run writes.
				{
					From: "dev-review", To: "dev-done", On: model.TriggerCardChanged,
					If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomePassed},
				},
				{
					From: "dev-review", To: "dev-work", On: model.TriggerCardChanged,
					If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomeFailed},
				},

				// What has no arrow is not an oversight: an agent stage that
				// failed leaves the card where it stopped, because sending a
				// task back to the agent that has just failed it is a loop with
				// nothing new in it. The card carries its outcome and the reason is
				// in its journal.
			},
		},
		{
			// The short one, and the one worth walking to see what a flow is:
			// something is made, something else checks it, and a person looks
			// at the result before it counts as done. Every stage here leaves
			// the next one what it needs by name, so the route works the same
			// whether the steps are worked by this application's own agents or
			// reported from outside (internal/appmcp).
			Name: "Page and check",
			Description: "An agent makes a page, a check gives a verdict, a person looks at it in the browser and decides. " +
				"A short route that shows a card's whole way.",
			EntryStage: "page-write",
			Stages: []model.Stage{
				{
					ID: "page-write", Name: "Layout", Action: model.ActionAgent, Crew: []string{"Claude"},
					Work: model.WorkTerminal,
					Prompt: "Make the page the card asks for: one index.html file in the working folder, " +
						"with no external dependencies. Put the file's address in «Page» — file:///…/index.html.",
					// Required: the browser screen below opens exactly this
					// value, and a step that ended without it would leave the
					// next stage looking at a blank page.
					Writes:  []model.PropertyWrite{{Property: "Page", Required: true}},
					Screens: []model.Screen{{Kind: model.ScreenNotes, Title: "Plan", Ref: "plan.md"}},
					X:       80, Y: 160,
				},
				{
					ID: "page-review", Name: "Check", Action: model.ActionAgent, Crew: []string{"Claude"},
					Work: model.WorkSession,
					Prompt: "Check the page at «Page»: does it do what the card asks, " +
						"is the markup intact. Put pass or fail in «Verdict», and write what is wrong in the text.",
					Reads:   []string{"Page"},
					Writes:  []model.PropertyWrite{{Property: "Verdict", Required: true}},
					Screens: []model.Screen{{Kind: model.ScreenBrowser, Title: "Page", Ref: "{Page}"}},
					X:       360, Y: 160,
				},
				// Nothing runs here: this is where somebody opens the page and
				// answers for it. The screen is the whole stage.
				{
					ID: "page-look", Name: "Look", Action: model.ActionNone,
					Screens: []model.Screen{{Kind: model.ScreenBrowser, Title: "Page", Ref: "{Page}"}},
					X:       640, Y: 160,
				},
				{ID: "page-done", Name: "Done", Final: true, X: 900, Y: 160},
			},
			Edges: []model.Edge{
				{From: "page-write", To: "page-review", On: model.TriggerSuccess},

				// The check routes the card by the value it was obliged to
				// write: «fail» sends it back to the stage that made the page.
				{
					From: "page-review", To: "page-write", On: model.TriggerSuccess,
					If: &model.Cond{Property: "Verdict", Value: "fail"},
				},
				{From: "page-review", To: "page-look", On: model.TriggerSuccess},

				{
					From: "page-look", To: "page-done", On: model.TriggerCardChanged,
					If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomePassed},
				},
				{
					From: "page-look", To: "page-write", On: model.TriggerCardChanged,
					If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomeFailed},
				},
			},
		},
		{
			Name:        "Triage and decision",
			Description: "The agent works out what to do first. If a person has to decide, the flow asks, not a chat.",
			EntryStage:  "triage",
			Stages: []model.Stage{
				{
					ID: "triage", Name: "Triage", Action: model.ActionAgent, Crew: []string{"Claude"},
					Work: model.WorkTerminal,
					Prompt: "Work out what has to be done and describe a plan. Do not change anything. " +
						"If a person has to choose between options, say so.",
					X: 80, Y: 200,
				},
				{ID: "decide", Name: "Decision needed", Action: model.ActionNone, X: 360, Y: 320},
				{
					ID: "do", Name: "Execution", Action: model.ActionAgent, Crew: []string{"Claude"},
					Work:       model.WorkTerminal,
					Prompt:     "Do what was worked out in the previous step, taking the person's decision into account.",
					MaxRunning: 1,
					X:          640, Y: 200,
				},
				{ID: "triage-done", Name: "Done", Final: true, X: 900, Y: 120},
				{ID: "triage-cancelled", Name: "Cancelled", Final: true, X: 640, Y: 400},
				{ID: "triage-blocked", Name: "Blocked", Final: true, X: 80, Y: 400},
			},
			Edges: []model.Edge{
				// The agent routes the card itself: the condition is on its own
				// closing words, and the prompt is told which words those are
				// (engine.ComposePrompt).
				{
					From: "triage", To: "decide", On: model.TriggerSuccess,
					If: &model.Cond{CommentContains: "DECISION NEEDED"},
				},
				{From: "triage", To: "do", On: model.TriggerSuccess},
				{From: "triage", To: "triage-blocked", On: model.TriggerFailure},

				{
					From: "decide", To: "do", On: model.TriggerCardChanged,
					If: &model.Cond{Property: "Decision", Value: "Go"},
				},
				{
					From: "decide", To: "triage-cancelled", On: model.TriggerCardChanged,
					If: &model.Cond{Property: "Decision", Value: "Cancel"},
				},

				{From: "do", To: "triage-done", On: model.TriggerSuccess},
				{From: "do", To: "triage-blocked", On: model.TriggerFailure},
			},
		},
	}
}
