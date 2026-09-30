package model

import (
	"regexp"
	"strings"

	"github.com/artipop/xxvi/internal/msg"
)

// A stage template is a kind of node the flow editor offers: «AI in the
// terminal», «Terminal», «Docker», «Web», «Diff». It is a registry like agents
// and projects — rows a person adds and edits — and the standard ones are rows
// too, put there by the migration that made the table, so there is one place to
// look for what the palette holds rather than a list in the code and a list in
// the database.
//
// A template is a preset and nothing more: picking one copies its action, its
// work mode, its brief and its screens onto the stage, and from then on the
// stage owns them. The engine never reads a template. That is what keeps the
// set open without opening what a flow can do: a template can only combine the
// closed sets of actions and screens a stage already has, and it is checked
// against them the same way a stage is.
type StageTemplate struct {
	ID string `json:"id"`
	// Name and Description are what the palette card says. Empty on a builtin
	// template means «the application's own words», so the card follows the
	// language; a person renaming it writes over that.
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Icon names a file among the node icons the frontend ships
	// (frontend/public/node-icons/<icon>.svg). A name rather than the picture:
	// the icons are drawn as one set, and a template picks from it.
	Icon string `json:"icon,omitempty"`
	// Color is the node's accent, #rrggbb. Empty is the application's own.
	Color string `json:"color,omitempty"`
	// Builtin marks the ones the application put there. Editable and
	// deletable like any other; the mark only says whose words an empty name
	// stands for.
	Builtin bool `json:"builtin,omitempty"`

	// The preset, in the stage's own terms.
	Action  string   `json:"action"`
	Work    string   `json:"work,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Crew    []string `json:"crew,omitempty"`
	Final   bool     `json:"final,omitempty"`
	Screens []Screen `json:"screens,omitempty"`
}

// The builtin templates' ids. Fixed, because a stage saved before templates
// existed is matched to one of them (BuiltinTemplateOf) and the migration that
// seeds them names them.
const (
	TemplateAgent    = "agent"
	TemplateTerminal = "terminal"
	TemplateDocker   = "docker"
	TemplateWeb      = "web"
	TemplateDiff     = "diff"
	TemplateNotes    = "notes"
	TemplateWait     = "wait"
	TemplatePublish  = "publish"
	TemplateVerdict  = "verdict"
	TemplateFinal    = "final"
)

// BuiltinTemplates are the templates a database starts with, in palette order.
// The migration writes exactly these; a test keeps the two in step.
func BuiltinTemplates() []StageTemplate {
	b := func(id, icon, color, action string, screens ...Screen) StageTemplate {
		return StageTemplate{ID: id, Icon: icon, Color: color, Builtin: true, Action: action, Screens: screens}
	}
	agent := b(TemplateAgent, "agent", "#4fb8ad", ActionAgent)
	agent.Work = WorkTerminal
	final := b(TemplateFinal, "final", "#7bc47f", ActionNone)
	final.Final = true
	return []StageTemplate{
		agent,
		b(TemplateTerminal, "terminal", "#c9cdd6", ActionNone, Screen{Kind: ScreenTerminal}),
		b(TemplateDocker, "docker", "#2496ed", ActionNone, Screen{Kind: ScreenTerminal, Ref: "docker compose up"}),
		b(TemplateWeb, "web", "#e0a458", ActionNone, Screen{Kind: ScreenRun, Ref: LaunchWeb}),
		b(TemplateDiff, "diff", "#c38ee0", ActionNone, Screen{Kind: ScreenDiff}),
		b(TemplateNotes, "notes", "#d9c36a", ActionNone, Screen{Kind: ScreenNotes, Ref: "plan.md"}),
		b(TemplateWait, "wait", "#949aab", ActionNone),
		b(TemplatePublish, "publish", "#6aa2e8", ActionPublish),
		b(TemplateVerdict, "verdict", "#6aa2e8", ActionVerdict),
		final,
	}
}

// BuiltinTemplateOf is the builtin template a stage that never named one was
// made from, read off what it does and what it opens first: flows saved before
// templates existed are drawn by it, and so is one written by hand.
func BuiltinTemplateOf(s Stage) string {
	switch {
	case s.Final:
		return TemplateFinal
	case s.Action == ActionAgent:
		return TemplateAgent
	case s.Action == ActionPublish:
		return TemplatePublish
	case s.Action == ActionVerdict:
		return TemplateVerdict
	}
	if len(s.Screens) == 0 {
		return TemplateWait
	}
	sc := s.Screens[0]
	switch sc.Kind {
	case ScreenDiff:
		return TemplateDiff
	case ScreenBrowser, ScreenRun:
		return TemplateWeb
	case ScreenNotes:
		return TemplateNotes
	case ScreenTerminal:
		if strings.HasPrefix(strings.TrimSpace(sc.Ref), "docker") {
			return TemplateDocker
		}
		return TemplateTerminal
	}
	return TemplateWait
}

var (
	iconName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// ValidateTemplate normalizes and checks a template by the rules a stage made
// from it would be checked by, so a template never offers a node the flow then
// refuses. The crew is not resolved against the registry: an agent may be
// registered after the template names it, and the flow says so when saved.
func ValidateTemplate(t StageTemplate) (StageTemplate, error) {
	t.ID = strings.TrimSpace(t.ID)
	t.Name = strings.TrimSpace(t.Name)
	t.Description = strings.TrimSpace(t.Description)
	t.Icon = strings.TrimSpace(t.Icon)
	t.Color = strings.TrimSpace(t.Color)
	t.Action = strings.TrimSpace(t.Action)
	t.Work = strings.TrimSpace(t.Work)
	t.Prompt = strings.TrimSpace(t.Prompt)

	if t.Name == "" && !t.Builtin {
		return StageTemplate{}, msg.Err("template.noName")
	}
	if t.Icon != "" && !iconName.MatchString(t.Icon) {
		return StageTemplate{}, msg.Err("template.badIcon", "icon", t.Icon)
	}
	if t.Color != "" && !hexColor.MatchString(t.Color) {
		return StageTemplate{}, msg.Err("template.badColor", "color", t.Color)
	}
	if t.Action == "" {
		t.Action = ActionNone
	}
	switch t.Action {
	case ActionNone, ActionAgent, ActionPublish, ActionVerdict:
	default:
		return StageTemplate{}, msg.Err("template.unknownAction",
			"action", t.Action, "allowed", strings.Join(Actions, ", "))
	}
	if t.Final && t.Action != ActionNone {
		return StageTemplate{}, msg.Err("template.finalRuns", "action", t.Action)
	}
	switch {
	case t.Action != ActionAgent:
		t.Work, t.Prompt, t.Crew = "", "", nil
	case t.Work == "":
		t.Work = WorkTerminal
	case t.Work != WorkTerminal && t.Work != WorkSession:
		return StageTemplate{}, msg.Err("template.unknownWork",
			"work", t.Work, "allowed", strings.Join(Works, ", "))
	}
	crew := make([]string, 0, len(t.Crew))
	for _, name := range t.Crew {
		if name = strings.TrimSpace(name); name != "" {
			crew = append(crew, name)
		}
	}
	t.Crew = nil
	if len(crew) > 0 {
		t.Crew = crew
	}
	if t.Final {
		t.Screens = nil
	}
	screens, err := normalizeScreens(t.Screens)
	if err != nil {
		return StageTemplate{}, err
	}
	t.Screens = screens
	return t, nil
}
