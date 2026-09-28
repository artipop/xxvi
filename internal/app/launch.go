package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/artipop/xxvi/internal/launch"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// The run screen (model.ScreenRun): the card's working copy started the way
// its kind of project is looked at. The process is a screen terminal like any
// other — same pty, same socket, same keying by screen — and what this file
// adds is choosing the command and, for an application with a window of its
// own, making room for that window.

// Windows is our own window, as far as making room for somebody else's goes.
// The window implements it; without one a launch still runs, and its window
// opens wherever it opens.
type Windows interface {
	// MakeRoom moves the window into its part of the screen and hands back
	// the rest. Asked again while already moved, it keeps the place it would
	// give back to: the one from before the first move.
	MakeRoom(split func(work launch.Rect) (ours, theirs launch.Rect)) (launch.Rect, error)
	// GiveBack puts the window where it was before MakeRoom.
	GiveBack()
}

// SetWindows supplies the window once it exists.
func (a *App) SetWindows(w Windows) {
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	a.windows = w
}

// EventLaunch says a run screen's room changed: the window moved aside, the
// application's window was found and placed, or everything went back.
const EventLaunch = "launch"

// Room states, as the run screen shows them.
const (
	RoomWaiting     = "waiting"     // our window moved aside, theirs not seen yet
	RoomPlaced      = "placed"      // theirs is beside ours
	RoomNoAccess    = "noAccess"    // theirs cannot be moved without permission
	RoomUnsupported = "unsupported" // theirs cannot be moved on this system
)

// room is the one window arrangement there can be: there is one window of
// ours to move aside.
type room struct {
	screenID string
	cancel   context.CancelFunc
}

// LaunchPlan is what a run screen opens on.
type LaunchPlan struct {
	// Profiles is what the working copy looks like it can be started as, the
	// likeliest first and a plain command last.
	Profiles []launch.Profile `json:"profiles"`
	// Chosen is what to offer: what was last started for this project if
	// anything was — a person's choice outranks a guess — and otherwise the
	// first profile.
	Chosen     launch.Profile `json:"chosen"`
	Remembered bool           `json:"remembered,omitempty"`
	// Clients are the HTTP clients installed here, for a backend.
	Clients []string `json:"clients,omitempty"`
	Docker  bool     `json:"docker"`
	// Running is the screen's process, when one is up.
	Running *LaunchRun `json:"running,omitempty"`
}

// LaunchRun is a started profile.
type LaunchRun struct {
	Terminal TerminalHandle `json:"terminal"`
	Profile  launch.Profile `json:"profile"`
	Room     string         `json:"room,omitempty"`
	App      string         `json:"app,omitempty"`
}

// runState is kept per screen so a pane scrolled away and back — or a ribbon
// re-read — finds the run it started rather than offering to start another.
type runState struct {
	profile launch.Profile
	room    string
	app     string
}

// LaunchPlan reads the card's working copy and says how it can be started.
// prefer is the stage's own hint (the screen's ref): which kind to put first.
func (s *API) LaunchPlan(cardID, screenID, prefer string) (LaunchPlan, error) {
	dir, err := s.app.Agents.WorkDir(cardID)
	if err != nil {
		return LaunchPlan{}, err
	}
	plan := LaunchPlan{
		Profiles: launch.Prefer(launch.Detect(dir), prefer),
		Clients:  launch.Clients(),
		Docker:   launch.Docker(),
	}
	plan.Chosen = plan.Profiles[0]
	if p, ok := s.app.rememberedLaunch(cardID); ok {
		plan.Chosen, plan.Remembered = p, true
	}
	if session := s.app.Terminals.OnScreen(screenID); session != nil && session.Alive() {
		st := s.app.runOn(cardID, screenID, session.Command)
		if st != nil {
			plan.Running = &LaunchRun{
				Terminal: s.handle(session.ID),
				Profile:  st.profile,
				Room:     st.room,
				App:      st.app,
			}
		}
	}
	return plan, nil
}

// runOn is what the screen is running. A run started before the application
// last closed is still going — the holder kept it (internal/ptyhold) — while
// what this run remembered about it is gone; it is taken to be what was last
// started for the project, when the command agrees, and a plain command when
// it does not.
func (a *App) runOn(cardID, screenID, command string) *runState {
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	if st := a.runs[screenID]; st != nil {
		return st
	}
	p := launch.Profile{Kind: model.LaunchCommand, Command: command}
	if remembered, ok := a.rememberedLaunch(cardID); ok && remembered.Command == command {
		p = remembered
	}
	if a.runs == nil {
		a.runs = map[string]*runState{}
	}
	st := &runState{profile: p}
	a.runs[screenID] = st
	return st
}

// StartLaunch runs a profile on a screen, replacing whatever that screen was
// running: a changed command is a restart, not a second process.
func (s *API) StartLaunch(cardID, screenID string, p launch.Profile) (LaunchRun, error) {
	if s.app.Terminals.Endpoint() == "" {
		return LaunchRun{}, msg.Err("terminal.disabled")
	}
	p.Command = strings.TrimSpace(p.Command)
	if !model.IsLaunchKind(p.Kind) {
		p.Kind = model.LaunchCommand
	}
	if old := s.app.Terminals.OnScreen(screenID); old != nil {
		old.Close()
	}
	s.app.rememberLaunch(cardID, p)

	st := &runState{profile: p}
	var before map[int]bool
	if p.Kind == model.LaunchDesktop || p.Kind == model.LaunchMobile {
		// Taken before the process starts, or its window would be in it.
		var err error
		before, err = launch.Snapshot()
		st.room = roomState(err)
	}
	s.app.roomMu.Lock()
	if s.app.runs == nil {
		s.app.runs = map[string]*runState{}
	}
	s.app.runs[screenID] = st
	s.app.roomMu.Unlock()

	session, err := s.app.Terminals.Open(cardID, screenID, p.Command)
	if err != nil {
		return LaunchRun{}, err
	}
	if st.room != "" {
		s.app.makeRoom(screenID, p.Kind, before, st.room == RoomWaiting)
	}
	return LaunchRun{Terminal: s.handle(session.ID), Profile: p, Room: s.app.roomOf(screenID)}, nil
}

// StopLaunch ends a screen's process and gives the screen back.
func (s *API) StopLaunch(screenID string) error {
	if session := s.app.Terminals.OnScreen(screenID); session != nil {
		session.Close()
	}
	s.app.giveBack(screenID)
	s.app.roomMu.Lock()
	delete(s.app.runs, screenID)
	s.app.roomMu.Unlock()
	return nil
}

// GiveBackWindow puts our window back while the application keeps running:
// somebody who has seen enough of it, or wants to arrange the two by hand.
func (s *API) GiveBackWindow(screenID string) error {
	s.app.giveBack(screenID)
	return nil
}

// LaunchAddress is where the screen's process said it can be reached, or
// nothing yet.
func (s *API) LaunchAddress(screenID string) string {
	session := s.app.Terminals.OnScreen(screenID)
	if session == nil {
		return ""
	}
	return launch.Address(session.History())
}

// SendRequest makes one request to a started service on the screen's behalf.
func (s *API) SendRequest(r launch.Request) (launch.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	return launch.Send(ctx, r)
}

// OpenClient starts one of the installed HTTP clients.
func (s *API) OpenClient(name string) error { return launch.OpenClient(name) }

func (s *API) handle(id string) TerminalHandle {
	return TerminalHandle{ID: id, URL: s.app.Terminals.Endpoint() + id}
}

func roomState(err error) string {
	switch {
	case err == nil:
		return RoomWaiting
	case errors.Is(err, launch.ErrNoAccess):
		return RoomNoAccess
	default:
		return RoomUnsupported
	}
}

// makeRoom moves our window aside and, when theirs can be found, waits for it
// and places it in the room. The window is moved even when theirs cannot be:
// the person drags theirs into the free part, which is still less than
// arranging both.
func (a *App) makeRoom(screenID, kind string, before map[int]bool, watch bool) {
	a.uiMu.RLock()
	windows := a.windows
	a.uiMu.RUnlock()

	a.roomMu.Lock()
	if a.room != nil {
		a.room.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.room = &room{screenID: screenID, cancel: cancel}
	a.roomMu.Unlock()

	theirs := launch.Rect{}
	if windows != nil {
		var err error
		theirs, err = windows.MakeRoom(func(work launch.Rect) (launch.Rect, launch.Rect) {
			return launch.Split(work, kind)
		})
		if err != nil {
			a.log.Info("could not make room for the application", "err", err)
		}
	}
	a.emitRoom(screenID)
	if !watch || theirs.W == 0 {
		return
	}

	var names []string
	if kind == model.LaunchMobile {
		names = launch.Simulators
	}
	go launch.Watch(ctx, before, names, theirs, kind != model.LaunchMobile, launch.Follow{
		Placed: func(name string) { a.setRoom(screenID, RoomPlaced, name) },
		Failed: func(err error) { a.setRoom(screenID, roomState(err), "") },
		// Their window closing is the person saying they are done looking,
		// and the screen is theirs again.
		Gone: func() { a.giveBack(screenID) },
	})
}

func (a *App) setRoom(screenID, state, name string) {
	a.roomMu.Lock()
	if st := a.runs[screenID]; st != nil {
		st.room, st.app = state, name
	}
	a.roomMu.Unlock()
	a.emitRoom(screenID)
}

// giveBack undoes makeRoom, if the room is this screen's. Another screen's
// room is left alone: it is somebody else's application standing there.
func (a *App) giveBack(screenID string) {
	a.roomMu.Lock()
	if a.room == nil || a.room.screenID != screenID {
		a.roomMu.Unlock()
		return
	}
	a.room.cancel()
	a.room = nil
	if st := a.runs[screenID]; st != nil {
		st.room, st.app = "", ""
	}
	a.roomMu.Unlock()

	a.uiMu.RLock()
	windows := a.windows
	a.uiMu.RUnlock()
	if windows != nil {
		windows.GiveBack()
	}
	a.emitRoom(screenID)
}

func (a *App) roomOf(screenID string) string {
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	if st := a.runs[screenID]; st != nil {
		return st.room
	}
	return ""
}

func (a *App) emitRoom(screenID string) {
	a.roomMu.Lock()
	payload := map[string]any{"screenId": screenID}
	if st := a.runs[screenID]; st != nil {
		payload["room"], payload["app"] = st.room, st.app
	}
	a.roomMu.Unlock()
	a.Emit(EventLaunch, payload)
}

// What was started is remembered for the project rather than the card: the
// next MR of the same repository is started the same way, and that is the
// whole point of remembering. A card with no project has only itself.
func launchKey(card model.Card) string {
	if card.Project != "" {
		return "launch.project." + card.Project
	}
	return "launch.card." + card.ID
}

func (a *App) rememberLaunch(cardID string, p launch.Profile) {
	card, err := a.Store.Card(cardID)
	if err != nil {
		return
	}
	b, _ := json.Marshal(p)
	if err := a.Store.SetSetting(launchKey(card), string(b)); err != nil {
		a.log.Info("launch choice not remembered", "err", err)
	}
}

func (a *App) rememberedLaunch(cardID string) (launch.Profile, bool) {
	card, err := a.Store.Card(cardID)
	if err != nil {
		return launch.Profile{}, false
	}
	raw, err := a.Store.Setting(launchKey(card), "")
	if err != nil || raw == "" {
		return launch.Profile{}, false
	}
	var p launch.Profile
	if json.Unmarshal([]byte(raw), &p) != nil || !model.IsLaunchKind(p.Kind) {
		return launch.Profile{}, false
	}
	return p, true
}
