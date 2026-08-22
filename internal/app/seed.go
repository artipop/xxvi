package app

import (
	"fmt"
	"path/filepath"

	"github.com/artipop/xxvi/internal/inbox"
	"github.com/artipop/xxvi/internal/model"
)

// What a first run finds: an agent, two sources with something in them, and two
// flows worth reading as examples.
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
	a.log.Info("первый запуск: создаются примеры", "dir", a.DataDir)

	if _, err := a.Store.SaveAgent(defaultAgent()); err != nil {
		return fmt.Errorf("зарегистрировать агента: %w", err)
	}
	for _, flow := range SeedFlows() {
		if _, err := a.Store.SaveFlow(flow); err != nil {
			return fmt.Errorf("создать флоу «%s»: %w", flow.Name, err)
		}
	}
	for _, src := range a.seedSources() {
		if _, err := a.Store.SaveSource(src); err != nil {
			return fmt.Errorf("создать источник «%s»: %w", src.Name, err)
		}
	}
	if err := a.seedItems(); err != nil {
		// Examples are a convenience: an application that will not start
		// because it could not write a sample file is worse than one with an
		// empty inbox.
		a.log.Warn("не удалось записать примеры элементов", "err", err)
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
		Prompt: "Ты работаешь над задачей из системы XXVI. " +
			"Отвечай по-русски и заканчивай сообщение коротким выводом о том, что сделано.",
	}
}

// SeedFlows are the two example routes. Together they use every trigger this
// application has, which is what makes them worth reading: «Разработка» is the
// one where a stage owes the card a value and the next arrow reads it, «Разбор и
// решение» is the one where the agent chooses the branch with its own words.
func SeedFlows() []model.Flow {
	return []model.Flow{
		{
			Name: "Разработка",
			Description: "Агент делает, агент проверяет, человек решает. " +
				"Всё, что не прошло, возвращается агенту, а не человеку.",
			EntryStage: "dev-work",
			Stages: []model.Stage{
				{
					ID: "dev-work", Name: "В работе", Action: model.ActionAgent, Crew: []string{"Claude"},
					Prompt: "Сделай то, что просит карточка. Если чего-то не хватает — спроси.",
					// What this stage leaves on the card. Not required: a task
					// that needed no branch still finished.
					Writes: []model.PropertyWrite{{Property: "Ветка"}},
					// What the ribbon shows while this step runs: the plan the
					// agent keeps, in the card's own folder, so the file it
					// writes is the file a person edits.
					Screens: []model.Screen{{Kind: model.ScreenNotes, Title: "План", Ref: "план.md"}},
					X:       80, Y: 160,
				},
				{
					ID: "dev-check", Name: "Проверка", Action: model.ActionAgent, Crew: []string{"Claude"},
					Prompt: "Проверь сделанное. Ответь «pass», если всё хорошо, и «fail», если нет — " +
						"и напиши, что именно не так.",
					// A verdict the fork below reads, and a required one: the
					// stage cannot end without it, because an edge branching on
					// a value nobody set would send the card down the fallback.
					Writes: []model.PropertyWrite{
						{Property: "Вердикт", Required: true},
						{Property: "Превью"},
					},
					// The address this stage writes is the address the screen
					// beside it opens — declared output and declared screen are
					// the same currency (docs/system.md §12.3).
					Screens: []model.Screen{
						{Kind: model.ScreenBrowser, Title: "Превью", Ref: "{Превью}"},
						{Kind: model.ScreenTerminal},
					},
					X: 360, Y: 160,
				},
				// A stage where nothing runs still shows something: this is
				// where somebody looks at the preview and decides by it, which
				// is why screens are not tied to an action (docs/system.md §12.4).
				{
					ID: "dev-review", Name: "На ревью", Action: model.ActionNone,
					Screens: []model.Screen{{Kind: model.ScreenBrowser, Title: "Превью", Ref: "{Превью}"}},
					X:       640, Y: 160,
				},
				{ID: "dev-done", Name: "Готово", Final: true, X: 900, Y: 160},
			},
			Edges: []model.Edge{
				{From: "dev-work", To: "dev-check", On: model.TriggerSuccess},

				// The fork reads what the stage before it was obliged to write.
				// Conditional first, fallback second — the order the editor
				// draws them in, though Next does not depend on it.
				{
					From: "dev-check", To: "dev-work", On: model.TriggerSuccess,
					If: &model.Cond{Property: "Вердикт", Value: "fail"},
				},
				{From: "dev-check", To: "dev-review", On: model.TriggerSuccess},

				// A review that says no is the one arrow a person draws. Nothing
				// runs on «На ревью», so it has no failure of its own to leave
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
				// nothing new in it. The card carries «Исход» and the reason is
				// in its comments.
			},
		},
		{
			Name:        "Разбор и решение",
			Description: "Агент сначала разбирается. Если нужно решение человека — спрашивает флоу, а не в чате.",
			EntryStage:  "triage",
			Stages: []model.Stage{
				{
					ID: "triage", Name: "Разбор", Action: model.ActionAgent, Crew: []string{"Claude"},
					Prompt: "Разберись, что нужно сделать, и опиши план. Ничего не меняй. " +
						"Если выбор между вариантами должен сделать человек, так и напиши.",
					X: 80, Y: 200,
				},
				{ID: "decide", Name: "Нужно решение", Action: model.ActionNone, X: 360, Y: 320},
				{
					ID: "do", Name: "Выполнение", Action: model.ActionAgent, Crew: []string{"Claude"},
					Prompt:     "Сделай то, о чём договорились в комментариях карточки.",
					MaxRunning: 1,
					X:          640, Y: 200,
				},
				{ID: "triage-done", Name: "Готово", Final: true, X: 900, Y: 120},
				{ID: "triage-cancelled", Name: "Отменено", Final: true, X: 640, Y: 400},
				{ID: "triage-blocked", Name: "Заблокировано", Final: true, X: 80, Y: 400},
			},
			Edges: []model.Edge{
				// The agent routes the card itself: the condition is on its own
				// closing words, and the prompt is told which words those are
				// (engine.ComposePrompt).
				{
					From: "triage", To: "decide", On: model.TriggerSuccess,
					If: &model.Cond{CommentContains: "НУЖНО РЕШЕНИЕ"},
				},
				{From: "triage", To: "do", On: model.TriggerSuccess},
				{From: "triage", To: "triage-blocked", On: model.TriggerFailure},

				{
					From: "decide", To: "do", On: model.TriggerCardChanged,
					If: &model.Cond{Property: "Решение", Value: "Делаем"},
				},
				{
					From: "decide", To: "triage-cancelled", On: model.TriggerCardChanged,
					If: &model.Cond{Property: "Решение", Value: "Отменяем"},
				},

				{From: "do", To: "triage-done", On: model.TriggerSuccess},
				{From: "do", To: "triage-blocked", On: model.TriggerFailure},
			},
		},
	}
}

// seedSources are two sources rather than one, because the inbox groups by
// source and a single group shows nothing about that. They also differ in the
// one way sources differ most: what happens to an item no rule matched.
func (a *App) seedSources() []model.Source {
	return []model.Source{
		{
			Name: "Задачи", Plugin: inbox.PluginDemo, Enabled: true, IntervalSeconds: 30,
			Config: map[string]string{inbox.ConfigPath: a.demoPath("tasks")},
			Rules: []model.Rule{
				{
					Name: "срочные",
					When: model.Match{Title: "срочно|упал|сломал"},
					Then: model.ActionCard,
					// A suggestion, not an action: it prefills the «В работу»
					// dialog and starts nothing (docs/system.md §8).
					SuggestFlow: "Разработка",
					Props:       map[string]string{"Приоритет": "срочный"},
				},
				{
					Name:        "остальное",
					Then:        model.ActionCard,
					SuggestFlow: "Разбор и решение",
				},
			},
		},
		{
			// Noisy: a stream of notifications is mostly noise, so here a rule
			// is a subscription and everything else is dropped.
			Name: "Телефон", Plugin: inbox.PluginDemo, Enabled: true, Noisy: true, IntervalSeconds: 30,
			Config: map[string]string{inbox.ConfigPath: a.demoPath("phone")},
			Rules: []model.Rule{
				{
					Name:  "доставка",
					When:  model.Match{Title: "доставк|курьер|посылк"},
					Then:  model.ActionCard,
					Props: map[string]string{"Ссылка": "{{.URL}}"},
				},
			},
		},
	}
}

func (a *App) demoPath(name string) string {
	return filepath.Join(a.DataDir, "sources", name, inbox.DemoFile)
}

// seedItems puts something in the inbox on a first run, so the application
// opens showing what it is rather than an empty screen with a tour.
func (a *App) seedItems() error {
	items := map[string][]model.Item{
		"tasks": {
			{
				ExternalID: "seed-1", Version: "1",
				Title: "Срочно: не открывается страница отчётов",
				Body:  "После вчерашнего обновления отчёты отдают пустую страницу. Смотрели двое, причину не нашли.",
			},
			{
				ExternalID: "seed-2", Version: "1",
				Title: "Переписать импорт справочников",
				Body:  "Импорт написан давно и падает на больших файлах. Нужно решить, переписывать целиком или чинить точечно.",
			},
			{
				ExternalID: "seed-3", Version: "1",
				Title: "Добавить фильтр по дате в список задач",
				Body:  "Просили несколько раз. Ничего сложного, но руки не доходят.",
			},
		},
		"phone": {
			{
				ExternalID: "seed-4", Version: "1",
				Title: "Доставка приедет завтра с 10 до 14",
				Body:  "Курьер позвонит за час.",
				URL:   "https://example.org/delivery/4",
			},
			{
				// Matches no rule on a noisy source, so it is deliberately
				// dropped — which is the thing worth seeing about «шумный».
				ExternalID: "seed-5", Version: "1",
				Title: "Ваша подписка продлена",
				Body:  "Спасибо, что остаётесь с нами.",
			},
		},
	}
	for name, list := range items {
		path := a.demoPath(name)
		for _, item := range list {
			if err := inbox.AppendItem(path, item); err != nil {
				return err
			}
		}
	}
	return nil
}
