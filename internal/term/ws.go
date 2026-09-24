package term

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

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
			m.replay(w, r, tail)
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
func (m *Manager) replay(w http.ResponseWriter, r *http.Request, tail []byte) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := write(ctx, conn, websocket.MessageBinary, append(tail, releaseInput...)); err != nil {
		return
	}
	_ = write(ctx, conn, websocket.MessageText, []byte(`{"type":"exit"}`))
}

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
					if err := s.Resize(msg.Cols, msg.Rows); err != nil {
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
				_ = write(ctx, conn, websocket.MessageBinary, []byte(releaseInput))
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
