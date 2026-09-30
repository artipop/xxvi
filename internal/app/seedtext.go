package app

import (
	"strings"
)

// The example flows speak the language the person reads the app in. Their
// words are written into the flows themselves rather than looked up when shown:
// a stage's name reaches the ribbon, the journal and every message the backend
// sends about a task, and one translation at the source reaches all of them.
//
// Only what is still as the application wrote it is rewritten — a name or a
// prompt somebody changed is theirs and stays in their words. Property names
// and values («Verdict», «pass») are left alone: an agent answers in them and an
// arrow branches on them, and the UI words them for show (propName, propValue).

// seedText is what one example flow says, in one language.
type seedText struct {
	name, description string
	stages            map[string]stageText
}

// stageText is one stage's words: its name, its prompt, and the titles of its
// screens in the order they are declared.
type stageText struct {
	name, prompt string
	titles       []string
}

// seedTexts are the example flows' words by language, keyed by the flow's entry
// stage — the ids are the application's own and stay put, where the flow's own
// id is generated when it is first saved. English is read off SeedFlows, so the
// words that are seeded and the words they are recognised by are one list.
func seedTexts() map[string]map[string]seedText {
	en := map[string]seedText{}
	for _, f := range SeedFlows() {
		t := seedText{name: f.Name, description: f.Description, stages: map[string]stageText{}}
		for _, s := range f.Stages {
			st := stageText{name: s.Name, prompt: s.Prompt}
			for _, sc := range s.Screens {
				st.titles = append(st.titles, sc.Title)
			}
			t.stages[s.ID] = st
		}
		en[f.EntryStage] = t
	}
	return map[string]map[string]seedText{"en": en, "ru": seedTextsRu}
}

var seedTextsRu = map[string]seedText{
	"dev-work": {
		name: "Разработка",
		description: "Агент делает работу, агент её проверяет, человек решает. " +
			"Что не прошло проверку, возвращается к агенту, а не к человеку.",
		stages: map[string]stageText{
			"dev-work": {name: "В работе", prompt: "Сделай то, что просит задача. Если чего-то не хватает — спроси.",
				titles: []string{"План"}},
			"dev-check": {name: "Проверка",
				prompt: "Проверь работу. Ответь «pass», если всё в порядке, и «fail», если нет, — " +
					"и напиши, что именно не так.",
				titles: []string{"Превью", ""}},
			"dev-review": {name: "На ревью", titles: []string{"Изменения", "Превью"}},
			"dev-done":   {name: "Готово"},
		},
	},
	"page-write": {
		name: "Страница и проверка",
		description: "Агент делает страницу, проверка выносит вердикт, человек смотрит её в браузере и решает. " +
			"Короткий маршрут, на котором видно весь путь задачи.",
		stages: map[string]stageText{
			"page-write": {name: "Вёрстка",
				prompt: "Сделай страницу, которую просит задача: один файл index.html в рабочей папке, " +
					"без внешних зависимостей. Адрес файла положи в «Page» — file:///…/index.html.",
				titles: []string{"План"}},
			"page-review": {name: "Проверка",
				prompt: "Проверь страницу по адресу из «Page»: делает ли она то, что просит задача, " +
					"цела ли вёрстка. В «Verdict» поставь pass или fail, а что не так — напиши текстом.",
				titles: []string{"Страница"}},
			"page-look": {name: "Посмотреть", titles: []string{"Страница"}},
			"page-done": {name: "Готово"},
		},
	},
	"triage": {
		name:        "Разбор и решение",
		description: "Агент разбирается, что делать сначала. Если решать должен человек, спрашивает флоу, а не чат.",
		stages: map[string]stageText{
			"triage": {name: "Разбор",
				prompt: "Разберись, что нужно сделать, и опиши план. Ничего не меняй. " +
					"Если человеку нужно выбрать между вариантами, так и скажи."},
			"decide": {name: "Нужно решение"},
			"do": {name: "Выполнение",
				prompt: "Сделай то, что разобрано на прошлом шаге, с учётом решения человека."},
			"triage-done":      {name: "Готово"},
			"triage-cancelled": {name: "Отменено"},
			"triage-blocked":   {name: "Заблокировано"},
		},
	},
	"tmr-work": {
		name: "Задача в MR",
		description: "Агент делает работу в своей ветке задачи, человек смотрит дифф, " +
			"приложение пушит и открывает MR, и задача ждёт, пока его вольют.",
		stages: map[string]stageText{
			"tmr-work": {name: "В работе",
				prompt: "Сделай то, что просит задача. Когда закончишь, закоммить работу в ветку задачи: " +
					"незакоммиченное в MR не попадёт."},
			"tmr-review":  {name: "На ревью", titles: []string{"Изменения"}},
			"tmr-publish": {name: "MR"},
			"tmr-wait":    {name: "Ждёт вливания", titles: []string{"MR"}},
			"tmr-done":    {name: "Влит"},
			"tmr-closed":  {name: "Закрыт"},
		},
	},
	"rmr-review": {
		name: "Ревью MR",
		description: "Чужой MR, который ждёт вашего ревью: прочитать дифф, запустить и попробовать, " +
			"потом одобрить или вернуть с замечаниями. Новые коммиты возвращают его на ревью.",
		stages: map[string]stageText{
			"rmr-review":  {name: "Ревью", titles: []string{"Изменения", "MR"}},
			"rmr-try":     {name: "Запустить и проверить", titles: []string{""}},
			"rmr-approve": {name: "Одобрить"},
			"rmr-changes": {name: "Запросить правки"},
			"rmr-wait":    {name: "Ждёт автора", titles: []string{"MR"}},
			"rmr-done":    {name: "Влит"},
			"rmr-closed":  {name: "Закрыт"},
		},
	},
}

// seedLangSetting is the language the examples were last put into, for the
// flows seeded after the window last said it (ensureHostingFlows).
const seedLangSetting = "ui.lang.seeds"

// seedLang is the language of the example flows' words for a UI language tag:
// the tag's base, and English for a language they have no words in.
func seedLang(tag string, texts map[string]map[string]seedText) string {
	base := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(base, "-_"); i >= 0 {
		base = base[:i]
	}
	if _, ok := texts[base]; ok {
		return base
	}
	return "en"
}

// localizeSeeds rewrites the example flows into one language. What it
// changed is saved flow by flow; a flow that cannot be saved in the new words —
// a name somebody has already given another flow — keeps the old ones.
func (a *App) localizeSeeds(tag string) (changed bool) {
	texts := seedTexts()
	lang := seedLang(tag, texts)
	flows, err := a.Store.Flows()
	if err != nil {
		a.log.Warn("could not read the flows to translate the examples", "err", err)
		return false
	}
	for _, f := range flows {
		want, ok := texts[lang][f.EntryStage]
		if !ok {
			continue
		}
		// Every language's words for one field, so a flow seeded in one and
		// shown in another is recognised as still the application's.
		pick := func(cur string, of func(seedText) (string, bool), to string) (string, bool) {
			if cur == to {
				return cur, false
			}
			for _, byEntry := range texts {
				if t, ok := byEntry[f.EntryStage]; ok {
					if v, ok := of(t); ok && v == cur {
						return to, true
					}
				}
			}
			return cur, false
		}
		dirty := false
		set := func(field *string, of func(seedText) (string, bool), to string) {
			if v, ok := pick(*field, of, to); ok {
				*field, dirty = v, true
			}
		}
		set(&f.Name, func(t seedText) (string, bool) { return t.name, true }, want.name)
		set(&f.Description, func(t seedText) (string, bool) { return t.description, true }, want.description)
		for i := range f.Stages {
			s := &f.Stages[i]
			ws, ok := want.stages[s.ID]
			if !ok {
				continue
			}
			stage := func(t seedText) (stageText, bool) { st, ok := t.stages[s.ID]; return st, ok }
			set(&s.Name, func(t seedText) (string, bool) { st, ok := stage(t); return st.name, ok }, ws.name)
			set(&s.Prompt, func(t seedText) (string, bool) { st, ok := stage(t); return st.prompt, ok }, ws.prompt)
			for j := range s.Screens {
				if j >= len(ws.titles) {
					break
				}
				j := j
				set(&s.Screens[j].Title, func(t seedText) (string, bool) {
					st, ok := stage(t)
					if !ok || j >= len(st.titles) {
						return "", false
					}
					return st.titles[j], true
				}, ws.titles[j])
			}
		}
		if !dirty {
			continue
		}
		if _, err := a.Store.SaveFlow(f); err != nil {
			a.log.Warn("could not translate an example flow", "flow", f.Name, "lang", lang, "err", err)
			continue
		}
		changed = true
	}
	return changed
}

// LocalizeSeeds puts the example flows into the language the screen resolved
// to — the UI's to say, since «system» is only resolved there. Called whenever
// that language is applied.
func (s *API) LocalizeSeeds(tag string) {
	if err := s.app.Store.SetSetting(seedLangSetting, strings.TrimSpace(tag)); err != nil {
		s.app.log.Warn("could not keep the examples' language", "err", err)
	}
	if s.app.localizeSeeds(tag) {
		s.app.Emit(EventFlows, map[string]any{})
	}
}
