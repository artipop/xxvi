package acp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/artipop/xxvi/internal/model"
)

// A question is an agent asking the person whose card it is working, and
// waiting for the answer.
//
// Both of the ways ACP has to ask arrive here: session/request_permission, when
// the agent wants a tool the policy does not cover, and the elicitation, when
// it wants an answer in words — the claude CLI's own AskUserQuestion comes
// through as a form of one property with a `oneOf` and a free-text field.
//
// Asking does not stop the session: the SDK dispatches every inbound request on
// its own goroutine, so the agent keeps working and the turn is still open
// while the card waits. What stops is the one thing that asked.

// QuestionKind says which of the two arrived, because they are answered
// differently — one picks a permission option, the other fills in a form.
type QuestionKind string

const (
	QuestionPermission QuestionKind = "permission"
	QuestionForm       QuestionKind = "form"
)

// QuestionOption is one answer the agent offered.
type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Kind is the permission option's own kind (allow_once, reject_always, …),
	// kept because "always" is remembered for the rest of the session.
	Kind string `json:"kind,omitempty"`
}

// Question is what a card shows and what the UI answers.
type Question struct {
	ID        string       `json:"id"`
	SessionID string       `json:"sessionId"`
	CardID    string       `json:"cardId,omitempty"`
	CardTitle string       `json:"cardTitle,omitempty"`
	StageID   string       `json:"stageId,omitempty"`
	Agent     string       `json:"agent,omitempty"`
	Kind      QuestionKind `json:"kind"`
	// Text is the question itself: the tool call's title for a permission, the
	// agent's message for a form.
	Text    string           `json:"text"`
	Tool    string           `json:"tool,omitempty"`
	Options []QuestionOption `json:"options,omitempty"`
	// FreeText says an answer may be typed instead of chosen. The claude
	// adapter always offers one beside its options, and a form with no options
	// at all is nothing but this.
	FreeText bool      `json:"freeText"`
	AskedAt  time.Time `json:"askedAt"`

	// field/freeField are the form properties an accepted answer goes under.
	// Not sent to the UI: it answers with an option id and a text, and what
	// they are called on the wire is this package's business.
	field     string
	freeField string
}

// Answer is the reply. An answer with neither an option nor text is a refusal,
// which is also what an agent gets when the application closes with the
// question still open.
type Answer struct {
	OptionID string `json:"optionId,omitempty"`
	Text     string `json:"text,omitempty"`
	Declined bool   `json:"declined,omitempty"`
}

func (a Answer) empty() bool { return a.OptionID == "" && strings.TrimSpace(a.Text) == "" }

// pendingQuestion is one question and the channel its answer arrives on.
type pendingQuestion struct {
	q     Question
	reply chan Answer
}

// ask puts the question to the person and waits. ctx is the agent request's own
// context, so an agent that gives up on its question takes it back.
func (m *Manager) ask(ctx context.Context, s *session, q Question) Answer {
	q.ID = uuid.NewString()
	q.SessionID = s.id
	q.CardID = s.card.ID
	q.CardTitle = s.card.Title
	q.StageID = s.stage.ID
	q.Agent = s.agent.Name
	q.AskedAt = time.Now().UTC()

	reply := make(chan Answer, 1)
	m.questionsMu.Lock()
	if m.questions == nil {
		m.questions = map[string]*pendingQuestion{}
	}
	m.questions[q.ID] = &pendingQuestion{q: q, reply: reply}
	m.questionsMu.Unlock()

	s.event(m, "question", map[string]any{
		"questionId": q.ID, "kind": string(q.Kind), "text": q.Text, "tool": q.Tool,
	})
	// The journal is the durable record of everything a session does, and a
	// question is the one thing in it that was addressed to a person.
	m.record(s, model.EntryAsk, questionEntry(q))
	m.setStatus(s, statusAsking)
	m.emitAttention(q.attention())
	m.log.Info("агент спрашивает", "session", s.id, "card", q.CardID, "kind", q.Kind, "tool", q.Tool)

	var answer Answer
	select {
	case answer = <-reply:
	case <-ctx.Done():
		// The agent withdrew the question — a cancelled turn, or its own
		// timeout. Nothing to answer any more.
		answer = Answer{Declined: true}
	case <-m.rootCtx.Done():
		answer = Answer{Declined: true}
	}

	m.questionsMu.Lock()
	delete(m.questions, q.ID)
	m.questionsMu.Unlock()

	m.setStatus(s, statusRunning)
	closed := q.attention()
	closed.Awaiting = false
	m.emitAttention(closed)
	// What was answered goes into the stream, not just that something was:
	// the stream is where the ribbon shows the question, and an answer it
	// cannot read out is a gap right after it.
	s.event(m, "answer", map[string]any{
		"questionId": q.ID, "optionId": answer.OptionID, "declined": answer.Declined || answer.empty(),
		"text": answer.Text, "label": optionLabel(q, answer.OptionID),
	})
	m.record(s, model.EntryAsk, answerEntry(q, answer))
	return answer
}

// Answer delivers a person's answer. It is safe to call twice: the second call
// finds nothing to answer and says so.
func (m *Manager) Answer(id string, ans Answer) error {
	m.questionsMu.Lock()
	pending, ok := m.questions[id]
	m.questionsMu.Unlock()
	if !ok {
		return fmt.Errorf("вопрос уже неактуален")
	}
	select {
	case pending.reply <- ans:
		return nil
	default:
		// Buffered by one and removed by the asker, so a full channel means an
		// answer is already on its way.
		return fmt.Errorf("на этот вопрос уже отвечают")
	}
}

// Questions lists everything an agent is waiting to hear, oldest first: the one
// that has been ignored longest is the one worth showing.
func (m *Manager) Questions() []Question {
	m.questionsMu.Lock()
	out := make([]Question, 0, len(m.questions))
	for _, pending := range m.questions {
		out = append(out, pending.q)
	}
	m.questionsMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].AskedAt.Before(out[j].AskedAt) })
	return out
}

// WaitingFor reports whether anything about a card is waiting for a person: an
// open question, or a stage's terminal gone silent. It is the card's mark, and
// it reads the same bookkeeping as the attention list, so the panel and the card
// cannot disagree about one fact (docs/system.md §7).
func (m *Manager) WaitingFor(cardID string) bool {
	if cardID == "" {
		return false
	}
	if m.QuestionForCard(cardID) != nil {
		return true
	}
	m.questionsMu.Lock()
	defer m.questionsMu.Unlock()
	for _, a := range m.quiet {
		if a.CardID == cardID {
			return true
		}
	}
	return false
}

// QuestionForCard is a card's own open question, if it has one.
func (m *Manager) QuestionForCard(cardID string) *Question {
	if cardID == "" {
		return nil
	}
	for _, q := range m.Questions() {
		if q.CardID == cardID {
			return &q
		}
	}
	return nil
}

// Attention is one thing waiting for a person. There are two kinds of it, and
// which one a row is, is said by whether it carries a question.
//
// A **question** is the protocol asking: an ACP session sent a permission
// request or an elicitation, and the agent is waiting on the answer with its
// turn still open. It has options, and answering it here is what releases the
// agent.
//
// A **silent terminal** is the other one, and it is not answerable from here on
// purpose: the agent asked inside its own interface, where the question was
// never ours to carry (docs/system.md §4.1.1). What the row says is «сходи
// посмотри», and the answer is typed where it was asked.
//
// It is a separate shape from Question so that the panel and the mark on a card
// read the same list — one bookkeeping of the fact, not two.
type Attention struct {
	Key        string           `json:"key"`
	QuestionID string           `json:"questionId"`
	CardID     string           `json:"cardId,omitempty"`
	CardTitle  string           `json:"cardTitle,omitempty"`
	Agent      string           `json:"agent,omitempty"`
	Tool       string           `json:"tool,omitempty"`
	Text       string           `json:"text,omitempty"`
	Options    []QuestionOption `json:"options,omitempty"`
	FreeText   bool             `json:"freeText,omitempty"`
	// Awaiting is false in an event that says a wait ended; the list only ever
	// carries true.
	Awaiting bool      `json:"awaiting"`
	Since    time.Time `json:"since,omitempty"`

	// Worktree is set on the third kind: a closed card's separate working
	// tree, and whether to remove it (workspace.go). Branch is what stays
	// either way; Dirty says removing it loses uncommitted changes.
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
}

// attention describes an open question the way the UI wants it. The key is the
// question's own id rather than its card: an agent making two tool calls at once
// asks twice, and answering one must not take the other off the screen.
func (q Question) attention() Attention {
	return Attention{
		Key: "q:" + q.ID, QuestionID: q.ID, CardID: q.CardID, CardTitle: q.CardTitle,
		Agent: q.Agent, Tool: q.Tool, Text: q.Text, Options: q.Options, FreeText: q.FreeText,
		Awaiting: true, Since: q.AskedAt,
	}
}

// Attention lists everything waiting for a person, oldest first: the one that
// has been ignored longest is the one worth showing.
func (m *Manager) Attention() []Attention {
	questions := m.Questions()
	out := make([]Attention, 0, len(questions))
	for _, q := range questions {
		out = append(out, q.attention())
	}

	m.questionsMu.Lock()
	for _, a := range m.quiet {
		out = append(out, a)
	}
	m.questionsMu.Unlock()

	out = append(out, m.worktreeAttention()...)

	sort.SliceStable(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// raiseQuiet marks a stage in a terminal as waiting for a person. The session
// is marked too, so the card shows it the same way it shows an agent stopped on
// a question: from outside, both are a step that has stopped moving.
func (m *Manager) raiseQuiet(s *session) {
	a := Attention{
		Key:       "t:" + s.id,
		CardID:    s.card.ID,
		CardTitle: s.card.Title,
		Agent:     s.agent.Name,
		Text:      "Терминал агента молчит — посмотрите, не ждёт ли он ответа",
		Awaiting:  true,
		Since:     time.Now(),
	}
	m.questionsMu.Lock()
	m.quiet[s.id] = a
	m.questionsMu.Unlock()

	m.setStatus(s, statusAsking)
	m.emitAttention(a)
	m.log.Info("терминал стадии молчит", "session", s.id, "card", s.card.ID)
}

// clearQuiet takes the mark off — the CLI drew something, or the step ended.
func (m *Manager) clearQuiet(s *session) {
	m.questionsMu.Lock()
	a, had := m.quiet[s.id]
	delete(m.quiet, s.id)
	m.questionsMu.Unlock()
	if !had {
		return
	}
	if s.currentStatus() == statusAsking {
		m.setStatus(s, statusRunning)
	}
	a.Awaiting = false
	m.emitAttention(a)
}

func questionEntry(q Question) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Агент %s спрашивает:\n\n%s", q.Agent, q.Text)
	for _, opt := range q.Options {
		b.WriteString("\n- " + opt.Label)
		if opt.Description != "" {
			b.WriteString(" — " + opt.Description)
		}
	}
	return b.String()
}

func answerEntry(q Question, ans Answer) string {
	switch {
	case ans.Declined || ans.empty():
		return fmt.Sprintf("Вопрос агента %s остался без ответа — работа продолжена без него.", q.Agent)
	case ans.Text != "":
		return fmt.Sprintf("Ответ агенту %s: %s", q.Agent, ans.Text)
	default:
		return fmt.Sprintf("Ответ агенту %s: %s", q.Agent, optionLabel(q, ans.OptionID))
	}
}

func optionLabel(q Question, optionID string) string {
	for _, opt := range q.Options {
		if opt.ID == optionID {
			return opt.Label
		}
	}
	return optionID
}
