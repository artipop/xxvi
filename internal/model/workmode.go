package model

import "github.com/artipop/xxvi/internal/msg"

// How a card works in its project when the project is a git repository. Asked
// of the card, not of the project: the same repository takes a quick fix in
// place today and three features side by side tomorrow, and it is the task
// that knows which it is.
//
// Three answers, and the first is the one there always was: the agent works in
// the folder as it stands, on whatever is checked out. The other two give the
// card a branch of its own — they differ in where that branch is checked out.
const (
	// WorkModeFolder is the folder as it stands: no branch is made and nothing
	// is switched. The empty string, so every card from before this works on.
	WorkModeFolder = ""
	// WorkModeWorktree is a separate working tree per card, on the card's own
	// branch: several cards of one repository at once, and the person's own
	// checkout is left alone.
	WorkModeWorktree = "worktree"
	// WorkModeBranch is the card's branch checked out in the folder itself: one
	// card at a time, and the work shows up in the person's editor straight away.
	WorkModeBranch = "branch"
)

// WorkModes is every accepted mode, in the order a person is offered them.
var WorkModes = []string{WorkModeFolder, WorkModeWorktree, WorkModeBranch}

// ValidateWorkMode refuses a mode this application does not know.
func ValidateWorkMode(mode string) error {
	for _, m := range WorkModes {
		if m == mode {
			return nil
		}
	}
	return msg.Err("workMode.unknown", "mode", mode)
}
