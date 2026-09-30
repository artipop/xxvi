package store

import (
	"reflect"
	"testing"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// The migration writes the builtin templates as SQL and the model states them
// as Go; the palette of a new database has to be exactly the second.
func TestMigrationSeedsTheBuiltinTemplates(t *testing.T) {
	s := open(t)
	got, err := s.StageTemplates()
	if err != nil {
		t.Fatal(err)
	}
	want := model.BuiltinTemplates()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("шаблоны в базе разошлись с моделью:\n база:   %+v\n модель: %+v", got, want)
	}
	for _, tpl := range got {
		if _, err := model.ValidateTemplate(tpl); err != nil {
			t.Errorf("встроенный шаблон %q не проходит свою же проверку: %v", tpl.ID, err)
		}
	}
}

// A stage remembers the template it was made from, and one that never named
// one is given the builtin it is — which is how flows saved before templates
// are drawn.
func TestStageTemplateSurvivesSave(t *testing.T) {
	s := open(t)
	claude(t, s)
	f := devFlow()
	f.Stages[1].Screens = []model.Screen{{Kind: model.ScreenDiff}}
	f.Stages[0].Template = "my-agent"
	saved, err := s.SaveFlow(f)
	if err != nil {
		t.Fatal(err)
	}
	back, err := s.Flow(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{back.Stages[0].Template, back.Stages[1].Template, back.Stages[2].Template}
	want := []string{"my-agent", model.TemplateDiff, model.TemplateFinal}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("шаблоны стадий: %v, ждали %v", got, want)
	}
}

// A template a person adds goes to the end of the palette, cannot pass itself
// off as builtin, and an edited builtin stays builtin.
func TestSaveAndDeleteStageTemplate(t *testing.T) {
	s := open(t)
	mine, err := s.SaveStageTemplate(model.StageTemplate{
		Name: "Тесты", Icon: "flask", Color: "#ff8800", Builtin: true,
		Screens: []model.Screen{{Kind: model.ScreenTerminal, Ref: "go test ./..."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mine.ID == "" || mine.Builtin {
		t.Fatalf("новый шаблон должен получить id и не быть встроенным: %+v", mine)
	}

	docker := model.BuiltinTemplates()[2]
	docker.Name = "Контейнеры"
	docker.Builtin = false
	if _, err := s.SaveStageTemplate(docker); err != nil {
		t.Fatal(err)
	}

	all, _ := s.StageTemplates()
	if last := all[len(all)-1]; last.ID != mine.ID {
		t.Fatalf("новый шаблон должен встать в конец, а там %q", last.ID)
	}
	if all[2].Name != "Контейнеры" || !all[2].Builtin {
		t.Fatalf("правка встроенного шаблона: %+v", all[2])
	}

	if _, err := s.SaveStageTemplate(model.StageTemplate{Icon: "x"}); !msg.Is(err, "template.noName") {
		t.Fatalf("свой шаблон без имени должен быть отвергнут: %v", err)
	}
	if err := s.DeleteStageTemplate(mine.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStageTemplate(mine.ID); !msg.Is(err, "template.notFound") {
		t.Fatalf("второе удаление: %v", err)
	}
}
