// Package appmcp is this application from the outside: the moves a person makes
// on a card, offered to an agent that nobody here started.
//
// It is the twin of internal/stagemcp and not the same thing. There an agent we
// launched reports the one step it was given, and the grant dies with the run.
// Here the caller is somebody else's session — a CLI in another window, an
// agent on a schedule — and what it may do is what a person may do: read the
// inbox, put a card on a flow, set a value, say a step is over. Nothing new
// happens to a card because the caller was an agent: every tool ends in one of
// the engine's three roads (engine.TakeIntoWork, CardChanged, Finished), and a
// fourth road is exactly what this must not become.
//
// The door is the one stagemcp uses — loopback HTTP and a bearer token — with
// one difference that follows from the caller outliving nothing: the token is
// the install's, kept in the database, rather than minted per run. Where the
// port is, is written beside it (handoff.go), because the port is new on every
// launch and a configuration file is not.
package appmcp

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/store"
)

// ServerName is how a CLI is told to call these tools. The same short name
// stagemcp uses: a session sees one of the two and never both.
const ServerName = "xxvi"

// tokenSetting is where the install's token lives. In the database rather than
// in a file, because it is as long-lived as the cards are and everything else
// that long-lived is there.
const tokenSetting = "mcp.token"

// Filer files a card somebody typed — the inbox pipeline, which is what makes a
// card added here an ordinary card rather than a row written behind the rules.
type Filer interface {
	AddManual(source, title, body string, props map[string]string) (model.Card, error)
}

// Folders is where a card's work happens: the same folder its agents work in,
// so a page an outside agent writes is the page the ribbon's browser opens.
type Folders interface {
	WorkDir(cardID string) (string, error)
}

// Deps is everything these tools touch. Interfaces rather than the application
// itself: internal/app wires this package, and a handle on the application
// would be a cycle.
type Deps struct {
	Store  *store.Store
	Engine *engine.Engine
	// Filer and Folders may be nil, and the tools that need them then say what
	// is missing instead of panicking — a headless run has no inbox pipeline.
	Filer   Filer
	Folders Folders
	// Emit tells the UI a card changed. Optional: without a window there is
	// nobody to tell, and the state is in the database either way.
	Emit func(event string, payload any)
}

// Server is the loopback listener, the install's token, and the tools on it.
type Server struct {
	deps Deps
	log  *slog.Logger

	mu    sync.Mutex
	addr  string
	token string

	http *http.Server
}

// New builds the server. Listen opens it.
func New(deps Deps, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{deps: deps, log: log}
}

// Listen opens the port and settles the token, reading the one this install
// already has or minting it on a first run.
//
// A failure here is not fatal: it costs outside agents their way in, and
// everything a person does in the window works without it.
func (s *Server) Listen() error {
	token, err := s.deps.Store.Setting(tokenSetting, "")
	if err != nil {
		return fmt.Errorf("прочитать токен инструментов приложения: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		token = uuid.NewString()
		if err := s.deps.Store.SetSetting(tokenSetting, token); err != nil {
			return fmt.Errorf("записать токен инструментов приложения: %w", err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("открыть порт инструментов приложения: %w", err)
	}
	srv := &http.Server{Handler: s.handler()}

	s.mu.Lock()
	s.addr, s.token, s.http = ln.Addr().String(), token, srv
	s.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("сервер инструментов приложения остановлен", "err", err)
		}
	}()
	return nil
}

// URL is where a caller connects. Empty until the port is open, which is how a
// caller learns there is nothing to connect to.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addr == "" {
		return ""
	}
	return "http://" + s.addr + "/mcp"
}

// Token is the install's bearer token.
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// Close stops the listener.
func (s *Server) Close() {
	s.mu.Lock()
	srv := s.http
	s.http, s.addr = nil, ""
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

func (s *Server) allowed(r *http.Request) bool {
	want := s.Token()
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// handler serves the tools over MCP's HTTP transport, stateless for the same
// reason stagemcp is: one request carries its own right to be here, and a
// session id that outlived the check would be a second way in.
func (s *Server) handler() http.Handler {
	inner := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if !s.allowed(r) {
			return nil
		}
		return s.mcp()
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowed(r) {
			http.Error(w, "неверный токен", http.StatusForbidden)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// instructions is what a session reads before it calls anything: the shape of
// the application in the smallest number of sentences that still makes the
// tools predictable.
const instructions = `Это XXVI — входящие и флоу. Задача живёт карточкой: она лежит во входящих,
человек или ты ставишь её на флоу (take_into_work), и дальше она едет по стадиям.

Стадия — единственное место, где карточка стоит. Шаг на ней кончается отчётом
(finish_step): исход шага плюс значения, которые стадия обязалась оставить на
карточке. Куда карточка поедет после отчёта, решает флоу, а не вызывающий, —
поэтому «перейти к следующему шагу» здесь и есть finish_step с исходом done.

Стадия, на которой ничего не выполняется, ждёт ответа: там finish_step ставит
«Исход», а любое другое ожидаемое значение ставится set_property.`

// mcp is the tool set. Built per request rather than once: the server is
// stateless, and a tool set holding nothing per-call costs nothing to build.
func (s *Server) mcp() *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Title: "XXVI", Version: "1"},
		&mcp.ServerOptions{Instructions: instructions},
	)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "flows",
		Description: "Какие есть флоу: стадии, что на них происходит, какие значения они оставляют на карточке и по каким рёбрам карточка едет дальше.",
	}, s.flows)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cards",
		Description: "Карточки: во входящих, в работе или закрытые. Для тех, что в работе, сказано, на какой стадии стоят и чего ждут.",
	}, s.cards)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "card",
		Description: "Одна карточка целиком: текст, свойства, стадия, чего она ждёт, рабочая папка и последние записи в её истории.",
	}, s.card)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_card",
		Description: "Завести карточку во входящих. Она никуда не едет, пока её не поставят на флоу.",
	}, s.addCard)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "take_into_work",
		Description: "Поставить карточку из входящих на входную стадию флоу. Это единственный способ начать движение.",
	}, s.takeIntoWork)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "finish_step",
		Description: "Сказать, что текущий шаг карточки закончен, и чем — и тем самым перейти к следующему. " +
			"На агентской стадии это отчёт о работе: исход done или failed плюс объявленные значения. " +
			"На стадии, где ничего не выполняется, это ответ за неё: done ставит «Исход» = «прошло», failed — «не прошло». " +
			"Куда поедет карточка, решает флоу.",
	}, s.finishStep)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_property",
		Description: "Поставить свойство карточки. Если стадия ждёт именно это значение, карточка поедет дальше; " +
			"любое другое свойство просто ляжет на карточку.",
	}, s.setProperty)
	return srv
}

// ---- tools ----

type noInput struct{}

func (s *Server) flows(_ context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	flows, err := s.deps.Store.Flows()
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	if len(flows) == 0 {
		return text("Флоу пока нет."), nil, nil
	}
	var b strings.Builder
	for i, f := range flows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(describeFlow(f))
	}
	return text(b.String()), nil, nil
}

type cardsInput struct {
	State string `json:"state,omitempty" jsonschema:"какие карточки: work — в работе на флоу (по умолчанию), inbox — во входящих, done — закрытые"`
}

func (s *Server) cards(_ context.Context, _ *mcp.CallToolRequest, in cardsInput) (*mcp.CallToolResult, any, error) {
	state := model.StateFlow
	switch strings.ToLower(strings.TrimSpace(in.State)) {
	case "", "work", "flow":
	case "inbox":
		state = model.StateInbox
	case "done":
		state = model.StateDone
	case "dropped":
		state = model.StateDropped
	default:
		return errorf("«%s» — не состояние карточки. Есть work, inbox, done и dropped.", in.State), nil, nil
	}
	cards, err := s.deps.Store.CardsInState(state)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	if len(cards) == 0 {
		return text("Таких карточек нет."), nil, nil
	}
	var b strings.Builder
	for _, c := range cards {
		fmt.Fprintf(&b, "- «%s» [%s]", c.Title, c.ID)
		if place, err := s.deps.Engine.CardFlowFor(c.ID); err == nil && place != nil {
			fmt.Fprintf(&b, " — флоу «%s», стадия «%s»", place.FlowName, stageName(*place))
			switch {
			case place.Running:
				b.WriteString(", шаг работает агент приложения")
			case place.Queued:
				b.WriteString(", ждёт места на стадии")
			case len(place.WaitingFor) > 0:
				fmt.Fprintf(&b, ", ждёт: %s", strings.Join(place.WaitingFor, "; "))
			}
		}
		b.WriteString("\n")
	}
	return text(b.String()), nil, nil
}

type cardInput struct {
	Card string `json:"card" jsonschema:"карточка: её id или заголовок"`
}

func (s *Server) card(_ context.Context, _ *mcp.CallToolRequest, in cardInput) (*mcp.CallToolResult, any, error) {
	card, err := s.resolveCard(in.Card)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	return text(s.describeCard(card)), nil, nil
}

type addCardInput struct {
	Title string `json:"title" jsonschema:"заголовок — то, что нужно сделать"`
	Body  string `json:"body,omitempty" jsonschema:"подробности: что известно о задаче"`
}

func (s *Server) addCard(_ context.Context, _ *mcp.CallToolRequest, in addCardInput) (*mcp.CallToolResult, any, error) {
	if s.deps.Filer == nil {
		return errorf("карточки сейчас заводить нечем — входящие не подняты"), nil, nil
	}
	card, err := s.deps.Filer.AddManual("", in.Title, in.Body, nil)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	return text(fmt.Sprintf("Карточка «%s» [%s] лежит во входящих. Чтобы она поехала, поставьте её на флоу (take_into_work).",
		card.Title, card.ID)), nil, nil
}

type takeInput struct {
	Card   string `json:"card" jsonschema:"карточка: её id или заголовок"`
	Flow   string `json:"flow" jsonschema:"флоу: его название или id"`
	Worker string `json:"worker,omitempty" jsonschema:"кто работает шаги: имя агента из реестра приложения — и тогда его запускает приложение; любое другое имя — и приложение не запускает никого, шаги работает тот, кто зовёт эти инструменты"`
}

func (s *Server) takeIntoWork(_ context.Context, _ *mcp.CallToolRequest, in takeInput) (*mcp.CallToolResult, any, error) {
	card, err := s.resolveCard(in.Card)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	flow, err := s.resolveFlow(in.Flow)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	// The worker is set before the card moves: the entry stage starts the
	// moment it arrives, and an assignee arriving after that would be a
	// decision taken too late.
	if who := strings.TrimSpace(in.Worker); who != "" {
		if _, err := s.deps.Store.UpdateCard(card.ID, store.CardEdit{Assignee: &who}); err != nil {
			return errorf("%v", err), nil, nil
		}
	}
	if err := s.deps.Engine.TakeIntoWork(card.ID, flow.ID); err != nil {
		return errorf("%v", err), nil, nil
	}
	s.emit(card.ID)
	return text(s.describeCard(s.reread(card))), nil, nil
}

type finishInput struct {
	Card       string            `json:"card" jsonschema:"карточка: её id или заголовок"`
	Outcome    string            `json:"outcome" jsonschema:"как кончился шаг: done или failed"`
	Summary    string            `json:"summary" jsonschema:"что сделано, в нескольких предложениях — это ложится в историю карточки и это читает следующая стадия"`
	Properties map[string]string `json:"properties,omitempty" jsonschema:"значения, которые стадия обязалась оставить на карточке, по имени свойства"`
}

// finishStep ends the step a card stands on. Which of the engine's roads that
// is depends on the stage and not on the caller: a stage that runs something
// has an outcome, and a stage that runs nothing has an answer.
func (s *Server) finishStep(_ context.Context, _ *mcp.CallToolRequest, in finishInput) (*mcp.CallToolResult, any, error) {
	card, err := s.resolveCard(in.Card)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	var ok bool
	switch strings.ToLower(strings.TrimSpace(in.Outcome)) {
	case "done", "success":
		ok = true
	case "failed", "failure", "fail":
	default:
		return errorf("«%s» — не исход шага. Ожидается done или failed.", in.Outcome), nil, nil
	}

	st, onFlow, err := s.deps.Store.FlowState(card.ID)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	if !onFlow {
		return errorf("карточка «%s» не в работе (%s) — шага, который можно закончить, у неё нет. Поставьте её на флоу: take_into_work.",
			card.Title, card.State), nil, nil
	}
	flow, err := s.deps.Store.Flow(st.FlowID)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	stage, found := flow.Stage(st.StageID)
	if !found {
		return errorf("стадия карточки исчезла из флоу «%s» — переставьте карточку в приложении", flow.Name), nil, nil
	}
	if s.running(card.ID) {
		return errorf("этот шаг работает агент приложения — отчёт придёт от него. " +
			"Если шаг нужно забрать себе, отмените его в приложении."), nil, nil
	}

	if stage.Action == model.ActionAgent {
		if missing := missingRequired(stage.Writes, ok, in.Properties); missing != "" {
			return errorf("шаг не закончен: не хватает %s. Допишите значение и позовите ещё раз.", missing), nil, nil
		}
		on := model.TriggerSuccess
		if !ok {
			on = model.TriggerFailure
		}
		s.deps.Engine.Finished(card.ID, on, "отчёт через MCP", reportText(stage.Writes, in.Summary, in.Properties))
		return text(s.moved(card, flow, stage)), nil, nil
	}

	// A stage that runs nothing has no outcome of its own: what moves it is a
	// person's mark, and this is that mark made by somebody else. Refused where
	// the stage is not waiting for it, because a value nothing reads is a
	// caller left believing the card will move.
	if !flow.WatchesProperty(stage.ID, model.OutcomeProperty) {
		waits := flow.WaitDescriptions(stage.ID)
		if len(waits) == 0 {
			return errorf("стадия «%s» не ждёт ответа — отсюда карточку двигают в приложении.", stage.Name), nil, nil
		}
		return errorf("стадия «%s» ждёт не исхода шага, а %s. Поставьте это значение: set_property.",
			stage.Name, strings.Join(waits, "; ")), nil, nil
	}
	if err := s.putProps(card.ID, in.Properties); err != nil {
		return errorf("%v", err), nil, nil
	}
	if summary := strings.TrimSpace(in.Summary); summary != "" {
		if _, err := s.deps.Store.Record(model.JournalEntry{CardID: card.ID, Kind: model.EntryReport, Text: summary}); err != nil {
			s.log.Warn("не удалось записать итог шага", "card", card.ID, "err", err)
		}
	}
	value := model.OutcomePassed
	if !ok {
		value = model.OutcomeFailed
	}
	if _, err := s.deps.Store.UpdateCard(card.ID, store.CardEdit{
		Props: map[string]string{model.OutcomeProperty: value},
	}); err != nil {
		return errorf("%v", err), nil, nil
	}
	s.deps.Engine.CardChanged(card.ID, model.OutcomeProperty, value)
	s.emit(card.ID)
	return text(s.moved(card, flow, stage)), nil, nil
}

type propInput struct {
	Card     string `json:"card" jsonschema:"карточка: её id или заголовок"`
	Property string `json:"property" jsonschema:"название свойства, как его спрашивает флоу"`
	Value    string `json:"value" jsonschema:"значение"`
}

func (s *Server) setProperty(_ context.Context, _ *mcp.CallToolRequest, in propInput) (*mcp.CallToolResult, any, error) {
	card, err := s.resolveCard(in.Card)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	name := strings.TrimSpace(in.Property)
	if name == "" {
		return errorf("у свойства нет названия"), nil, nil
	}
	if _, err := s.deps.Store.UpdateCard(card.ID, store.CardEdit{Props: map[string]string{name: in.Value}}); err != nil {
		return errorf("%v", err), nil, nil
	}
	// Told to the engine the way a person's edit is told: whether any stage was
	// waiting for exactly this is the engine's to decide.
	if strings.TrimSpace(in.Value) != "" {
		s.deps.Engine.CardChanged(card.ID, name, in.Value)
	}
	s.emit(card.ID)
	return text(s.describeCard(s.reread(card))), nil, nil
}

// ---- what the tools answer with ----

// moved is the answer to a report: where the card is now, and — when it did not
// move — what the flow said about that, because the engine writes the reason
// into the card's own history rather than returning it.
func (s *Server) moved(card model.Card, flow model.Flow, from model.Stage) string {
	fresh := s.reread(card)
	var b strings.Builder
	place, _ := s.deps.Engine.CardFlowFor(card.ID)
	switch {
	case fresh.State == model.StateDone:
		fmt.Fprintf(&b, "Шаг записан. Карточка «%s»: «%s» → флоу «%s» пройден, карточка закрыта.", fresh.Title, from.Name, flow.Name)
	case place == nil:
		fmt.Fprintf(&b, "Шаг записан. Карточка «%s» больше не на флоу (%s).", fresh.Title, fresh.State)
	case place.StageID == from.ID:
		fmt.Fprintf(&b, "Шаг записан, но карточка «%s» осталась на «%s».", fresh.Title, from.Name)
	default:
		fmt.Fprintf(&b, "Шаг записан. Карточка «%s»: «%s» → «%s».", fresh.Title, from.Name, stageName(*place))
	}
	if outcome := fresh.Props[model.OutcomeProperty]; outcome != "" {
		fmt.Fprintf(&b, " Исход: %s.", outcome)
	}
	if last := s.lastEntries(card.ID, 2); last != "" {
		b.WriteString("\n\n")
		b.WriteString(last)
	}
	return b.String()
}

// describeCard is one card as a caller needs it: the task, what stands on it,
// where it is, and what the stage it is on expects.
func (s *Server) describeCard(card model.Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "«%s» [%s]\nСостояние: %s", card.Title, card.ID, card.State)
	if card.Assignee != "" {
		fmt.Fprintf(&b, ", работает: %s", card.Assignee)
	}
	b.WriteString("\n")
	if body := strings.TrimSpace(card.Body); body != "" {
		fmt.Fprintf(&b, "\n%s\n", body)
	}
	if len(card.Props) > 0 {
		b.WriteString("\nСвойства:\n")
		for _, name := range sortedKeys(card.Props) {
			fmt.Fprintf(&b, "- «%s» = «%s»\n", name, card.Props[name])
		}
	}
	if s.deps.Folders != nil {
		if dir, err := s.deps.Folders.WorkDir(card.ID); err == nil {
			fmt.Fprintf(&b, "\nРабочая папка: %s\n", dir)
		}
	}
	place, err := s.deps.Engine.CardFlowFor(card.ID)
	if err == nil && place != nil {
		fmt.Fprintf(&b, "\nФлоу «%s», стадия «%s»", place.FlowName, stageName(*place))
		switch {
		case place.Running:
			b.WriteString(" — шаг работает агент приложения, отчёт придёт от него")
		case place.Queued:
			b.WriteString(" — ждёт места на стадии")
		}
		b.WriteString("\n")
		if flow, err := s.deps.Store.Flow(place.FlowID); err == nil {
			if stage, ok := flow.Stage(place.StageID); ok {
				b.WriteString(describeStage(flow, stage, "  ", card.Props))
			}
		}
		if len(place.WaitingFor) > 0 {
			fmt.Fprintf(&b, "  Ждёт: %s\n", strings.Join(place.WaitingFor, "; "))
		}
	}
	if last := s.lastEntries(card.ID, 5); last != "" {
		b.WriteString("\nПоследнее в журнале:\n")
		b.WriteString(last)
	}
	return b.String()
}

// describeFlow is a flow as something to choose by and to walk: the stages with
// what they do and owe, then the arrows with their conditions.
func describeFlow(f model.Flow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Флоу «%s» [%s]\n", f.Name, f.ID)
	if d := strings.TrimSpace(f.Description); d != "" {
		fmt.Fprintf(&b, "%s\n", d)
	}
	for _, st := range f.Stages {
		fmt.Fprintf(&b, "- «%s» [%s]", st.Name, st.ID)
		switch {
		case st.Final:
			b.WriteString(" — финал: сюда карточка приезжает закрытой")
		case st.Action == model.ActionAgent:
			fmt.Fprintf(&b, " — работает агент (%s)", model.WorkLabel(st.Work))
			if st.ID == f.EntryStage {
				b.WriteString(", входная")
			}
		default:
			b.WriteString(" — ничего не выполняется, стадия ждёт ответа")
			if st.ID == f.EntryStage {
				b.WriteString(", входная")
			}
		}
		b.WriteString("\n")
		b.WriteString(describeStage(f, st, "  ", nil))
	}
	for _, e := range f.Edges {
		from, _ := f.Stage(e.From)
		to, _ := f.Stage(e.To)
		fmt.Fprintf(&b, "→ «%s» —%s→ «%s»", from.Name, model.TriggerLabel(e.On), to.Name)
		if desc := e.If.Describe(); desc != "" {
			fmt.Fprintf(&b, " при %s", desc)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// describeStage is what a caller working this stage has to know: the values it
// owes the card, and the screens a person will be looking at while it works.
//
// With a card's properties in hand the screens are named as that card will see
// them — «{Страница}» is the address the browser actually opens, and a caller
// that has to produce it is better off reading it resolved than guessing what
// the placeholder becomes.
func describeStage(f model.Flow, st model.Stage, indent string, props map[string]string) string {
	var b strings.Builder
	if p := strings.TrimSpace(st.Prompt); p != "" {
		fmt.Fprintf(&b, "%sЗадание стадии: %s\n", indent, p)
	}
	for _, w := range st.Writes {
		fmt.Fprintf(&b, "%sОставляет на карточке: «%s»", indent, w.Property)
		if w.Required {
			b.WriteString(" — обязательно, без него шаг не закончится")
		}
		b.WriteString("\n")
	}
	for _, sc := range st.Screens {
		fmt.Fprintf(&b, "%sЭкран: %s", indent, model.ScreenKindLabel(sc.Kind))
		if sc.Title != "" {
			fmt.Fprintf(&b, " «%s»", sc.Title)
		}
		if sc.Ref != "" {
			ref, waiting := model.ResolveRef(sc.Ref, props)
			fmt.Fprintf(&b, " → %s", ref)
			// Only where there is a card to be waiting: without one the
			// placeholder is the answer, not a value that is missing.
			if props != nil && len(waiting) > 0 {
				fmt.Fprintf(&b, " (ждёт значения: %s)", strings.Join(waiting, ", "))
			}
		}
		b.WriteString("\n")
	}
	for _, m := range f.MarksFrom(st.ID) {
		fmt.Fprintf(&b, "%sОтвет «%s» ведёт на «%s»\n", indent, m.Value, m.Stage)
	}
	return b.String()
}

// ---- helpers ----

// resolveCard takes an id or a title, because a caller that has just read a
// list has the title in hand and an id is what a list is for.
func (s *Server) resolveCard(ref string) (model.Card, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.Card{}, fmt.Errorf("не сказано, какая карточка")
	}
	if card, err := s.deps.Store.Card(ref); err == nil {
		return card, nil
	}
	var found []model.Card
	for _, state := range []model.CardState{model.StateFlow, model.StateInbox, model.StateDone, model.StateDropped} {
		cards, err := s.deps.Store.CardsInState(state)
		if err != nil {
			return model.Card{}, err
		}
		for _, c := range cards {
			if strings.EqualFold(strings.TrimSpace(c.Title), ref) {
				found = append(found, c)
			}
		}
	}
	switch len(found) {
	case 0:
		return model.Card{}, fmt.Errorf("карточка «%s» не найдена — посмотрите список: cards", ref)
	case 1:
		return found[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "карточек с заголовком «%s» несколько, назовите id:", ref)
	for _, c := range found {
		fmt.Fprintf(&b, " %s (%s)", c.ID, c.State)
	}
	return model.Card{}, fmt.Errorf("%s", b.String())
}

func (s *Server) resolveFlow(ref string) (model.Flow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.Flow{}, fmt.Errorf("не сказано, какое флоу")
	}
	if flow, err := s.deps.Store.Flow(ref); err == nil {
		return flow, nil
	}
	if flow, err := s.deps.Store.FlowByName(ref); err == nil {
		return flow, nil
	}
	return model.Flow{}, fmt.Errorf("флоу «%s» не найдено — посмотрите список: flows", ref)
}

// reread is the card as it is now. Every tool answers with the state after its
// own move, and the card in hand is the state before it.
func (s *Server) reread(card model.Card) model.Card {
	if fresh, err := s.deps.Store.Card(card.ID); err == nil {
		return fresh
	}
	return card
}

// running reports a live session on the card — the same answer the card screen
// gives, read from the same rows: a session is live while its status is not
// terminal.
func (s *Server) running(cardID string) bool {
	sessions, err := s.deps.Store.SessionsForCard(cardID)
	if err != nil {
		return false
	}
	for _, sess := range sessions {
		if !sess.Status.Terminal() {
			return true
		}
	}
	return false
}

func (s *Server) putProps(cardID string, props map[string]string) error {
	if len(props) == 0 {
		return nil
	}
	clean := map[string]string{}
	for name, value := range props {
		if name = strings.TrimSpace(name); name != "" {
			clean[name] = value
		}
	}
	if len(clean) == 0 {
		return nil
	}
	_, err := s.deps.Store.UpdateCard(cardID, store.CardEdit{Props: clean})
	return err
}

func (s *Server) lastEntries(cardID string, n int) string {
	entries, err := s.deps.Store.Journal(cardID)
	if err != nil || len(entries) == 0 {
		return ""
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(e.Text))
	}
	return b.String()
}

func (s *Server) emit(cardID string) {
	if s.deps.Emit != nil {
		s.deps.Emit(engine.EventCard, map[string]any{"cardId": cardID})
	}
}

// missingRequired names the values a report came without, or nothing. The same
// refusal a stage worked in a terminal gets (internal/stagemcp): a step that
// owes a value has not ended until the value stands, and the caller has to know
// which one to add.
func missingRequired(writes []model.PropertyWrite, ok bool, props map[string]string) string {
	if !ok {
		// A failed step is telling us why the values do not exist.
		return ""
	}
	var missing []string
	for _, w := range writes {
		if w.Required && strings.TrimSpace(props[w.Property]) == "" {
			missing = append(missing, "«"+w.Property+"»")
		}
	}
	return strings.Join(missing, ", ")
}

// reportText is the report in the shape the engine already reads: what was done,
// then the declared values, one line each. One currency for a session's closing
// words, a terminal's report and this one — one thing to explain rather than
// three.
func reportText(writes []model.PropertyWrite, summary string, props map[string]string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(summary))
	declared := map[string]bool{}
	for _, w := range writes {
		declared[w.Property] = true
	}
	for _, name := range sortedKeys(props) {
		if !declared[name] {
			continue
		}
		if value := strings.TrimSpace(props[name]); value != "" {
			fmt.Fprintf(&b, "\n%s: %s", name, value)
		}
	}
	return strings.TrimSpace(b.String())
}

// stageName is the stage's own name, which the card's view knows and does not
// hand over directly.
func stageName(place engine.CardFlow) string {
	for _, st := range place.Stages {
		if st.ID == place.StageID {
			return st.Name
		}
	}
	return place.StageID
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func text(s string) *mcp.CallToolResult {
	if strings.TrimSpace(s) == "" {
		s = "(пусто)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errorf(format string, args ...any) *mcp.CallToolResult {
	res := text(fmt.Sprintf(format, args...))
	res.IsError = true
	return res
}
