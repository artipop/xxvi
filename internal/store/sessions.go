package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// The running half: agent sessions and their stream, the queue of cards waiting
// for a place on a stage, the keys that make one event move a card once, and
// the machine's own settings.

// SessionStatus is the lifecycle state of an agent session.
type SessionStatus string

const (
	StatusQueued    SessionStatus = "queued"
	StatusRunning   SessionStatus = "running"
	StatusAsking    SessionStatus = "asking" // stopped on a question, waiting for a person
	StatusDone      SessionStatus = "done"
	StatusFailed    SessionStatus = "failed"
	StatusCancelled SessionStatus = "cancelled"
	// StatusPaused is a terminal run the application closed on. Its CLI saved
	// the conversation, and the stage waits for a person to continue it
	// (Engine.Continue) rather than being over.
	StatusPaused SessionStatus = "paused"
)

// Terminal reports whether the status is final. A paused run is: the process
// is gone, and continuing it is a new run.
func (s SessionStatus) Terminal() bool {
	return s == StatusDone || s == StatusFailed || s == StatusCancelled || s == StatusPaused
}

// Session is one recorded run of an agent against one card on one stage.
type Session struct {
	ID        string `json:"id"`
	CardID    string `json:"cardId"`
	FlowID    string `json:"flowId,omitempty"`
	StageID   string `json:"stageId,omitempty"`
	AgentName string `json:"agentName"`
	AgentKind string `json:"agentKind"`
	// Work is how this run was worked — the stage's mode as it stood when the
	// card entered (docs/system.md §4.1.1). Recorded rather than looked up: the
	// ribbon reads it long after the stage may have been edited.
	Work         string        `json:"work,omitempty"`
	ACPSessionID string        `json:"acpSessionId,omitempty"`
	Status       SessionStatus `json:"status"`
	Cwd          string        `json:"cwd,omitempty"`
	StartedAt    time.Time     `json:"startedAt"`
	FinishedAt   time.Time     `json:"finishedAt,omitempty"`
	// Error is why the run ended the way it did, when it did not simply finish.
	Error *msg.Msg `json:"error,omitempty"`
}

type sessionRow struct {
	ID           string        `db:"id"`
	CardID       string        `db:"card_id"`
	FlowID       string        `db:"flow_id"`
	StageID      string        `db:"stage_id"`
	AgentName    string        `db:"agent_name"`
	AgentKind    string        `db:"agent_kind"`
	Work         string        `db:"work"`
	ACPSessionID string        `db:"acp_session_id"`
	Status       string        `db:"status"`
	Cwd          string        `db:"cwd"`
	StartedAt    int64         `db:"started_at"`
	FinishedAt   sql.NullInt64 `db:"finished_at"`
	ErrorText    string        `db:"error_text"`
}

func (r sessionRow) session() Session {
	return Session{
		ID: r.ID, CardID: r.CardID, FlowID: r.FlowID, StageID: r.StageID,
		AgentName: r.AgentName, AgentKind: r.AgentKind, Work: r.Work, ACPSessionID: r.ACPSessionID,
		Status: SessionStatus(r.Status), Cwd: r.Cwd,
		StartedAt: fromMillis(r.StartedAt), FinishedAt: fromNullMillis(r.FinishedAt),
		Error: parsedMsg(r.ErrorText),
	}
}

// InsertSession records a session as it starts.
func (s *Store) InsertSession(sess Session) error {
	_, err := s.db.Exec(`
		INSERT INTO agent_session (id, card_id, flow_id, stage_id, agent_name, agent_kind, work, status, cwd, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.CardID, sess.FlowID, sess.StageID, sess.AgentName, sess.AgentKind,
		sess.Work, string(sess.Status), sess.Cwd, millis(sess.StartedAt))
	return err
}

// SessionUpdate is a change to a running session, empty-means-unchanged.
type SessionUpdate struct {
	Status       *SessionStatus
	ACPSessionID *string
	Cwd          *string
	Error        *msg.Msg
	FinishedAt   *time.Time
}

// UpdateSession applies a change.
func (s *Store) UpdateSession(id string, u SessionUpdate) error {
	set := []string{}
	args := []any{}
	if u.Status != nil {
		set = append(set, "status = ?")
		args = append(args, string(*u.Status))
	}
	if u.ACPSessionID != nil {
		set = append(set, "acp_session_id = ?")
		args = append(args, *u.ACPSessionID)
	}
	if u.Cwd != nil {
		set = append(set, "cwd = ?")
		args = append(args, *u.Cwd)
	}
	if u.Error != nil {
		set = append(set, "error_text = ?")
		args = append(args, u.Error.Store())
	}
	if u.FinishedAt != nil {
		set = append(set, "finished_at = ?")
		args = append(args, nullMillis(*u.FinishedAt))
	}
	if len(set) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := s.db.Exec(`UPDATE agent_session SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...)
	return err
}

// Session is one run by its id.
func (s *Store) Session(id string) (Session, error) {
	var r sessionRow
	err := s.db.Get(&r, `SELECT * FROM agent_session WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Session{}, err
	}
	return r.session(), nil
}

// SessionsForCard is a card's runs, newest first.
func (s *Store) SessionsForCard(cardID string) ([]Session, error) {
	var rows []sessionRow
	if err := s.db.Select(&rows, `SELECT * FROM agent_session WHERE card_id = ? ORDER BY started_at DESC`, cardID); err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.session())
	}
	return out, nil
}

// AbandonRunningSessions ends every session left running by a previous run of
// the application. A process that is gone is not still working: a row that says
// otherwise would make a card look busy forever.
//
// A terminal run whose conversation is known is paused, as it would have been
// had the application closed properly: the CLI keeps its conversation on disk
// whichever way it went. Anything else is cancelled.
func (s *Store) AbandonRunningSessions() (int, error) {
	res, err := s.db.Exec(`
		UPDATE agent_session
		SET status = CASE WHEN work = ? AND acp_session_id != '' THEN ? ELSE ? END,
		    finished_at = ?,
		    error_text = CASE WHEN work = ? AND acp_session_id != '' THEN ? ELSE ? END
		WHERE status IN (?, ?, ?)`,
		model.WorkTerminal, string(StatusPaused), string(StatusCancelled),
		millis(time.Now()),
		model.WorkTerminal, msg.New("session.paused").Store(), msg.New("session.appClosed").Store(),
		string(StatusQueued), string(StatusRunning), string(StatusAsking))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ---- session stream ----

// AppendSessionEvent persists one thing a session did, in order.
func (s *Store) AppendSessionEvent(sessionID string, seq int64, kind string, payload any) error {
	_, err := s.db.Exec(`
		INSERT INTO session_event (session_id, seq, kind, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		sessionID, seq, kind, encodeJSON(payload), millis(time.Now()))
	return err
}

// SessionEvent is one line of a session's stream.
type SessionEvent struct {
	Seq       int64     `json:"seq"`
	Kind      string    `json:"kind"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"createdAt"`
}

// SessionEvents is a session's stream, in order.
func (s *Store) SessionEvents(sessionID string) ([]SessionEvent, error) {
	var rows []struct {
		Seq       int64  `db:"seq"`
		Kind      string `db:"kind"`
		Payload   string `db:"payload"`
		CreatedAt int64  `db:"created_at"`
	}
	if err := s.db.Select(&rows, `
		SELECT seq, kind, payload, created_at FROM session_event WHERE session_id = ? ORDER BY seq`, sessionID); err != nil {
		return nil, err
	}
	out := make([]SessionEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionEvent{Seq: r.Seq, Kind: r.Kind, Payload: r.Payload, CreatedAt: fromMillis(r.CreatedAt)})
	}
	return out, nil
}

// ---- the stage queue ----

// QueuedCard is one card waiting for a place on a stage.
type QueuedCard struct {
	CardID   string    `json:"cardId"`
	FlowID   string    `json:"flowId"`
	StageID  string    `json:"stageId"`
	QueuedAt time.Time `json:"queuedAt"`
}

// Enqueue parks a card whose stage is full. It reports whether the entry is
// fresh, so the card is told once rather than on every retry.
func (s *Store) Enqueue(q QueuedCard) (fresh bool, err error) {
	var existing string
	err = s.db.Get(&existing, `SELECT stage_id FROM stage_queue WHERE card_id = ?`, q.CardID)
	switch {
	case err == nil && existing == q.StageID:
		return false, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	_, err = s.db.Exec(`
		INSERT INTO stage_queue (card_id, flow_id, stage_id, queued_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(card_id) DO UPDATE SET
			flow_id = excluded.flow_id, stage_id = excluded.stage_id, queued_at = excluded.queued_at`,
		q.CardID, q.FlowID, q.StageID, millis(time.Now()))
	return err == nil, err
}

// Dequeue forgets a card that no longer waits: it started, or somebody moved it
// somewhere else.
func (s *Store) Dequeue(cardID string) error {
	_, err := s.db.Exec(`DELETE FROM stage_queue WHERE card_id = ?`, cardID)
	return err
}

// NextQueued is the card that has waited longest for a stage. FIFO, because a
// queue that reorders itself is a queue nobody can predict.
func (s *Store) NextQueued(stageID string) (QueuedCard, bool, error) {
	var r struct {
		CardID   string `db:"card_id"`
		FlowID   string `db:"flow_id"`
		StageID  string `db:"stage_id"`
		QueuedAt int64  `db:"queued_at"`
	}
	err := s.db.Get(&r, `SELECT * FROM stage_queue WHERE stage_id = ? ORDER BY queued_at LIMIT 1`, stageID)
	if errors.Is(err, sql.ErrNoRows) {
		return QueuedCard{}, false, nil
	}
	if err != nil {
		return QueuedCard{}, false, err
	}
	return QueuedCard{CardID: r.CardID, FlowID: r.FlowID, StageID: r.StageID, QueuedAt: fromMillis(r.QueuedAt)}, true, nil
}

// IsQueued reports whether a card is waiting for a place.
func (s *Store) IsQueued(cardID string) (bool, error) {
	var n int
	err := s.db.Get(&n, `SELECT COUNT(*) FROM stage_queue WHERE card_id = ?`, cardID)
	return n > 0, err
}

// QueuedOnStage counts the cards waiting for each stage of a flow.
func (s *Store) QueuedOnStage(flowID string) (map[string]int, error) {
	var rows []struct {
		StageID string `db:"stage_id"`
		N       int    `db:"n"`
	}
	if err := s.db.Select(&rows, `
		SELECT stage_id, COUNT(*) AS n FROM stage_queue WHERE flow_id = ? GROUP BY stage_id`, flowID); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.StageID] = r.N
	}
	return out, nil
}

// ---- idempotency ----

// Claim records a key and reports whether it was fresh. One event moves a card
// once: the engine claims "flow|<card>|<stage>|<trigger>" before it acts.
func (s *Store) Claim(key string) (bool, error) {
	res, err := s.db.Exec(`INSERT INTO idempotency (key, created_at) VALUES (?, ?) ON CONFLICT DO NOTHING`,
		key, millis(time.Now()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ForgetClaims drops keys older than the window, so a long-lived database does
// not keep every event it ever saw.
func (s *Store) ForgetClaims(olderThan time.Duration) error {
	_, err := s.db.Exec(`DELETE FROM idempotency WHERE created_at < ?`,
		millis(time.Now().Add(-olderThan)))
	return err
}

// ---- settings ----

// Setting reads a machine setting, or the fallback when it was never set.
func (s *Store) Setting(key, fallback string) (string, error) {
	var value string
	err := s.db.Get(&value, `SELECT value FROM setting WHERE key = ?`, key)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// SetSetting stores a machine setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO setting (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Settings is every setting, for a screen that shows them all.
func (s *Store) Settings() (map[string]string, error) {
	var rows []struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}
	if err := s.db.Select(&rows, `SELECT key, value FROM setting`); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

// IsEmpty reports a database nothing has been put in yet — what the first-run
// seeding asks before it offers anything.
func (s *Store) IsEmpty() (bool, error) {
	var n int
	if err := s.db.Get(&n, `SELECT (SELECT COUNT(*) FROM flow) + (SELECT COUNT(*) FROM source) + (SELECT COUNT(*) FROM card)`); err != nil {
		return false, fmt.Errorf("check whether the database is empty: %w", err)
	}
	return n == 0, nil
}
