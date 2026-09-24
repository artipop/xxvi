package hosting

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// Reporter is where a hosting stage says how it ended: the engine.
type Reporter interface {
	StepDone(job engine.Job, outcome string, detail msg.Msg)
}

// SetReporter supplies the engine once both exist.
func (s *Service) SetReporter(r Reporter) { s.reporter = r }

// Publish works one hosting stage in the background (engine.Publisher).
func (s *Service) Publish(job engine.Job) {
	go func() {
		outcome, detail := model.TriggerSuccess, msg.Msg{}
		var err error
		switch job.Stage.Action {
		case model.ActionPublish:
			detail, err = s.publish(job.Card.ID)
		case model.ActionVerdict:
			detail, err = s.verdict(job.Card.ID)
		}
		if err != nil {
			s.log.Warn("hosting step failed", "card", job.Card.ID, "stage", job.Stage.Name, "err", err)
			s.note(job.Card.ID, model.EntryProblem,
				msg.New("journal.hostingFailed", "stage", job.Stage.Name).Because(err))
			outcome, detail = model.TriggerFailure, msg.New("outcome.hostingFailed")
		}
		if s.reporter != nil {
			s.reporter.StepDone(job, outcome, detail)
		}
	}()
}

// publish pushes the card's branch and opens its MR, or brings the open one up
// to date. Everything it needs is known exactly — the branch, where it lands,
// the title — which is why this is code and not an agent asked to do it.
func (s *Service) publish(cardID string) (msg.Msg, error) {
	card, err := s.store.Card(cardID)
	if err != nil {
		return msg.Msg{}, err
	}
	project, err := s.cardProject(card)
	if err != nil {
		return msg.Msg{}, err
	}
	if card.WorkMode == model.WorkModeReview {
		return msg.Msg{}, msg.Err("publish.foreignBranch", "branch", card.Branch)
	}
	if card.Branch == "" {
		// A card working in the folder as it stands has no branch of its own,
		// and publishing whatever the folder is on would publish somebody
		// else's work under this card's name.
		return msg.Msg{}, msg.Err("publish.noBranch")
	}
	dir := card.Worktree
	if dir == "" {
		dir = project.Path
	}
	if head, _ := git(dir, "rev-parse", "--abbrev-ref", "HEAD"); head != card.Branch {
		return msg.Msg{}, msg.Err("publish.notOnBranch", "branch", card.Branch, "head", head)
	}
	// What would be pushed is only what is committed, and what was reviewed
	// was the working tree: pushing the commits and leaving the rest behind
	// publishes something nobody looked at.
	if status, err := git(dir, "status", "--porcelain"); err != nil {
		return msg.Msg{}, err
	} else if status != "" {
		return msg.Msg{}, msg.Err("publish.dirty", "files", dirtyFiles(status))
	}
	target := strings.TrimPrefix(card.Base, "origin/")
	if target == "" || target == "HEAD" {
		return msg.Msg{}, msg.Err("publish.noBase", "branch", card.Branch)
	}
	if n, err := git(dir, "rev-list", "--count", card.Base+".."+card.Branch); err == nil && n == "0" {
		return msg.Msg{}, msg.Err("publish.nothing", "branch", card.Branch, "base", target)
	}

	remote, provider, err := s.For(project)
	if err != nil {
		return msg.Msg{}, err
	}
	if _, err := git(dir, "push", "-u", "origin", card.Branch); err != nil {
		return msg.Msg{}, msg.Wrap(err, "publish.pushFailed", "branch", card.Branch)
	}

	ctx := context.Background()
	body := s.description(card)
	mr, found, err := provider.OpenMR(ctx, remote.Path, card.Branch)
	if err != nil {
		return msg.Msg{}, err
	}
	what := "journal.mrUpdated"
	if found {
		if err := provider.UpdateMRBody(ctx, remote.Path, mr.IID, body); err != nil {
			return msg.Msg{}, err
		}
	} else {
		mr, err = provider.CreateMR(ctx, remote.Path, NewMR{
			Source: card.Branch, Target: target, Title: card.Title, Body: body,
		})
		if err != nil {
			return msg.Msg{}, err
		}
		what = "journal.mrOpened"
	}
	if _, err := s.store.UpdateCard(card.ID, store.CardEdit{Props: map[string]string{model.MRProperty: mr.URL}}); err != nil {
		return msg.Msg{}, err
	}
	s.note(card.ID, model.EntryProps, msg.New(what, "mr", "!"+strconv.Itoa(mr.IID), "url", mr.URL, "branch", card.Branch, "target", target))
	return msg.New("outcome.published", "mr", "!"+strconv.Itoa(mr.IID)), nil
}

// description is the card's own text and what the stages said they did: the
// task, and its report. Written by the application but in nobody's language —
// every word in it is the card's or an agent's.
func (s *Service) description(card model.Card) string {
	parts := []string{strings.TrimSpace(card.Body)}
	if journal, err := s.store.Journal(card.ID); err == nil {
		for _, e := range journal {
			if e.Kind == model.EntryReport && strings.TrimSpace(e.Text) != "" {
				parts = append(parts, strings.TrimSpace(e.Text))
			}
		}
	}
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n---\n\n")
}

// verdict sends a person's review to somebody else's MR: an approval pinned to
// the commit they looked at, or the remarks they wrote. Which one is the
// person's last mark on the card — the outcome field their button set.
func (s *Service) verdict(cardID string) (msg.Msg, error) {
	card, err := s.store.Card(cardID)
	if err != nil {
		return msg.Msg{}, err
	}
	project, err := s.cardProject(card)
	if err != nil {
		return msg.Msg{}, err
	}
	iid, ok := MRNumber(card.Prop(model.MRProperty))
	if !ok {
		return msg.Msg{}, msg.Err("verdict.noMR")
	}
	remote, provider, err := s.For(project)
	if err != nil {
		return msg.Msg{}, err
	}
	ctx := context.Background()
	number := "!" + strconv.Itoa(iid)
	switch card.Prop(model.OutcomeProperty) {
	case model.OutcomePassed:
		// The head the person reviewed is the one the card last brought in:
		// the source updates it with every new commit, and so the diff.
		if err := provider.Approve(ctx, remote.Path, iid, card.ItemVersion); err != nil {
			return msg.Msg{}, err
		}
		s.setReview(card.ID, model.ReviewApproved)
		s.note(card.ID, model.EntryProps, msg.New("journal.mrApproved", "mr", number))
		return msg.New("outcome.approved", "mr", number), nil
	case model.OutcomeFailed:
		if err := provider.RequestChanges(ctx, remote.Path, iid, card.Prop(model.RemarksProperty)); err != nil {
			return msg.Msg{}, err
		}
		s.setReview(card.ID, model.ReviewChanges)
		s.note(card.ID, model.EntryProps, msg.New("journal.mrChangesRequested", "mr", number))
		return msg.New("outcome.changesRequested", "mr", number), nil
	}
	return msg.Msg{}, msg.Err("verdict.noOutcome")
}

func (s *Service) setReview(cardID, value string) {
	if _, err := s.store.UpdateCard(cardID, store.CardEdit{Props: map[string]string{model.ReviewProperty: value}}); err != nil {
		s.log.Warn("could not write the review on the card", "card", cardID, "err", err)
	}
}

func (s *Service) cardProject(card model.Card) (model.Project, error) {
	if card.Project == "" {
		return model.Project{}, msg.Err("hosting.noProject")
	}
	return s.store.Project(card.Project)
}

func (s *Service) note(cardID string, kind model.EntryKind, what msg.Msg) {
	if _, err := s.store.Record(model.JournalEntry{CardID: cardID, Kind: kind, Msg: &what}); err != nil {
		s.log.Warn("could not write the card journal", "card", cardID, "err", err)
	}
}

var mrNumber = regexp.MustCompile(`/-/merge_requests/(\d+)`)

// MRNumber is the IID in an MR's address. The address is what the card keeps,
// because it is also what a person and a browser screen open.
func MRNumber(url string) (int, bool) {
	m := mrNumber.FindStringSubmatch(url)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// dirtyFiles names what is uncommitted, a few at most: the sentence has to fit
// on a plaque.
func dirtyFiles(status string) string {
	var files []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	if len(files) > 5 {
		files = append(files[:5], "…")
	}
	return strings.Join(files, ", ")
}
