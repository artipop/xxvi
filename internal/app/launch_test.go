package app

import (
	"testing"

	"github.com/artipop/xxvi/internal/launch"
	"github.com/artipop/xxvi/internal/model"
)

func tryStage(t *testing.T, a *App) (model.Flow, model.Stage) {
	t.Helper()
	flows, err := a.Store.Flows()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range flows {
		for _, st := range f.Stages {
			if st.ID == "rmr-try" {
				return f, st
			}
		}
	}
	t.Fatal("no «Run and check» stage")
	return model.Flow{}, model.Stage{}
}

func TestUpgradeRunScreens(t *testing.T) {
	a := open(t)

	// An installation seeded before the run screen: a bare shell.
	f, _ := tryStage(t, a)
	for i := range f.Stages {
		if f.Stages[i].ID == "rmr-try" {
			f.Stages[i].Screens = []model.Screen{{Kind: model.ScreenTerminal}}
		}
	}
	if _, err := a.Store.SaveFlow(f); err != nil {
		t.Fatal(err)
	}
	a.upgradeRunScreens()
	if _, st := tryStage(t, a); len(st.Screens) != 1 || st.Screens[0].Kind != model.ScreenRun {
		t.Fatalf("an untouched stage gets the run screen, got %+v", st.Screens)
	}

	// One somebody edited is left as it is.
	f, _ = tryStage(t, a)
	for i := range f.Stages {
		if f.Stages[i].ID == "rmr-try" {
			f.Stages[i].Screens = []model.Screen{{Kind: model.ScreenTerminal, Ref: "make run"}}
		}
	}
	if _, err := a.Store.SaveFlow(f); err != nil {
		t.Fatal(err)
	}
	a.upgradeRunScreens()
	if _, st := tryStage(t, a); st.Screens[0].Ref != "make run" {
		t.Fatalf("an edited stage must stay, got %+v", st.Screens)
	}
}

func TestLaunchChoiceIsRememberedPerProject(t *testing.T) {
	a := open(t)
	one := model.Card{ID: "c1", Project: "p1"}
	other := model.Card{ID: "c2", Project: "p1"}
	if launchKey(one) != launchKey(other) {
		t.Fatal("two cards of one project share the choice")
	}
	if launchKey(model.Card{ID: "c3"}) == launchKey(one) {
		t.Fatal("a card without a project has its own")
	}

	card, err := a.Store.CreateCard(model.Card{Title: "x"})
	if err != nil {
		t.Skipf("no way to make a card here: %v", err)
	}
	if _, ok := a.rememberedLaunch(card.ID); ok {
		t.Fatal("nothing is remembered before a launch")
	}
	want := launch.Profile{Kind: model.LaunchBackend, Tool: "compose", Command: "docker compose up"}
	a.rememberLaunch(card.ID, want)
	if got, ok := a.rememberedLaunch(card.ID); !ok || got != want {
		t.Fatalf("remembered %+v %v, want %+v", got, ok, want)
	}
}
