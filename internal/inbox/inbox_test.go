package inbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/store"
)

func setup(t *testing.T, src model.Source) (*store.Store, *Pipeline) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.SaveSource(src); err != nil {
		t.Fatalf("источник: %v", err)
	}
	return st, NewPipeline(st, nil, nil)
}

func plain() model.Source {
	return model.Source{Name: "Демо", Plugin: PluginDemo, Enabled: true}
}

func item(id, title string) model.Item {
	return model.Item{ExternalID: id, Title: title, Body: "текст", At: time.Now().UTC()}
}

func TestItemBecomesAnInboxCard(t *testing.T) {
	st, p := setup(t, plain())
	res, err := p.Ingest("Демо", []model.Item{item("1", "Починить кран")})
	if err != nil {
		t.Fatalf("принять: %v", err)
	}
	if len(res) != 1 || res[0].Outcome != OutcomeCreated {
		t.Fatalf("ожидалась созданная карточка: %+v", res)
	}
	card, err := st.Card(res[0].CardID)
	if err != nil {
		t.Fatalf("карточка: %v", err)
	}
	if card.State != model.StateInbox || card.Source != "Демо" || card.Title != "Починить кран" {
		t.Fatalf("карточка не сошлась: %+v", card)
	}
}

// The same item arriving again is the case the whole ExternalID/Version pair
// exists for: a source reports what it can see, not what changed.
func TestTheSameItemDoesNotBecomeASecondCard(t *testing.T) {
	st, p := setup(t, plain())
	p.Ingest("Демо", []model.Item{item("1", "Раз")})
	res, _ := p.Ingest("Демо", []model.Item{item("1", "Раз")})

	if res[0].Outcome != OutcomeUnchanged {
		t.Fatalf("повтор не должен ничего менять: %+v", res[0])
	}
	cards, _ := st.CardsInState(model.StateInbox)
	if len(cards) != 1 {
		t.Fatalf("карточка должна быть одна, а их %d", len(cards))
	}
}

func TestChangedItemCommentsOnItsCard(t *testing.T) {
	st, p := setup(t, plain())
	first, _ := p.Ingest("Демо", []model.Item{{ExternalID: "1", Title: "Раз", Version: "v1", Body: "было"}})
	res, _ := p.Ingest("Демо", []model.Item{{ExternalID: "1", Title: "Раз", Version: "v2", Body: "стало"}})

	if res[0].Outcome != OutcomeCommented || res[0].CardID != first[0].CardID {
		t.Fatalf("изменившийся элемент дописывает комментарий своей карточке: %+v", res[0])
	}
	comments, _ := st.Comments(first[0].CardID)
	if len(comments) != 1 || comments[0].Author != "Демо" {
		t.Fatalf("комментарий от источника не записан: %+v", comments)
	}
	// And the card now reflects the new state, so a third delivery is silent.
	again, _ := p.Ingest("Демо", []model.Item{{ExternalID: "1", Version: "v2"}})
	if again[0].Outcome != OutcomeUnchanged {
		t.Fatalf("после обновления повтор снова молчит: %+v", again[0])
	}
}

func TestUpdateModeIgnoreStaysSilent(t *testing.T) {
	src := plain()
	src.Update = model.UpdateIgnore
	st, p := setup(t, src)
	first, _ := p.Ingest("Демо", []model.Item{{ExternalID: "1", Title: "Раз", Version: "v1"}})
	res, _ := p.Ingest("Демо", []model.Item{{ExternalID: "1", Title: "Раз", Version: "v2"}})

	if res[0].Outcome != OutcomeUnchanged {
		t.Fatalf("режим ignore ничего не пишет: %+v", res[0])
	}
	comments, _ := st.Comments(first[0].CardID)
	if len(comments) != 0 {
		t.Fatalf("комментариев быть не должно: %+v", comments)
	}
}

// Rules are tried in order and the first match wins, so the catch-all goes last.
func TestRulesDecideInOrder(t *testing.T) {
	src := plain()
	src.Rules = []model.Rule{
		{Name: "срочные", When: model.Match{Title: "срочно"}, Then: model.ActionCard, SuggestFlow: "Разработка"},
		{Name: "всё остальное", Then: model.ActionDrop},
	}
	st, p := setup(t, src)

	res, _ := p.Ingest("Демо", []model.Item{
		item("1", "СРОЧНО: упал прод"),
		item("2", "рассылка"),
	})
	if res[0].Outcome != OutcomeCreated || res[0].Rule != "срочные" {
		t.Fatalf("первый элемент должен пройти по правилу «срочные»: %+v", res[0])
	}
	if res[1].Outcome != OutcomeDropped || res[1].Rule != "всё остальное" {
		t.Fatalf("второй должен быть отброшен правилом-ловушкой: %+v", res[1])
	}
	card, _ := st.Card(res[0].CardID)
	if card.Prop(PropSuggestedFlow) != "Разработка" {
		t.Fatalf("подсказанный флоу должен лечь на карточку: %v", card.Props)
	}
	cards, _ := st.CardsInState(model.StateInbox)
	if len(cards) != 1 {
		t.Fatalf("отброшенный элемент карточкой не становится, а их %d", len(cards))
	}
}

// A noisy source inverts the default: there a rule is a subscription and
// everything else is noise.
func TestNoisySourceDropsWhatMatchedNothing(t *testing.T) {
	src := plain()
	src.Noisy = true
	src.Rules = []model.Rule{{Name: "доставка", When: model.Match{Title: "доставк"}, Then: model.ActionCard}}
	_, p := setup(t, src)

	res, _ := p.Ingest("Демо", []model.Item{
		item("1", "Доставка приедет завтра"),
		item("2", "Ваша подписка продлена"),
	})
	if res[0].Outcome != OutcomeCreated {
		t.Fatalf("подписка должна заводить карточку: %+v", res[0])
	}
	if res[1].Outcome != OutcomeDropped {
		t.Fatalf("на шумном источнике остальное — шум: %+v", res[1])
	}
}

// An ordinary source does the opposite: silently losing an item is what makes
// an integration impossible to debug.
func TestOrdinarySourceFilesWhatMatchedNothing(t *testing.T) {
	src := plain()
	src.Rules = []model.Rule{{Name: "срочные", When: model.Match{Title: "срочно"}, Then: model.ActionCard}}
	_, p := setup(t, src)

	res, _ := p.Ingest("Демо", []model.Item{item("1", "обычное письмо")})
	if res[0].Outcome != OutcomeCreated {
		t.Fatalf("несовпавший элемент идёт во входящие: %+v", res[0])
	}
}

// A rule asking for a comment on an item that has no card has nothing to
// comment on, and filing it would be a different decision than the rule made.
func TestCommentRuleOnAnUnknownItemDoesNothing(t *testing.T) {
	src := plain()
	src.Rules = []model.Rule{{Name: "шум", Then: model.ActionComment}}
	st, p := setup(t, src)

	res, _ := p.Ingest("Демо", []model.Item{item("1", "что-то")})
	if res[0].Outcome != OutcomeDropped {
		t.Fatalf("комментировать нечего: %+v", res[0])
	}
	cards, _ := st.CardsInState(model.StateInbox)
	if len(cards) != 0 {
		t.Fatalf("карточек быть не должно, а их %d", len(cards))
	}
}

// A rule's properties are templates over the item, and they win over the item's
// own: they are the later and more specific statement about it.
func TestRulePropsAreRenderedOverTheItem(t *testing.T) {
	src := plain()
	src.Rules = []model.Rule{{
		Name:  "со ссылкой",
		Then:  model.ActionCard,
		Props: map[string]string{"Ссылка": "{{.URL}}", "Приоритет": "высокий"},
	}}
	st, p := setup(t, src)

	res, _ := p.Ingest("Демо", []model.Item{{
		ExternalID: "1", Title: "Раз", URL: "https://example.org/1",
		Props: map[string]string{"Приоритет": "низкий"},
	}})
	card, _ := st.Card(res[0].CardID)
	if card.Prop("Ссылка") != "https://example.org/1" {
		t.Fatalf("шаблон не подставился: %v", card.Props)
	}
	if card.Prop("Приоритет") != "высокий" {
		t.Fatalf("правило должно выигрывать у свойства элемента: %v", card.Props)
	}
}

// An item with no id at all still must not become a second card when its source
// repeats it.
func TestItemWithoutAnIDGetsAStableOne(t *testing.T) {
	st, p := setup(t, plain())
	unnamed := model.Item{Title: "Без идентификатора", Body: "текст"}
	p.Ingest("Демо", []model.Item{unnamed})
	p.Ingest("Демо", []model.Item{unnamed})

	cards, _ := st.CardsInState(model.StateInbox)
	if len(cards) != 1 {
		t.Fatalf("повтор не должен заводить вторую карточку, их %d", len(cards))
	}
}

// One malformed item must not cost the rest of the batch: a source that sends a
// hundred things and files nothing is a source nobody can debug.
func TestOneBadItemDoesNotStopTheBatch(t *testing.T) {
	st, p := setup(t, plain())
	res, err := p.Ingest("Демо", []model.Item{
		item("1", "Первая"),
		{ExternalID: "2"}, // no title and no body at all
		item("3", "Третья"),
	})
	if err != nil {
		t.Fatalf("партия не должна падать целиком: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("обработаны должны быть все три: %+v", res)
	}
	cards, _ := st.CardsInState(model.StateInbox)
	if len(cards) != 3 {
		t.Fatalf("ожидалось три карточки, получено %d", len(cards))
	}
	// The one with nothing to name it still has to be readable in a list.
	for _, c := range cards {
		if c.Title == "" {
			t.Fatal("у карточки не может быть пустого заголовка")
		}
	}
}

func TestDisabledSourceBringsNothing(t *testing.T) {
	src := plain()
	src.Enabled = false
	_, p := setup(t, src)
	if _, err := p.Ingest("Демо", []model.Item{item("1", "Раз")}); err == nil {
		t.Fatal("выключенный источник не должен принимать элементы")
	}
}

func TestInboxIsGroupedBySource(t *testing.T) {
	st, p := setup(t, plain())
	if _, err := st.SaveSource(model.Source{Name: "Телефон", Plugin: PluginDemo, Enabled: true}); err != nil {
		t.Fatalf("второй источник: %v", err)
	}
	p.Ingest("Демо", []model.Item{item("1", "Раз"), item("2", "Два")})
	p.Ingest("Телефон", []model.Item{item("3", "Три")})

	cards, _ := st.CardsInState(model.StateInbox)
	groups := model.GroupBySource(cards)
	if len(groups) != 2 {
		t.Fatalf("ожидалось две группы, получено %d", len(groups))
	}
	if groups[0].Source != "Демо" || len(groups[0].Cards) != 2 {
		t.Fatalf("первой идёт самая наполненная группа: %+v", groups[0])
	}
}

func TestManualCardLandsInTheInbox(t *testing.T) {
	st, p := setup(t, plain())
	card, err := p.AddManual("Демо", "Своя задача", "текст", nil)
	if err != nil {
		t.Fatalf("добавить: %v", err)
	}
	if card.State != model.StateInbox || card.ExternalID != "" {
		t.Fatalf("карточка, набранная руками, — не элемент источника: %+v", card)
	}
	// And having no external id, two of them are two cards.
	if _, err := p.AddManual("Демо", "Своя задача", "текст", nil); err != nil {
		t.Fatalf("вторая такая же должна создаваться: %v", err)
	}
	cards, _ := st.CardsInState(model.StateInbox)
	if len(cards) != 2 {
		t.Fatalf("ожидалось две карточки, получено %d", len(cards))
	}
}

// ---- reading the file ----

func TestReadItemsSkipsBlanksAndReportsBadLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DemoFile)
	content := `{"id":"1","title":"Раз"}

// комментарий
{сломано}
{"id":"2","title":"Два"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("файл: %v", err)
	}
	items, err := ReadItems(path)
	if err == nil {
		t.Fatal("про нечитаемую строку надо сказать")
	}
	if len(items) != 2 {
		t.Fatalf("остальные строки должны прочитаться, получено %d", len(items))
	}
	// "Somewhere in this file" is not something a person can act on.
	if !strings.Contains(err.Error(), "4") {
		t.Fatalf("ошибка должна называть номер строки: %v", err)
	}
}

// A source configured before anybody put anything in its file has nothing to
// bring, which is not the same as being broken.
func TestReadItemsTreatsAMissingFileAsEmpty(t *testing.T) {
	items, err := ReadItems(filepath.Join(t.TempDir(), "нет-такого.jsonl"))
	if err != nil || len(items) != 0 {
		t.Fatalf("отсутствующий файл — это пусто, а не ошибка: %v (%d)", err, len(items))
	}
}

// A hand-added item goes through the file so it is an item like any other: it
// meets the same rules and survives the file being read again.
func TestAppendItemRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", DemoFile)
	if err := AppendItem(path, model.Item{Title: "Своё", Body: "текст"}); err != nil {
		t.Fatalf("дописать: %v", err)
	}
	items, err := ReadItems(path)
	if err != nil || len(items) != 1 {
		t.Fatalf("прочитать: %v (%d)", err, len(items))
	}
	if items[0].ExternalID == "" {
		t.Fatal("у дописанного элемента должен появиться устойчивый идентификатор")
	}
	if items[0].At.IsZero() {
		t.Fatal("время должно проставляться")
	}
}
