package term

import "github.com/artipop/xxvi/internal/ptyhold"

// engine is what a Session drives its process through. Output and the end come
// back the other way: into publish and finish.
type engine interface {
	Write([]byte) error
	Resize(cols, rows int) error
	// Hangup fails where there is nothing gentler than Kill to send.
	Hangup() error
	Kill()
	// Release lets go of what the ended process left behind.
	Release()
}

// local runs the process in the application, and it ends with it. What there
// is when the holder could not be started, and what tests use.
type local struct{ p *ptyhold.Proc }

func (l local) Write(b []byte) error        { return l.p.Write(b) }
func (l local) Resize(cols, rows int) error { return l.p.Resize(cols, rows) }
func (l local) Hangup() error               { return l.p.Hangup() }
func (l local) Kill()                       { l.p.Kill() }
func (l local) Release()                    { l.p.Close() }

// held is a session in the holder.
type held struct {
	c  *ptyhold.Client
	id string
}

func (h held) Write(b []byte) error        { return h.c.Write(h.id, b) }
func (h held) Resize(cols, rows int) error { return h.c.Resize(h.id, cols, rows) }
func (h held) Hangup() error               { return h.c.Hangup(h.id) }
func (h held) Kill()                       { _ = h.c.Kill(h.id) }

// Release forgets the session in the holder: by the time it has ended, its
// screen is in the Session already. Waited for, so an application closing right
// after does not leave a holder keeping an ended session for nobody.
func (h held) Release() { _ = h.c.Forget(h.id) }
