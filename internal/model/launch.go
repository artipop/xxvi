package model

// Launch kinds: what a project turns out to be once somebody wants to run it,
// and so what the run screen puts beside its log. Closed, like screen kinds: a
// kind decides what the window does, and a kind nothing can show is a trap.
const (
	// LaunchWeb is a page served by a dev server, opened inside the window.
	LaunchWeb = "web"
	// LaunchBackend is a service somebody talks to over HTTP, run as it is or
	// in Docker.
	LaunchBackend = "backend"
	// LaunchDesktop opens a window of its own.
	LaunchDesktop = "desktop"
	// LaunchMobile opens a simulator or an emulator.
	LaunchMobile = "mobile"
	// LaunchCommand is a command and its output, nothing more.
	LaunchCommand = "command"
)

// LaunchKinds is every accepted kind, in the order the run screen offers them.
var LaunchKinds = []string{LaunchWeb, LaunchBackend, LaunchDesktop, LaunchMobile, LaunchCommand}

// IsLaunchKind reports a member of LaunchKinds.
func IsLaunchKind(kind string) bool {
	for _, k := range LaunchKinds {
		if k == kind {
			return true
		}
	}
	return false
}
