package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// What a screen points at is written with the card's properties in it, and the
// property is looked up the way every name is matched here — trimmed and
// case-folded — so a flow written with «превью» finds what a stage wrote as
// «Превью».
func TestResolveRefPutsCardPropertiesIn(t *testing.T) {
	props := map[string]string{"Превью": "http://localhost:5173", "Ветка": "fix/login"}

	got, waiting := ResolveRef("{Превью}/login", props)
	if got != "http://localhost:5173/login" || len(waiting) != 0 {
		t.Fatalf("свойство должно подставиться целиком: %q, ждёт %v", got, waiting)
	}

	got, _ = ResolveRef("{превью}", props)
	if got != "http://localhost:5173" {
		t.Fatalf("имя свойства ищется так же, как везде: %q", got)
	}

	got, _ = ResolveRef("ветка {Ветка} на {Превью}", props)
	if got != "ветка fix/login на http://localhost:5173" {
		t.Fatalf("несколько свойств в одной ссылке: %q", got)
	}
}

// A value that is not there yet is named rather than filled with emptiness: the
// screen stands blank and says what it is waiting for, which is the difference
// between a window that explains itself and about:blank.
func TestResolveRefNamesWhatItIsWaitingFor(t *testing.T) {
	got, waiting := ResolveRef("{Превью}", map[string]string{})
	if got != "{Превью}" {
		t.Fatalf("несостоявшаяся подстановка не должна превращаться в пустоту: %q", got)
	}
	if len(waiting) != 1 || waiting[0] != "Превью" {
		t.Fatalf("экран должен сказать, какого свойства ждёт: %v", waiting)
	}

	// A property present but empty is the same as absent: the stage declared it
	// and has not delivered it yet.
	_, waiting = ResolveRef("{Превью}", map[string]string{"Превью": "  "})
	if len(waiting) != 1 {
		t.Fatalf("пустое значение — это то же ожидание: %v", waiting)
	}
}

// The braces are the whole syntax. Anything richer would be a script, and
// nothing stored in a flow is interpreted as one.
func TestScreenRefsReadsOnlyBracedNames(t *testing.T) {
	if got := ScreenRefs("http://localhost:5173"); len(got) != 0 {
		t.Fatalf("в адресе без скобок свойств нет: %v", got)
	}
	if got := ScreenRefs("{Превью} и снова {Превью}"); len(got) != 1 {
		t.Fatalf("одно имя названо один раз: %v", got)
	}
	if got := ScreenRefs("{незакрытая"); len(got) != 0 {
		t.Fatalf("незакрытая скобка ничего не называет: %v", got)
	}
}

// The dataflow warning for screens, next to the one for conditions: a screen
// pointing at a property no stage writes is a screen that quietly stays blank.
func TestUnresolvedScreenRefsWarnsAboutWhatNobodyWrites(t *testing.T) {
	f := loopFlow()
	f.Stages[2].Screens = []Screen{{Kind: ScreenBrowser, Ref: "{Превью}"}}

	if got := f.UnresolvedScreenRefs(); len(got) != 0 {
		t.Fatalf("«Превью» пишет стадия деплоя — предупреждать не о чем: %v", got)
	}

	f.Stages[2].Screens = []Screen{{Kind: ScreenBrowser, Ref: "{Стенд}"}}
	got := f.UnresolvedScreenRefs()
	if len(got) != 1 || got[0] != "Стенд" {
		t.Fatalf("экран смотрит на свойство, которого никто не пишет: %v", got)
	}

	// The MR is the application's to write — a publish opens it, a review
	// source brings it — so a screen on it is not a screen on nothing.
	f.Stages[2].Screens = []Screen{{Kind: ScreenBrowser, Ref: "{MR}"}}
	if got := f.UnresolvedScreenRefs(); len(got) != 0 {
		t.Fatalf("«MR» пишет само приложение: %v", got)
	}
	f.Stages[2].Screens = []Screen{{Kind: ScreenBrowser, Ref: "{Стенд}"}}

	// It is a warning, not a refusal: a person may well set the value by hand.
	if _, err := ValidateFlow(f); err != nil {
		t.Fatalf("непрописанное свойство экрана — предупреждение, а не отказ: %v", err)
	}
}

// A stage that runs nothing may still declare screens, unlike writes and reads.
// The subject differs: an output is about the card, a screen is about the person
// looking — and the review stage is exactly where the preview has to be open
// (docs/system.md §12.4).
func TestAWaitingStageMayDeclareScreens(t *testing.T) {
	f := Flow{
		Name: "С ревью", EntryStage: "review",
		Stages: []Stage{
			{ID: "review", Name: "Ревью", Action: ActionNone,
				Screens: []Screen{{Kind: ScreenBrowser, Ref: "http://localhost:5173"}}},
		},
	}
	if _, err := ValidateFlow(f); err != nil {
		t.Fatalf("ждущая стадия имеет право на экран: %v", err)
	}

	// The rule it is deliberately not symmetric with still stands.
	f.Stages[0].Writes = []PropertyWrite{{Property: "Вердикт"}}
	if _, err := ValidateFlow(f); err == nil {
		t.Fatal("писать на карточку на ждущей стадии по-прежнему некому")
	}
}

func TestScreensAreRefusedWhenTheyCouldNotBeShown(t *testing.T) {
	flowWith := func(screens ...Screen) Flow {
		return Flow{
			Name: "Один шаг", EntryStage: "work",
			Stages: []Stage{{ID: "work", Name: "Работа", Action: ActionAgent, Screens: screens}},
		}
	}

	cases := []struct {
		name    string
		screens []Screen
		says    string
	}{
		{"неизвестный вид", []Screen{{Kind: "хрусталь", Ref: "x"}}, "screen.unknownKind"},
		{"браузеру нечего открыть", []Screen{{Kind: ScreenBrowser}}, "screen.noRef"},
		{"заметкам нечего открыть", []Screen{{Kind: ScreenNotes}}, "screen.noRef"},
		{"путь к заметкам абсолютный", []Screen{{Kind: ScreenNotes, Ref: "/etc/passwd"}}, "notes.absolute"},
		{"путь к заметкам уходит вверх", []Screen{{Kind: ScreenNotes, Ref: "../../секрет"}}, "notes.escapes"},
		{"обход через середину пути", []Screen{{Kind: ScreenNotes, Ref: "docs/../../секрет"}}, "notes.escapes"},
		{"дифф сравнивает не с ревизией", []Screen{{Kind: ScreenDiff, Ref: "--exec=rm"}}, "diff.notRevision"},
		{"два одинаковых экрана", []Screen{
			{Kind: ScreenBrowser, Ref: "{Превью}"},
			{Kind: ScreenBrowser, Ref: "{Превью}"},
		}, "screen.twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ValidateFlow(flowWith(c.screens...))
			if err == nil {
				t.Fatalf("такой экран должен быть отвергнут")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Fatalf("отказ должен говорить, что не так: %v", err)
			}
		})
	}
}

// Two kinds have a useful default and may point at nothing: a terminal without
// a command is a shell in the card's working folder, and a diff without
// revisions is what the working copy has and the last commit does not. The rest
// have nothing to open without a ref.
func TestScreensWithAUsefulDefaultMayPointAtNothing(t *testing.T) {
	f := Flow{
		Name: "Один шаг", EntryStage: "work",
		Stages: []Stage{{ID: "work", Name: "Работа", Action: ActionAgent,
			Screens: []Screen{{Kind: ScreenTerminal}, {Kind: ScreenDiff}}}},
	}
	saved, err := ValidateFlow(f)
	if err != nil {
		t.Fatalf("терминал без команды и дифф без ревизий — рабочие экраны: %v", err)
	}
	if len(saved.Stages[0].Screens) != 2 {
		t.Fatalf("экраны не должны были потеряться: %+v", saved.Stages[0].Screens)
	}
}

// A registry entry is edited and handed back whole, so every field on it is a
// field the editor sends — and a blank form sends an empty string for anything
// it does not fill in. A time cannot be parsed from one, so there is none here:
// this is the shape the project form actually posts.
func TestAProjectIsWhatTheFormSends(t *testing.T) {
	var p Project
	posted := `{"id":"","name":"Сайт","kind":"folder","path":"/tmp/сайт"}`
	if err := json.Unmarshal([]byte(posted), &p); err != nil {
		t.Fatalf("то, что отправляет форма, должно разбираться: %v", err)
	}
	checked, err := ValidateProject(p)
	if err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if checked.Name != "Сайт" || checked.Path != "/tmp/сайт" || checked.Kind != ProjectFolder {
		t.Fatalf("поля разъехались: %+v", checked)
	}

	// And a form that carried a stray empty timestamp is not refused over it:
	// there is no such field to parse.
	if err := json.Unmarshal([]byte(`{"name":"Сайт","path":"/tmp","createdAt":""}`), &p); err != nil {
		t.Fatalf("лишнее поле не должно ломать разбор: %v", err)
	}
}
