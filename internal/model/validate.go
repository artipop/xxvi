package model

import (
	"fmt"
	"path"
	"strings"
)

// Validation of a whole flow, in one place, because that is how it is saved:
// the editor hands over the entire graph and the engine checks it before it
// takes it. A transition leading nowhere, two stages of one name, an agent that
// is not registered — each is refused with a sentence saying which.
//
// The list this implements is docs/system.md §10.

// ValidateFlow normalizes and checks a flow against the agent registry. The
// returned flow is the one to store: trimmed, with crews resolved to registry
// spelling and stage ids settled.
func ValidateFlow(f Flow, agents []Agent) (Flow, error) {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" {
		return Flow{}, fmt.Errorf("имя флоу не может быть пустым")
	}
	f.Description = strings.TrimSpace(f.Description)
	if len(f.Stages) == 0 {
		return Flow{}, fmt.Errorf("флоу «%s» не содержит ни одной стадии", f.Name)
	}

	seenID := make(map[string]bool, len(f.Stages))
	seenName := make(map[string]bool, len(f.Stages))
	for i, s := range f.Stages {
		s.ID = strings.TrimSpace(s.ID)
		s.Name = strings.TrimSpace(s.Name)
		s.Action = strings.TrimSpace(s.Action)
		s.Prompt = strings.TrimSpace(s.Prompt)

		if s.ID == "" {
			return Flow{}, fmt.Errorf("у стадии %d нет идентификатора", i+1)
		}
		if seenID[s.ID] {
			return Flow{}, fmt.Errorf("идентификатор стадии «%s» встречается дважды", s.ID)
		}
		if s.Name == "" {
			return Flow{}, fmt.Errorf("у стадии %d нет названия", i+1)
		}
		lower := strings.ToLower(s.Name)
		if seenName[lower] {
			return Flow{}, fmt.Errorf("две стадии называются «%s» — карточка не сможет сказать, где она", s.Name)
		}
		if s.Action == "" {
			s.Action = ActionNone
		}
		switch s.Action {
		case ActionNone, ActionAgent:
		default:
			return Flow{}, fmt.Errorf("неизвестное действие «%s» у стадии «%s» (допустимо: %s)",
				s.Action, s.Name, strings.Join(Actions, ", "))
		}
		if s.MaxRunning < 0 {
			return Flow{}, fmt.Errorf("лимит одновременных сессий стадии «%s» не может быть отрицательным", s.Name)
		}

		crew, err := normalizeCrew(s.Crew, agents)
		if err != nil {
			return Flow{}, fmt.Errorf("стадия «%s»: %w", s.Name, err)
		}
		s.Crew = crew

		// A stage that runs an agent has to be able to find one. With a crew
		// that is already checked above; without one the card falls back to
		// "the only registered agent", and that has to actually be true.
		if s.Action == ActionAgent && len(s.Crew) == 0 && len(agents) != 1 {
			if len(agents) == 0 {
				return Flow{}, fmt.Errorf("стадия «%s» запускает агента, но ни один агент не зарегистрирован", s.Name)
			}
			return Flow{}, fmt.Errorf("у стадии «%s» не задан состав, а агентов несколько (%s) — выбирать будет не из чего",
				s.Name, AgentNames(agents))
		}
		// A final stage is where a card stops. Running something there would
		// produce an outcome with nowhere to go.
		if s.Final && s.Action != ActionNone {
			return Flow{}, fmt.Errorf("финальная стадия «%s» ничего не делает — уберите действие «%s»", s.Name, s.Action)
		}

		writes, err := normalizeWrites(s.Writes)
		if err != nil {
			return Flow{}, fmt.Errorf("стадия «%s»: %w", s.Name, err)
		}
		s.Writes = writes
		s.Reads = normalizeReads(s.Reads)

		// Only a stage that runs something can produce a value. A stage that
		// waits gets its answer from a person, and that answer is the card's own
		// property — declared nowhere, because nobody is being told to write it.
		if len(s.Writes) > 0 && s.Action != ActionAgent {
			return Flow{}, fmt.Errorf("стадия «%s» ничего не запускает — писать на карточку там некому", s.Name)
		}
		if len(s.Reads) > 0 && s.Action != ActionAgent {
			return Flow{}, fmt.Errorf("стадия «%s» ничего не запускает — читать с карточки там некому", s.Name)
		}

		// Screens are checked but not restricted by action: unlike writes and
		// reads they are about the person looking rather than about the card,
		// and a stage where nothing runs is exactly where somebody is looking
		// (docs/system.md §11.4).
		screens, err := normalizeScreens(s.Screens)
		if err != nil {
			return Flow{}, fmt.Errorf("стадия «%s»: %w", s.Name, err)
		}
		s.Screens = screens

		seenID[s.ID], seenName[lower] = true, true
		f.Stages[i] = s
	}

	f.EntryStage = strings.TrimSpace(f.EntryStage)
	if f.EntryStage == "" {
		return Flow{}, fmt.Errorf("во флоу «%s» не указана входная стадия", f.Name)
	}
	if !seenID[f.EntryStage] {
		return Flow{}, fmt.Errorf("входная стадия «%s» отсутствует во флоу", f.EntryStage)
	}

	// Several conditional edges may share one (from, on) — the conditions tell
	// them apart. What stays ambiguous, and refused, is two edges with nothing
	// to tell them apart: two unconditional ones.
	seenFallback := make(map[string]bool, len(f.Edges))
	for i, e := range f.Edges {
		e.ID = strings.TrimSpace(e.ID)
		e.From = strings.TrimSpace(e.From)
		e.To = strings.TrimSpace(e.To)
		e.On = strings.TrimSpace(e.On)

		if !seenID[e.From] {
			return Flow{}, fmt.Errorf("переход ведёт из несуществующей стадии «%s»", e.From)
		}
		if !seenID[e.To] {
			return Flow{}, fmt.Errorf("переход из «%s» ведёт в несуществующую стадию «%s»", stageName(f, e.From), e.To)
		}
		trigger, ok := TriggerByKind(e.On)
		if !ok {
			return Flow{}, fmt.Errorf("неизвестное событие перехода «%s»", e.On)
		}
		if from, _ := f.Stage(e.From); from.Final {
			return Flow{}, fmt.Errorf("из финальной стадии «%s» не может быть переходов", from.Name)
		}
		if e.If.IsZero() {
			e.If = nil
		}
		if e.If != nil {
			cond, err := validateCond(*e.If, trigger)
			if err != nil {
				return Flow{}, fmt.Errorf("переход из «%s» по событию «%s»: %w",
					stageName(f, e.From), TriggerLabel(e.On), err)
			}
			e.If = &cond
		}
		// For card.changed the condition is not a guard but the event itself:
		// an edge that does not say which value fires it waits for nothing.
		if e.On == TriggerCardChanged && e.If == nil {
			return Flow{}, fmt.Errorf("переход «%s» из «%s» должен говорить, какое значение его запускает",
				TriggerLabel(e.On), stageName(f, e.From))
		}
		if e.If == nil {
			key := e.From + "|" + e.On
			if seenFallback[key] {
				return Flow{}, fmt.Errorf("у стадии «%s» два перехода по событию «%s» без условий — куда ехать, непонятно",
					stageName(f, e.From), TriggerLabel(e.On))
			}
			seenFallback[key] = true
		}
		f.Edges[i] = e
	}
	return f, nil
}

// validateCond normalizes one condition and checks it makes sense on its
// trigger: the agent's words exist only where an agent just spoke — on the
// stage's own outcome.
func validateCond(c Cond, trigger Trigger) (Cond, error) {
	c.Property = strings.TrimSpace(c.Property)
	c.Value = strings.TrimSpace(c.Value)
	c.CommentContains = strings.TrimSpace(c.CommentContains)

	hasProp := c.Property != "" || c.Value != ""
	hasComment := c.CommentContains != ""
	switch {
	case hasProp && hasComment:
		return Cond{}, fmt.Errorf("условие либо про свойство карточки, либо про ответ агента — не оба сразу")
	case !hasProp && !hasComment:
		return Cond{}, fmt.Errorf("пустое условие")
	case hasProp && (c.Property == "" || c.Value == ""):
		return Cond{}, fmt.Errorf("условию нужны и свойство, и значение")
	case hasComment && trigger.Source != SourceOutcome:
		return Cond{}, fmt.Errorf("условие про ответ агента возможно только на исходе шага — здесь агент ничего не говорил")
	}
	return c, nil
}

// normalizeCrew resolves crew names to registry spelling, dropping repeats and
// refusing names nobody answers to. A stage naming an unregistered agent is
// worth refusing at save time: at run time it would be a card that silently
// never starts.
func normalizeCrew(crew []string, agents []Agent) ([]string, error) {
	out := make([]string, 0, len(crew))
	seen := make(map[string]bool, len(crew))
	for _, name := range crew {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var entry *Agent
		for i := range agents {
			if SameAgentName(name, agents[i].Name) {
				entry = &agents[i]
				break
			}
		}
		if entry == nil {
			return nil, fmt.Errorf("агент «%s» не найден в реестре (%s)", name, AgentNames(agents))
		}
		if seen[Username(entry.Name)] {
			continue
		}
		seen[Username(entry.Name)] = true
		out = append(out, entry.Name)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// normalizeWrites trims a stage's declared outputs and refuses the two things
// that would be a lie: the same property twice, and the outcome field.
//
// The outcome is refused rather than dropped. The engine writes it after every
// stage without anybody declaring it, so a stage that declares it is a person
// who believes they are turning something on — and a silently ignored
// declaration is the kind of thing found out an afternoon later.
func normalizeWrites(writes []PropertyWrite) ([]PropertyWrite, error) {
	out := make([]PropertyWrite, 0, len(writes))
	seen := make(map[string]bool, len(writes))
	for _, w := range writes {
		w.Property = strings.TrimSpace(w.Property)
		if w.Property == "" {
			continue
		}
		if IsOutcomeProperty(w.Property) {
			return nil, fmt.Errorf("«%s» пишется само после каждой стадии — объявлять его не нужно", OutcomeProperty)
		}
		key := strings.ToLower(w.Property)
		if seen[key] {
			return nil, fmt.Errorf("свойство «%s» объявлено дважды", w.Property)
		}
		seen[key] = true
		out = append(out, w)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// normalizeScreens trims a stage's screens and refuses the ones that could not
// be shown.
//
// A repeat is refused rather than dropped, unlike a repeated read: two
// identical screens are two windows on the strip, and a person who asked for
// them twice meant something by it — most likely a typo in one of the two.
//
// The notes path is confined here rather than only where it is read, because a
// path that escapes the card's folder is a mistake in the flow, and the place
// to say so is the editor rather than the moment somebody opens the screen.
func normalizeScreens(screens []Screen) ([]Screen, error) {
	out := make([]Screen, 0, len(screens))
	seen := make(map[string]bool, len(screens))
	for _, sc := range screens {
		sc.Kind = strings.TrimSpace(sc.Kind)
		sc.Title = strings.TrimSpace(sc.Title)
		sc.Ref = strings.TrimSpace(sc.Ref)
		if sc.Kind == "" && sc.Ref == "" {
			continue
		}
		if !isScreenKind(sc.Kind) {
			return nil, fmt.Errorf("неизвестный вид экрана «%s»", sc.Kind)
		}
		// A terminal without a command is a shell in the card's folder, and
		// that is a screen worth having. The other two point at something, and
		// without it there is nothing to open.
		if sc.Ref == "" && sc.Kind != ScreenTerminal {
			return nil, fmt.Errorf("экран «%s» не говорит, что показывать", ScreenKindLabel(sc.Kind))
		}
		if sc.Kind == ScreenNotes {
			if err := checkNotesPath(sc.Ref); err != nil {
				return nil, err
			}
		}
		key := strings.ToLower(sc.Kind + "\x00" + sc.Ref)
		if seen[key] {
			return nil, fmt.Errorf("экран «%s» на «%s» объявлен дважды", ScreenKindLabel(sc.Kind), sc.Ref)
		}
		seen[key] = true
		out = append(out, sc)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func isScreenKind(kind string) bool {
	for _, k := range ScreenKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// checkNotesPath keeps a notes screen inside the card's working folder — the
// same folder the agent is confined to, so both sides of one file have the same
// boundary.
// The path is judged in slashes rather than with path/filepath, because it is
// stored in a flow and read on whichever machine opens it: a rule that means one
// thing on macOS and another on Windows would let a flow travel and change its
// mind.
func checkNotesPath(ref string) error {
	slashed := strings.ReplaceAll(ref, `\`, "/")
	abs := strings.HasPrefix(slashed, "/")
	// A Windows drive letter is absolute even without a leading slash.
	if len(slashed) >= 2 && slashed[1] == ':' {
		abs = true
	}
	if abs {
		return fmt.Errorf("путь к заметкам «%s» должен быть относительным — он лежит в папке карточки", ref)
	}
	// Checked on the cleaned path so that "a/../../b" is caught as well as the
	// plainly written "../b".
	clean := path.Clean(slashed)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("путь к заметкам «%s» выходит из папки карточки", ref)
	}
	return nil
}

// normalizeReads trims a stage's declared inputs and drops repeats. Repeats are
// dropped rather than refused: a read asks for nothing, so naming one twice is
// untidy rather than wrong.
func normalizeReads(reads []string) []string {
	out := make([]string, 0, len(reads))
	seen := make(map[string]bool, len(reads))
	for _, name := range reads {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stageName is a stage's name for an error message, falling back to its id for
// a graph that names something that is not there.
func stageName(f Flow, id string) string {
	if s, ok := f.Stage(id); ok {
		return s.Name
	}
	return id
}
