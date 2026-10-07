package term

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/artipop/xxvi/internal/ptyhold"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// A terminal's bytes travel over a socket of their own rather than over the
// window's event bus. Events reach the page by splicing JavaScript into the
// webview, which is right for "something changed" and wrong for a build log:
// every chunk would become a JSON string executed as code.
//
// The socket needs a real listener, and the application does not otherwise have
// one — on macOS the page is served through a custom scheme, which has no
// TCP behind it. So one is opened here, on loopback only, and addressed by a
// token minted per run: the port is closed to the network by the operating
// system, and the token means another page on this machine cannot guess it.

// Listen opens the loopback listener the terminal sockets live on. It is called
// once, when the application starts.
func (m *Manager) Listen() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("open the terminals port: %w", err)
	}
	m.mu.Lock()
	m.token = uuid.NewString()
	m.addr = ln.Addr().String()
	m.mu.Unlock()

	server := &http.Server{Handler: http.HandlerFunc(m.serve)}
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			m.log.Error("terminals socket stopped", "err", err)
		}
	}()
	return nil
}

// Endpoint is where a screen connects: everything but the terminal's own id.
// Handed to the page rather than built there, because the port is chosen by the
// operating system and the token by this process.
func (m *Manager) Endpoint() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addr == "" {
		return ""
	}
	return "ws://" + m.addr + "/" + m.token + "/"
}

func (m *Manager) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	token := m.token
	m.mu.Unlock()

	rest, ok := strings.CutPrefix(r.URL.Path, "/"+token+"/")
	if !ok || token == "" || rest == "" || strings.Contains(rest, "/") {
		http.NotFound(w, r)
		return
	}
	session := m.Get(rest)
	// A terminal whose process has ended is still a screen of the ribbon, and
	// the ribbon is a journal: what it printed is served from the tail kept on
	// disk, then the socket says the same «exit» a shell that just finished
	// would have said. A step somebody scrolls back to shows what happened in
	// it, not an error about a process that was never going to be alive.
	if session == nil {
		if tail := m.transcript(rest); tail != nil {
			m.replay(w, r, rest, tail)
			return
		}
		http.Error(w, "terminal not found", http.StatusNotFound)
		return
	}

	// The origin is not checked, and that is the token's job here: the page is
	// served under a custom scheme whose origin never matches this host, so an
	// origin check could only ever be a check that always fails or one that
	// always passes. What guards the port is that it is on loopback and its
	// address carries a secret minted this run.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	m.pipe(conn, session)
}

// replay hands over a finished terminal and closes: there is nothing to type
// into and nothing more to wait for.
func (m *Manager) replay(w http.ResponseWriter, r *http.Request, id string, tail []byte) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tail = scrollableReplay(ctx, conn, tail, m.savedHistory(id))
	if err := write(ctx, conn, websocket.MessageBinary, append(tail, releaseInput...)); err != nil {
		return
	}
	_ = write(ctx, conn, websocket.MessageText, []byte(`{"type":"exit"}`))
}

// Older tails kept the alternate screen active. Rebuild those at the size the
// viewer sends on connection, just as it would have interpreted the snapshot.
func scrollableReplay(ctx context.Context, conn *websocket.Conn, tail, history []byte) []byte {
	if len(history) == 0 && !bytes.Contains(tail, []byte("\x1b[?1049h")) &&
		!bytes.Contains(tail, []byte("\x1b[?1047h")) &&
		!bytes.Contains(tail, []byte("\x1b[?47h")) {
		return tail
	}
	ready := make(chan control, 1)
	go func() {
		kind, data, err := conn.Read(ctx)
		var size control
		if err == nil && kind == websocket.MessageText {
			_ = json.Unmarshal(data, &size)
		}
		ready <- size
	}()
	cols, rows := 80, 24
	select {
	case size := <-ready:
		if size.Type == "resize" && size.Cols >= 2 && size.Cols <= 1000 && size.Rows >= 2 && size.Rows <= 1000 {
			cols, rows = size.Cols, size.Rows
		}
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
	}
	screen := ptyhold.NewScreen(cols, rows)
	defer screen.Close()
	screen.Write(tail)
	if len(history) > 0 {
		return screen.ReadOnlyWithHistory(history)
	}
	return screen.ReadOnlyBytes()
}

// releaseInput turns off what makes a finished terminal still act like a live
// one under the mouse: with reporting on, a click is a report for a process
// that is gone, and the text cannot be selected.
const releaseInput = "\x1b[?9l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1005l\x1b[?1006l\x1b[?1015l"

// resetScreen is RIS: the emulator forgets what it drew and every mode the
// process had set, so the history replayed after it lands on a blank screen.
const resetScreen = "\x1bc"

// control is the one message that is not raw bytes.
type control struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (m *Manager) pipe(conn *websocket.Conn, s *Session) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	history, updates, unsubscribe := s.Subscribe()
	defer func() { unsubscribe() }()
	view := s.View()
	defer view.Close()

	// Keystrokes in, and the one thing that is not a keystroke: how big the
	// window is. Without it the process keeps the default eighty columns while
	// the emulator draws whatever width it actually has, and every line breaks
	// in the wrong place.
	go func() {
		defer cancel()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			switch kind {
			case websocket.MessageBinary:
				if err := s.Write(data); err != nil {
					return
				}
			case websocket.MessageText:
				var msg control
				if json.Unmarshal(data, &msg) != nil {
					continue
				}
				if msg.Type == "resize" {
					if err := view.Resize(msg.Cols, msg.Rows); err != nil {
						m.log.Warn("could not resize the terminal", "terminal", s.ID, "err", err)
					}
				}
			}
		}
	}()

	// What it printed before this window opened comes first, so a screen opened
	// again is the screen that was there.
	if len(history) > 0 {
		if err := write(ctx, conn, websocket.MessageBinary, history); err != nil {
			return
		}
	}

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case chunk, ok := <-updates:
			if !ok && s.Alive() {
				// Dropped for falling behind, with a gap in what it was sent.
				// The screen is reset and drawn again from the history, which is
				// whole — the same thing a window opened just now would get.
				history, updates, unsubscribe = s.Subscribe()
				redraw := append([]byte(resetScreen), history...)
				if err := write(ctx, conn, websocket.MessageBinary, redraw); err != nil {
					return
				}
				continue
			}
			if !ok {
				// The subscription ends with the process. Say so, so the screen
				// draws it as a shell that finished rather than a connection
				// that broke.
				// A fullscreen CLI can end without leaving its alternate screen.
				// Replace it with the scrollable transcript before releasing input.
				tail := s.History()
				if history := m.savedHistory(s.ID); len(history) > 0 {
					s.mu.Lock()
					cols, rows := s.cols, s.rows
					s.mu.Unlock()
					screen := ptyhold.NewScreen(cols, rows)
					screen.Write(tail)
					tail = screen.ReadOnlyWithHistory(history)
					screen.Close()
				}
				final := append([]byte(resetScreen), tail...)
				final = append(final, releaseInput...)
				_ = write(ctx, conn, websocket.MessageBinary, final)
				_ = write(ctx, conn, websocket.MessageText, []byte(`{"type":"exit"}`))
				return
			}
			if err := write(ctx, conn, websocket.MessageBinary, chunk); err != nil {
				return
			}
		case <-ping.C:
			pctx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pctx)
			stop()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func write(ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, data []byte) error {
	wctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return conn.Write(wctx, kind, data)
}
