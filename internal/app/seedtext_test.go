package app

import (
	"testing"

	"github.com/artipop/xxvi/internal/model"
)

func flowByEntry(t *testing.T, a *App, entry string) model.Flow {
	t.Helper()
	flows, err := a.Store.Flows()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range flows {
		if f.EntryStage == entry {
			return f
		}
	}
	t.Fatalf("no flow entered at %q", entry)
	return model.Flow{}
}

func stageOf(f model.Flow, id string) model.Stage {
	for _, s := range f.Stages {
		if s.ID == id {
			return s
		}
	}
	return model.Stage{}
}

// The examples are seeded in English and put into the reader's language, and
// back again when the language changes: every word is one the application
// wrote, so every word is the application's to rewrite.
func TestLocalizeSeedsBothWays(t *testing.T) {
	a := open(t)

	a.localizeSeeds("ru-RU")
	f := flowByEntry(t, a, "dev-work")
	if f.Name != "Разработка" || stageOf(f, "dev-work").Name != "В работе" {
		t.Fatalf("флоу должен стать русским: %q, %q", f.Name, stageOf(f, "dev-work").Name)
	}
	if got := stageOf(f, "dev-review").Screens[0].Title; got != "Изменения" {
		t.Fatalf("заголовок экрана тоже переводится: %q", got)
	}
	if got := stageOf(f, "dev-check").Prompt; got == "" || got[0:2] == "Ch" {
		t.Fatalf("промпт тоже переводится: %q", got)
	}

	a.localizeSeeds("en")
	f = flowByEntry(t, a, "dev-work")
	if f.Name != "Development" || stageOf(f, "dev-work").Name != "In progress" {
		t.Fatalf("и обратно на английский: %q, %q", f.Name, stageOf(f, "dev-work").Name)
	}

	// A language with no words of its own gets English.
	a.localizeSeeds("de")
	if f = flowByEntry(t, a, "dev-work"); f.Name != "Development" {
		t.Fatalf("незнакомый язык — английский: %q", f.Name)
	}
}

// What somebody changed is theirs: a renamed stage keeps its name, and the
// rest of the flow is still translated around it.
func TestLocalizeSeedsLeavesEdits(t *testing.T) {
	a := open(t)
	f := flowByEntry(t, a, "dev-work")
	for i := range f.Stages {
		if f.Stages[i].ID == "dev-check" {
			f.Stages[i].Name = "Автотесты"
		}
	}
	if _, err := a.Store.SaveFlow(f); err != nil {
		t.Fatal(err)
	}

	a.localizeSeeds("ru")
	f = flowByEntry(t, a, "dev-work")
	if got := stageOf(f, "dev-check").Name; got != "Автотесты" {
		t.Fatalf("переименованная стадия осталась бы как есть: %q", got)
	}
	if f.Name != "Разработка" {
		t.Fatalf("остальное переводится: %q", f.Name)
	}
}

// Every stage the examples seed has words in every language, so no stage is
// left in English on a Russian screen by an omission.
func TestSeedTextsCoverEveryStage(t *testing.T) {
	texts := seedTexts()
	for lang, byEntry := range texts {
		for _, f := range SeedFlows() {
			tf, ok := byEntry[f.EntryStage]
			if !ok || tf.name == "" || tf.description == "" {
				t.Errorf("%s: нет слов для флоу %q", lang, f.Name)
				continue
			}
			for _, s := range f.Stages {
				st, ok := tf.stages[s.ID]
				if !ok || st.name == "" {
					t.Errorf("%s: нет названия стадии %q", lang, s.ID)
				}
				if (s.Prompt == "") != (st.prompt == "") {
					t.Errorf("%s: промпт стадии %q", lang, s.ID)
				}
				if len(st.titles) != len(s.Screens) {
					t.Errorf("%s: заголовки экранов стадии %q: %d из %d", lang, s.ID, len(st.titles), len(s.Screens))
				}
			}
		}
	}
}
