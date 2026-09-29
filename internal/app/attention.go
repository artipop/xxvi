package app

import (
	"sort"
	"sync"
	"time"

	"github.com/artipop/xxvi/internal/acp"
)

// Attention is everything waiting for a person, oldest first. The agents know
// what their runs are stuck on; the engine knows which cards stand where only
// a person moves them on. One list, so that a task left on a review or after
// a failed step is in the same place as an agent's question — a card that
// waits for a person is the same fact whoever noticed it (docs/system.md §7).
func (a *App) Attention() []acp.Attention {
	out := a.Agents.Attention()
	standing, err := a.Engine.Standing()
	if err != nil {
		a.log.Warn("could not read which cards stand", "err", err)
	}
	for _, s := range standing {
		out = append(out, acp.Attention{
			Key: "s:" + s.CardID, CardID: s.CardID, CardTitle: s.CardTitle,
			Awaiting: true, Since: s.Since,
			Standing: s.Why, Stage: s.Stage, Problem: s.Problem,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// A task is finished in rounds — the agent, the review, the remarks, the agent
// again — and between any two of them it waits for a person who may have gone
// on to something else. The first notification is easy to miss and the panel
// is only seen by somebody already looking, so what has waited long enough is
// said again, less often the longer it waits: a reminder every quarter of an
// hour is noise nobody reads, one a day is still a reminder.

// remindAfter is how long a row waits before each reminder; past the last,
// one more every remindEvery.
var remindAfter = []time.Duration{15 * time.Minute, time.Hour, 4 * time.Hour, 24 * time.Hour}

const remindEvery = 24 * time.Hour

// remindLevel is how many reminders a row of this age has earned.
func remindLevel(age time.Duration) int {
	n := 0
	for _, d := range remindAfter {
		if age < d {
			return n
		}
		n++
	}
	last := remindAfter[len(remindAfter)-1]
	return n + int((age-last)/remindEvery)
}

// reminders is which level each row was last reminded at. In memory: after a
// restart everything overdue is reminded once, together, which is what a
// person coming back to the machine wants to hear anyway.
type reminders struct {
	mu   sync.Mutex
	sent map[string]int
	stop chan struct{}
}

// due is the rows that have earned a reminder they have not had, and forgets
// the rows that are gone. The row's own notification counts as nothing: it
// said something happened, a reminder says it is still so.
func (r *reminders) due(rows []acp.Attention, now time.Time) []acp.Attention {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[string]int{}
	}
	seen := make(map[string]bool, len(rows))
	var out []acp.Attention
	for _, row := range rows {
		if !remindable(row) {
			continue
		}
		seen[row.Key] = true
		level := remindLevel(now.Sub(row.Since))
		if level > r.sent[row.Key] {
			r.sent[row.Key] = level
			out = append(out, row)
		}
	}
	for key := range r.sent {
		if !seen[key] {
			delete(r.sent, key)
		}
	}
	return out
}

// remindable is a row that holds a task up. A closed card's working tree does
// not: the task is over, and the tree costs disk, not progress.
func remindable(row acp.Attention) bool {
	return row.Worktree == ""
}

// startReminders looks over what is waiting once a minute until Close.
func (a *App) startReminders() {
	a.remind.stop = make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-a.remind.stop:
				return
			case now := <-tick.C:
				a.remindDue(now)
			}
		}
	}()
}

func (a *App) stopReminders() {
	if a.remind.stop != nil {
		close(a.remind.stop)
		a.remind.stop = nil
	}
}

func (a *App) remindDue(now time.Time) {
	a.uiMu.RLock()
	n := a.notifier
	a.uiMu.RUnlock()
	if n == nil || !n.ok {
		return
	}
	if due := a.remind.due(a.Attention(), now); len(due) > 0 {
		n.Remind(due)
	}
}
