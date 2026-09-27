package launch

import (
	"context"
	"errors"
	"time"

	"github.com/artipop/xxvi/internal/model"
)

// Rect is an area of the desktop in logical points, from the top-left corner
// of the primary screen — the space Wails places its window in, and the one
// System Events places everybody else's.
type Rect struct {
	X, Y, W, H int
}

// Somebody else's window is put beside ours rather than into it. Embedding a
// foreign window means a native handle per platform and a process that was
// never written to be a child; two windows side by side are what a person
// would arrange by hand, and closing theirs gives the whole screen back.

// Split divides a screen's work area between our window, on the left, and the
// application being looked at. A phone simulator is narrow and keeps its own
// shape, so it is given a strip; a desktop application is given most of it,
// since it is what is being looked at.
func Split(work Rect, kind string) (ours, theirs Rect) {
	const minOurs = 480
	theirW := work.W * 3 / 5
	if kind == model.LaunchMobile {
		theirW = min(520, work.W/2)
	}
	ourW := max(minOurs, work.W-theirW)
	theirW = work.W - ourW
	ours = Rect{X: work.X, Y: work.Y, W: ourW, H: work.H}
	theirs = Rect{X: work.X + ourW, Y: work.Y, W: theirW, H: work.H}
	return ours, theirs
}

// Reasons a window could not be placed. Neither stops the run: the
// application is up, and the person moves its window into the free half.
var (
	ErrUnsupported = errors.New("placing another application's window is not supported here")
	ErrNoAccess    = errors.New("no permission to move other applications' windows")
)

// Follow is told how a started application's window fared.
type Follow struct {
	// Placed is called once its window is in the area, with the
	// application's name.
	Placed func(name string)
	// Failed is called when it cannot be placed: no permission, or no way to
	// do it on this system.
	Failed func(err error)
	// Gone is called when the placed window has closed.
	Gone func()
}

// how long a build may take before its window is given up on: a first
// Flutter or Xcode build is minutes, not seconds.
const (
	waitWindow = 10 * time.Minute
	pollEvery  = time.Second
)

// Watch waits for the window of an application started after before was
// taken — or of one of names, which is how a simulator that was already open
// is found — puts it into area, and then waits for it to close. It returns
// when that happens or ctx ends.
func Watch(ctx context.Context, before map[int]bool, names []string, area Rect, resize bool, f Follow) {
	deadline := time.After(waitWindow)
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()

	var pid int
	var name string
find:
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case <-tick.C:
		}
		procs, err := guiProcesses()
		if err != nil {
			f.Failed(err)
			return
		}
		for _, p := range procs {
			if before[p.pid] && !matches(p.name, names) {
				continue
			}
			n, err := windowCount(p.pid)
			if errors.Is(err, ErrNoAccess) || errors.Is(err, ErrUnsupported) {
				f.Failed(err)
				return
			}
			if n > 0 {
				pid, name = p.pid, p.name
				break find
			}
		}
	}

	if err := place(pid, area, resize); err != nil {
		f.Failed(err)
		return
	}
	f.Placed(name)

	// Two empty looks in a row: an application swapping a splash screen for
	// its main window has none for a moment.
	empty := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		n, err := windowCount(pid)
		if err != nil || n == 0 {
			if empty++; empty >= 2 {
				f.Gone()
				return
			}
			continue
		}
		empty = 0
	}
}

type process struct {
	pid  int
	name string
}

// Snapshot is the applications that have windows now, taken before a launch
// so that the one it opens can be told from them.
func Snapshot() (map[int]bool, error) {
	procs, err := guiProcesses()
	if err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(procs))
	for _, p := range procs {
		out[p.pid] = true
	}
	return out, nil
}

func matches(name string, names []string) bool {
	for _, n := range names {
		if len(name) >= len(n) && name[:len(n)] == n {
			return true
		}
	}
	return false
}

// Simulators are what a mobile launch opens, by process name. The simulator
// is usually running already, from the last time, and so is not new.
var Simulators = []string{"Simulator", "qemu-system", "emulator"}
