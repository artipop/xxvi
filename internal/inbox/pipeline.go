// Package inbox turns outside events into cards: what a source brought becomes
// an item, rules decide what to do with it, and the result is a card in the
// inbox — or a comment on the card that item already has, or nothing.
//
// It is deliberately separate from the flow engine. Sources have to work with
// agents switched off: a card from a phone is useful to somebody who has no
// agent and never will. And a source may not start a flow — taking a card into
// work is a person's decision (docs/system.md §3).
package inbox

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/store"
)

// Emitter pushes events to the UI.
type Emitter interface {
	Emit(event string, payload any)
}

// EventInbox says the inbox changed.
const EventInbox = "inbox"

// Pipeline applies a source's rules and writes the result.
type Pipeline struct {
	store *store.Store
	ui    Emitter
	log   *slog.Logger

	// mu serializes ingestion. Deciding about an item is a read of what the
	// source already brought followed by a write, and two deliveries of the
	// same item must not both conclude it is new.
	mu sync.Mutex
}

func NewPipeline(st *store.Store, ui Emitter, log *slog.Logger) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{store: st, ui: ui, log: log}
}

// Outcome is what happened to one item.
type Outcome string

const (
	OutcomeCreated   Outcome = "created"   // a new card
	OutcomeCommented Outcome = "commented" // the item changed and said so on its card
	OutcomeUnchanged Outcome = "unchanged" // already seen, nothing new
	OutcomeDropped   Outcome = "dropped"   // a rule said to ignore it
)

// Result is one item's fate, with the card it concerns when there is one.
type Result struct {
	Item    string  `json:"item"`
	Outcome Outcome `json:"outcome"`
	CardID  string  `json:"cardId,omitempty"`
	Rule    string  `json:"rule,omitempty"`
}

// Ingest runs one batch of items through a source's rules. Items are handled
// one by one and one failing does not stop the rest: a source that sends a
// hundred things and gets nothing filed because the third was malformed is a
// source nobody can debug.
func (p *Pipeline) Ingest(sourceName string, items []model.Item) ([]Result, error) {
	src, err := p.store.Source(sourceName)
	if err != nil {
		return nil, err
	}
	if !src.Enabled {
		return nil, fmt.Errorf("источник «%s» выключен", src.Name)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]Result, 0, len(items))
	changed := false
	for _, item := range items {
		res, err := p.one(src, item.WithFallbackID())
		if err != nil {
			p.log.Warn("элемент источника не обработан", "source", src.Name, "item", item.ExternalID, "err", err)
			continue
		}
		if res.Outcome == OutcomeCreated || res.Outcome == OutcomeCommented {
			changed = true
		}
		out = append(out, res)
	}
	if changed {
		p.emit()
	}
	return out, nil
}

// one decides about a single item and writes the result.
func (p *Pipeline) one(src model.Source, item model.Item) (Result, error) {
	decision := src.Decide(item)
	res := Result{Item: item.ExternalID, Rule: decision.Rule}

	// What this source has already brought. The card carries the answer too —
	// it holds the item's id — so a database restored without source_item still
	// does not duplicate anything.
	existing, seen, err := p.existingCard(src.Name, item.ExternalID)
	if err != nil {
		return Result{}, err
	}

	switch decision.Action {
	case model.ActionDrop:
		// Remembered even so: a dropped item that arrives again should be
		// dropped again without being reasoned about a second time.
		if err := p.store.SeenItem(src.Name, item.ExternalID, item.Version, ""); err != nil {
			return Result{}, err
		}
		res.Outcome = OutcomeDropped
		return res, nil

	case model.ActionComment:
		// A rule asking for a comment on an item that has no card has nothing
		// to comment on. Filing it in the inbox would be a different decision
		// than the one the rule made, so it does nothing and says so.
		if !seen {
			res.Outcome = OutcomeDropped
			return res, nil
		}
		return p.commentOn(src, existing, item, res)
	}

	if seen {
		return p.commentOn(src, existing, item, res)
	}

	card, err := p.store.CreateCard(model.Card{
		Source: src.Name, ExternalID: item.ExternalID, ItemVersion: item.Version,
		Title: itemTitle(item), Body: item.Body, URL: item.URL,
		State: model.StateInbox, Assignee: decision.Assignee,
		Props:     mergeProps(item.Props, decision.Props, decision.SuggestFlow),
		CreatedAt: itemTime(item),
	})
	if err != nil {
		return Result{}, err
	}
	if err := p.store.SeenItem(src.Name, item.ExternalID, item.Version, card.ID); err != nil {
		return Result{}, err
	}
	res.Outcome, res.CardID = OutcomeCreated, card.ID
	return res, nil
}

// commentOn handles an item that already has a card: unchanged items are
// silent, and a changed one does what the source's update mode says.
func (p *Pipeline) commentOn(src model.Source, card model.Card, item model.Item, res Result) (Result, error) {
	res.CardID = card.ID
	if card.ItemVersion == item.Version {
		res.Outcome = OutcomeUnchanged
		return res, nil
	}
	if src.UpdateMode() == model.UpdateIgnore {
		if err := p.store.SetItemVersion(card.ID, item.Version); err != nil {
			return Result{}, err
		}
		res.Outcome = OutcomeUnchanged
		return res, nil
	}
	text := fmt.Sprintf("Источник «%s»: элемент обновился.\n\n%s", src.Name, strings.TrimSpace(item.Body))
	if _, err := p.store.AddComment(card.ID, src.Name, strings.TrimSpace(text)); err != nil {
		return Result{}, err
	}
	if err := p.store.SetItemVersion(card.ID, item.Version); err != nil {
		return Result{}, err
	}
	if err := p.store.SeenItem(src.Name, item.ExternalID, item.Version, card.ID); err != nil {
		return Result{}, err
	}
	res.Outcome = OutcomeCommented
	return res, nil
}

// existingCard answers "have we brought this one already". The card is asked
// first, because it is the durable record: source_item is an index of it.
func (p *Pipeline) existingCard(source, externalID string) (model.Card, bool, error) {
	card, ok, err := p.store.CardBySourceItem(source, externalID)
	if err != nil || ok {
		return card, ok, err
	}
	cardID, _, seen, err := p.store.ItemSeen(source, externalID)
	if err != nil || !seen || cardID == "" {
		return model.Card{}, false, err
	}
	card, err = p.store.Card(cardID)
	if err != nil {
		return model.Card{}, false, nil // the card was deleted; the item is new again
	}
	return card, true, nil
}

// AddManual files a card somebody typed. It is not a source item — there is
// nothing outside to be the same as — so it carries no external id and lands in
// the inbox directly.
func (p *Pipeline) AddManual(source, title, body string, props map[string]string) (model.Card, error) {
	if strings.TrimSpace(title) == "" {
		return model.Card{}, fmt.Errorf("у карточки нет заголовка")
	}
	card, err := p.store.CreateCard(model.Card{
		Source: strings.TrimSpace(source), Title: strings.TrimSpace(title),
		Body: body, State: model.StateInbox, Props: props,
	})
	if err != nil {
		return model.Card{}, err
	}
	p.emit()
	return card, nil
}

func (p *Pipeline) emit() {
	if p.ui != nil {
		p.ui.Emit(EventInbox, map[string]any{})
	}
}

// mergeProps folds the item's own properties, the rule's rendered ones and the
// flow it suggests into what the card carries. The rule wins over the item: it
// is the later and more specific statement about this item.
func mergeProps(item, rule map[string]string, suggestFlow string) map[string]string {
	out := make(map[string]string, len(item)+len(rule)+1)
	for k, v := range item {
		out[k] = v
	}
	for k, v := range rule {
		out[k] = v
	}
	if flow := strings.TrimSpace(suggestFlow); flow != "" {
		out[PropSuggestedFlow] = flow
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PropSuggestedFlow is where a rule's suggestion lands on the card. It is an
// ordinary property so that nothing special has to know about it: the «В
// работу» dialog reads it to preselect a flow, and a person can change or
// remove it like any other.
const PropSuggestedFlow = "Флоу"

func itemTitle(item model.Item) string {
	if title := strings.TrimSpace(item.Title); title != "" {
		return title
	}
	// An item with no title still has to become something readable, and its
	// first line is the best guess anybody has.
	if line, _, _ := strings.Cut(strings.TrimSpace(item.Body), "\n"); line != "" {
		return line
	}
	return "Без заголовка"
}

func itemTime(item model.Item) time.Time {
	if item.At.IsZero() {
		return time.Now().UTC()
	}
	return item.At.UTC()
}
