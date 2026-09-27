package launch

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// System Events answers both questions — which applications have windows and
// where a window goes — and asks the person once for each: automation to be
// told the list, accessibility to move a window. A refusal of either is
// ErrNoAccess, and the run screen says where to grant it.

func osascript(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		// -1743: automation refused; -1719 and -25211: accessibility.
		for _, code := range []string{"-1743", "-1719", "-25211", "assistive access"} {
			if strings.Contains(text, code) {
				return "", ErrNoAccess
			}
		}
		return "", fmt.Errorf("osascript: %s", text)
	}
	return text, nil
}

func guiProcesses() ([]process, error) {
	out, err := osascript(`set out to ""
tell application "System Events"
	repeat with p in (every application process whose background only is false)
		set out to out & (unix id of p) & tab & (name of p) & linefeed
	end repeat
end tell
return out`)
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	var procs []process
	for _, line := range strings.Split(out, "\n") {
		id, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		pid, err := strconv.Atoi(id)
		if !ok || err != nil || pid == self {
			continue
		}
		procs = append(procs, process{pid: pid, name: name})
	}
	return procs, nil
}

func windowCount(pid int) (int, error) {
	out, err := osascript(fmt.Sprintf(
		`tell application "System Events" to count windows of (first application process whose unix id is %d)`, pid))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// place moves the front window of pid into area. Without resize it keeps its
// size and is centred there, which is what a simulator needs: its window is
// the device's shape and stretching it only adds bezel.
func place(pid int, area Rect, resize bool) error {
	_, err := osascript(fmt.Sprintf(`tell application "System Events" to tell (first application process whose unix id is %d)
	set w to window 1
	if %t then
		set position of w to {%d, %d}
		set size of w to {%d, %d}
	else
		set {ww, wh} to size of w
		set x to %d + (%d - ww) div 2
		set y to %d + (%d - wh) div 2
		if x < %d then set x to %d
		if y < %d then set y to %d
		set position of w to {x, y}
	end if
	set frontmost to true
end tell`, pid, resize, area.X, area.Y, area.W, area.H,
		area.X, area.W, area.Y, area.H, area.X, area.X, area.Y, area.Y))
	return err
}
