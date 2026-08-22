package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/artipop/xxvi/internal/model"
)

// Cards, their properties, their history, and where they stand.

type cardRow struct {
	ID          string `db:"id"`
	Source      string `db:"source"`
	ExternalID  string `db:"external_id"`
	ItemVersion string `db:"item_version"`
	Title       string `db:"title"`
	Body        string `db:"body"`
	URL         string `db:"url"`
	State       string `db:"state"`
	Assignee    string `db:"assignee"`
	CreatedAt   int64  `db:"created_at"`
	UpdatedAt   int64  `db:"updated_at"`
}

func (r cardRow) card() model.Card {
	return model.Card{
		ID: r.ID, Source: r.Source, ExternalID: r.ExternalID, ItemVersion: r.ItemVersion,
		Title: r.Title, Body: r.Body, URL: r.URL,
		State: model.CardState(r.State), Assignee: r.Assignee,
		CreatedAt: fromMillis(r.CreatedAt), UpdatedAt: fromMillis(r.UpdatedAt),
	}
}

// CreateCard stores a new card and its properties. An id is generated when the
// caller has none.
func (s *Store) CreateCard(c model.Card) (model.Card, error) {
	if strings.TrimSpace(c.Title) == "" {
		return model.Card{}, fmt.Errorf("у карточки нет заголовка")
	}
	if strings.TrimSpace(c.ID) == "" {
		c.ID = uuid.NewString()
	}
	if !c.State.Valid() {
		c.State = model.StateInbox
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now

	err := s.tx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO card (id, source, external_id, item_version, title, body, url, state, assignee, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.Source, c.ExternalID, c.ItemVersion, c.Title, c.Body, c.URL,
			string(c.State), c.Assignee, millis(c.CreatedAt), millis(c.UpdatedAt)); err != nil {
			return err
		}
		return writeProps(tx, c.ID, c.Props)
	})
	if err != nil {
		return model.Card{}, fmt.Errorf("создать карточку: %w", err)
	}
	return c, nil
}

// Card reads one card with its properties.
func (s *Store) Card(id string) (model.Card, error) {
	var r cardRow
	err := s.db.Get(&r, `SELECT * FROM card WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Card{}, fmt.Errorf("карточка %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.Card{}, err
	}
	c := r.card()
	props, err := s.props(id)
	if err != nil {
		return model.Card{}, err
	}
	c.Props = props
	return c, nil
}

// CardsInState lists cards in one state, newest first, with their properties.
func (s *Store) CardsInState(state model.CardState) ([]model.Card, error) {
	var rows []cardRow
	if err := s.db.Select(&rows, `SELECT * FROM card WHERE state = ? ORDER BY created_at DESC`, string(state)); err != nil {
		return nil, fmt.Errorf("прочитать карточки: %w", err)
	}
	return s.withProps(rows)
}

// CardBySourceItem finds the card a source's item became. Not found is not an
// error: most items have never been seen.
func (s *Store) CardBySourceItem(source, externalID string) (model.Card, bool, error) {
	var r cardRow
	err := s.db.Get(&r, `SELECT * FROM card WHERE source = ? AND external_id = ?`, source, externalID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Card{}, false, nil
	}
	if err != nil {
		return model.Card{}, false, err
	}
	c := r.card()
	props, err := s.props(c.ID)
	if err != nil {
		return model.Card{}, false, err
	}
	c.Props = props
	return c, true, nil
}

// CardEdit is a change to a card, empty-means-unchanged, so a caller that only
// knows the assignee does not have to send the title back with it.
//
// The card's own text is not here on purpose for the description a person
// wrote: an agent that has something to say about a card says it in a comment,
// where the rest of its history already is.
type CardEdit struct {
	Title    *string
	Body     *string
	Assignee *string
	State    *model.CardState
	// Props are set or overwritten one by one; a property is removed by
	// setting it to the empty string.
	Props map[string]string
}

// UpdateCard applies an edit and returns the card as it now is.
func (s *Store) UpdateCard(id string, edit CardEdit) (model.Card, error) {
	err := s.tx(func(tx *sqlx.Tx) error {
		set := []string{"updated_at = ?"}
		args := []any{millis(time.Now())}
		if edit.Title != nil {
			if strings.TrimSpace(*edit.Title) == "" {
				return fmt.Errorf("у карточки не может быть пустого заголовка")
			}
			set = append(set, "title = ?")
			args = append(args, strings.TrimSpace(*edit.Title))
		}
		if edit.Body != nil {
			set = append(set, "body = ?")
			args = append(args, *edit.Body)
		}
		if edit.Assignee != nil {
			set = append(set, "assignee = ?")
			args = append(args, strings.TrimSpace(*edit.Assignee))
		}
		if edit.State != nil {
			if !edit.State.Valid() {
				return fmt.Errorf("неизвестное состояние карточки %q", *edit.State)
			}
			set = append(set, "state = ?")
			args = append(args, string(*edit.State))
		}
		args = append(args, id)
		res, err := tx.Exec(`UPDATE card SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("карточка %q: %w", id, ErrNotFound)
		}
		return writeProps(tx, id, edit.Props)
	})
	if err != nil {
		return model.Card{}, err
	}
	return s.Card(id)
}

// SetItemVersion records that the card now reflects this state of its item.
func (s *Store) SetItemVersion(cardID, version string) error {
	_, err := s.db.Exec(`UPDATE card SET item_version = ?, updated_at = ? WHERE id = ?`,
		version, millis(time.Now()), cardID)
	return err
}

// ---- comments ----

// AddComment appends a line to a card's history. Author is an agent's name, a
// source's name, or empty for the application itself.
func (s *Store) AddComment(cardID, author, text string) (model.Comment, error) {
	if strings.TrimSpace(text) == "" {
		return model.Comment{}, nil
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO card_comment (card_id, author, text, created_at) VALUES (?, ?, ?, ?)`,
		cardID, author, text, millis(now))
	if err != nil {
		return model.Comment{}, fmt.Errorf("записать комментарий: %w", err)
	}
	id, _ := res.LastInsertId()
	return model.Comment{ID: id, CardID: cardID, Author: author, Text: text, CreatedAt: now}, nil
}

// Comments is a card's history, oldest first.
func (s *Store) Comments(cardID string) ([]model.Comment, error) {
	var rows []struct {
		ID        int64  `db:"id"`
		CardID    string `db:"card_id"`
		Author    string `db:"author"`
		Text      string `db:"text"`
		CreatedAt int64  `db:"created_at"`
	}
	if err := s.db.Select(&rows, `SELECT * FROM card_comment WHERE card_id = ? ORDER BY id`, cardID); err != nil {
		return nil, err
	}
	out := make([]model.Comment, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.Comment{
			ID: r.ID, CardID: r.CardID, Author: r.Author, Text: r.Text, CreatedAt: fromMillis(r.CreatedAt),
		})
	}
	return out, nil
}

// ---- where a card stands ----

// FlowState reads a card's position. Not being on a flow is not an error: most
// cards are not.
func (s *Store) FlowState(cardID string) (model.FlowState, bool, error) {
	var r struct {
		CardID    string `db:"card_id"`
		FlowID    string `db:"flow_id"`
		StageID   string `db:"stage_id"`
		EnteredAt int64  `db:"entered_at"`
	}
	err := s.db.Get(&r, `SELECT * FROM card_flow WHERE card_id = ?`, cardID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.FlowState{}, false, nil
	}
	if err != nil {
		return model.FlowState{}, false, err
	}
	st := model.FlowState{CardID: r.CardID, FlowID: r.FlowID, StageID: r.StageID, EnteredAt: fromMillis(r.EnteredAt)}
	if err := s.db.Select(&st.Visited, `SELECT stage_id FROM card_flow_visit WHERE card_id = ?`, cardID); err != nil {
		return model.FlowState{}, false, err
	}
	return st, true, nil
}

// FlowStates is every parked card — the index the engine reads whole.
func (s *Store) FlowStates() ([]model.FlowState, error) {
	var rows []struct {
		CardID    string `db:"card_id"`
		FlowID    string `db:"flow_id"`
		StageID   string `db:"stage_id"`
		EnteredAt int64  `db:"entered_at"`
	}
	if err := s.db.Select(&rows, `SELECT * FROM card_flow ORDER BY entered_at`); err != nil {
		return nil, err
	}
	out := make([]model.FlowState, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.FlowState{
			CardID: r.CardID, FlowID: r.FlowID, StageID: r.StageID, EnteredAt: fromMillis(r.EnteredAt),
		})
	}
	return out, nil
}

// EnterStage records that a card now stands on a stage, remembers the stage it
// left as visited, and sets the card's state to "on a flow". One write, because
// a card that is on a flow but nowhere is the invariant this protects.
func (s *Store) EnterStage(cardID, flowID, stageID string) error {
	return s.tx(func(tx *sqlx.Tx) error {
		var previous string
		err := tx.Get(&previous, `SELECT stage_id FROM card_flow WHERE card_id = ?`, cardID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// A flow may loop, so where the card has been is a set, not a trail.
		if previous != "" && previous != stageID {
			if _, err := tx.Exec(`
				INSERT INTO card_flow_visit (card_id, stage_id) VALUES (?, ?)
				ON CONFLICT DO NOTHING`, cardID, previous); err != nil {
				return err
			}
		}
		now := millis(time.Now())
		if _, err := tx.Exec(`
			INSERT INTO card_flow (card_id, flow_id, stage_id, entered_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(card_id) DO UPDATE SET
				flow_id = excluded.flow_id, stage_id = excluded.stage_id, entered_at = excluded.entered_at`,
			cardID, flowID, stageID, now); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE card SET state = ?, updated_at = ? WHERE id = ?`,
			string(model.StateFlow), now, cardID)
		return err
	})
}

// LeaveFlow takes a card off its flow and puts it in the given state — done
// when it reached a final stage, inbox when somebody pulled it back.
func (s *Store) LeaveFlow(cardID string, state model.CardState) error {
	if !state.Valid() {
		return fmt.Errorf("неизвестное состояние карточки %q", state)
	}
	return s.tx(func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`DELETE FROM card_flow WHERE card_id = ?`, cardID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM stage_queue WHERE card_id = ?`, cardID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE card SET state = ?, updated_at = ? WHERE id = ?`,
			string(state), millis(time.Now()), cardID)
		return err
	})
}

// AppendFlowEvent records one transition — the journal that answers "why is
// this card here".
func (s *Store) AppendFlowEvent(e model.FlowEvent) error {
	_, err := s.db.Exec(`
		INSERT INTO flow_event (card_id, flow_id, from_stage, to_stage, on_trigger, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.CardID, e.FlowID, e.FromStage, e.ToStage, e.On, e.Detail, millis(time.Now()))
	return err
}

// FlowEvents is a card's transitions, oldest first.
func (s *Store) FlowEvents(cardID string) ([]model.FlowEvent, error) {
	var rows []struct {
		ID        int64  `db:"id"`
		CardID    string `db:"card_id"`
		FlowID    string `db:"flow_id"`
		FromStage string `db:"from_stage"`
		ToStage   string `db:"to_stage"`
		OnTrigger string `db:"on_trigger"`
		Detail    string `db:"detail"`
		CreatedAt int64  `db:"created_at"`
	}
	if err := s.db.Select(&rows, `SELECT * FROM flow_event WHERE card_id = ? ORDER BY id`, cardID); err != nil {
		return nil, err
	}
	out := make([]model.FlowEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.FlowEvent{
			ID: r.ID, CardID: r.CardID, FlowID: r.FlowID, FromStage: r.FromStage,
			ToStage: r.ToStage, On: r.OnTrigger, Detail: r.Detail, CreatedAt: fromMillis(r.CreatedAt),
		})
	}
	return out, nil
}

// LastFlowEvent is how the card got where it stands: the most recent transition
// it made. What the engine asks when it is about to tell an agent why the card
// is in front of it — a card that came back on a failure arrived with a reason,
// and the reason is the one thing the next session needs and would otherwise
// have to be told by hand.
func (s *Store) LastFlowEvent(cardID string) (model.FlowEvent, bool, error) {
	var r struct {
		ID        int64  `db:"id"`
		CardID    string `db:"card_id"`
		FlowID    string `db:"flow_id"`
		FromStage string `db:"from_stage"`
		ToStage   string `db:"to_stage"`
		OnTrigger string `db:"on_trigger"`
		Detail    string `db:"detail"`
		CreatedAt int64  `db:"created_at"`
	}
	err := s.db.Get(&r, `SELECT * FROM flow_event WHERE card_id = ? ORDER BY id DESC LIMIT 1`, cardID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.FlowEvent{}, false, nil
	}
	if err != nil {
		return model.FlowEvent{}, false, err
	}
	return model.FlowEvent{
		ID: r.ID, CardID: r.CardID, FlowID: r.FlowID, FromStage: r.FromStage,
		ToStage: r.ToStage, On: r.OnTrigger, Detail: r.Detail, CreatedAt: fromMillis(r.CreatedAt),
	}, true, nil
}

// CardsOnStage counts the cards standing on each stage of a flow — what the
// flow overview draws, as a query rather than a second bookkeeping.
func (s *Store) CardsOnStage(flowID string) (map[string]int, error) {
	var rows []struct {
		StageID string `db:"stage_id"`
		N       int    `db:"n"`
	}
	if err := s.db.Select(&rows, `
		SELECT stage_id, COUNT(*) AS n FROM card_flow WHERE flow_id = ? GROUP BY stage_id`, flowID); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.StageID] = r.N
	}
	return out, nil
}

// CardsOnFlow lists the cards currently travelling a flow, with their stage.
func (s *Store) CardsOnFlow(flowID string) ([]model.Card, map[string]string, error) {
	var rows []cardRow
	if err := s.db.Select(&rows, `
		SELECT c.* FROM card c
		JOIN card_flow cf ON cf.card_id = c.id
		WHERE cf.flow_id = ?
		ORDER BY cf.entered_at`, flowID); err != nil {
		return nil, nil, err
	}
	cards, err := s.withProps(rows)
	if err != nil {
		return nil, nil, err
	}
	var places []struct {
		CardID  string `db:"card_id"`
		StageID string `db:"stage_id"`
	}
	if err := s.db.Select(&places, `SELECT card_id, stage_id FROM card_flow WHERE flow_id = ?`, flowID); err != nil {
		return nil, nil, err
	}
	stageOf := make(map[string]string, len(places))
	for _, p := range places {
		stageOf[p.CardID] = p.StageID
	}
	return cards, stageOf, nil
}

// ---- properties ----

func (s *Store) props(cardID string) (map[string]string, error) {
	var rows []struct {
		Name  string `db:"name"`
		Value string `db:"value"`
	}
	if err := s.db.Select(&rows, `SELECT name, value FROM card_prop WHERE card_id = ?`, cardID); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Name] = r.Value
	}
	return out, nil
}

// withProps fills in the properties of a page of cards in one query, rather
// than one query per card.
func (s *Store) withProps(rows []cardRow) ([]model.Card, error) {
	cards := make([]model.Card, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		cards = append(cards, r.card())
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return cards, nil
	}
	query, args, err := sqlx.In(`SELECT card_id, name, value FROM card_prop WHERE card_id IN (?)`, ids)
	if err != nil {
		return nil, err
	}
	var props []struct {
		CardID string `db:"card_id"`
		Name   string `db:"name"`
		Value  string `db:"value"`
	}
	if err := s.db.Select(&props, s.db.Rebind(query), args...); err != nil {
		return nil, err
	}
	byCard := map[string]map[string]string{}
	for _, p := range props {
		if byCard[p.CardID] == nil {
			byCard[p.CardID] = map[string]string{}
		}
		byCard[p.CardID][p.Name] = p.Value
	}
	for i := range cards {
		cards[i].Props = byCard[cards[i].ID]
	}
	return cards, nil
}

// writeProps sets or removes properties. Setting one to the empty string
// removes it: a property with no value is a property nobody set.
func writeProps(tx *sqlx.Tx, cardID string, props map[string]string) error {
	for name, value := range props {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.TrimSpace(value) == "" {
			if _, err := tx.Exec(`DELETE FROM card_prop WHERE card_id = ? AND name = ?`, cardID, name); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO card_prop (card_id, name, value) VALUES (?, ?, ?)
			ON CONFLICT(card_id, name) DO UPDATE SET value = excluded.value`,
			cardID, name, value); err != nil {
			return err
		}
	}
	return nil
}
