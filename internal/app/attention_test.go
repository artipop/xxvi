package app

import (
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/acp"
)

func TestRemindersThinOutTheLongerATaskWaits(t *testing.T) {
	for _, c := range []struct {
		age  time.Duration
		want int
	}{
		{10 * time.Minute, 0},
		{15 * time.Minute, 1},
		{59 * time.Minute, 1},
		{time.Hour, 2},
		{4 * time.Hour, 3},
		{24 * time.Hour, 4},
		{47 * time.Hour, 4},
		{48 * time.Hour, 5},
	} {
		if got := remindLevel(c.age); got != c.want {
			t.Errorf("через %v напоминаний должно быть %d, получено %d", c.age, c.want, got)
		}
	}
}

func TestEachReminderIsSaidOnce(t *testing.T) {
	var r reminders
	start := time.Now()
	row := acp.Attention{Key: "s:1", CardID: "1", Since: start}

	if got := r.due([]acp.Attention{row}, start.Add(5*time.Minute)); len(got) != 0 {
		t.Fatal("рано напоминать")
	}
	if got := r.due([]acp.Attention{row}, start.Add(16*time.Minute)); len(got) != 1 {
		t.Fatal("через четверть часа пора напомнить")
	}
	if got := r.due([]acp.Attention{row}, start.Add(17*time.Minute)); len(got) != 0 {
		t.Fatal("то же напоминание второй раз не приходит")
	}
	if got := r.due([]acp.Attention{row}, start.Add(61*time.Minute)); len(got) != 1 {
		t.Fatal("через час — следующее")
	}
}

func TestAnAnsweredRowStartsOverWhenItComesBack(t *testing.T) {
	var r reminders
	start := time.Now()
	row := acp.Attention{Key: "s:1", CardID: "1", Since: start}
	r.due([]acp.Attention{row}, start.Add(16*time.Minute))

	r.due(nil, start.Add(20*time.Minute)) // answered: the row is gone

	back := acp.Attention{Key: "s:1", CardID: "1", Since: start.Add(30 * time.Minute)}
	if got := r.due([]acp.Attention{back}, start.Add(46*time.Minute)); len(got) != 1 {
		t.Fatal("вернувшаяся строка — новое ожидание, и напоминание о нём своё")
	}
}

func TestALeftoverWorktreeIsNotReminded(t *testing.T) {
	var r reminders
	start := time.Now()
	row := acp.Attention{Key: "w:1", CardID: "1", Worktree: "/tmp/t", Since: start}
	if got := r.due([]acp.Attention{row}, start.Add(48*time.Hour)); len(got) != 0 {
		t.Fatal("дерево закрытой задачи работу не держит — о нём не напоминают")
	}
}
