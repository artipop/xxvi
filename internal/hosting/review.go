package hosting

import (
	"context"
	"strconv"
	"strings"

	"github.com/artipop/xxvi/internal/inbox"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// The first real source (docs/system.md §8): the MRs of a project that wait on
// this account's review. An MR is an item — its id is its number, its version
// is its last commit — so everything after that is the path every source
// takes: a new MR is a card in the inbox, and new commits update the card in
// place.

// PluginReview is the source plugin that reads a project's review queue.
const PluginReview = "review"

// ConfigProject is the source config key naming the project, by id.
const ConfigProject = "project"

// ReviewFlow is the flow a review card is suggested, by name: the seeded one
// (app.HostingFlows). A suggestion — the person taking it into work chooses.
const ReviewFlow = "MR review"

// Ingester files what a source brought: the inbox pipeline.
type Ingester interface {
	Ingest(source string, items []model.Item) ([]inbox.Result, error)
}

// Trees moves a review card's working tree to the MR's new head.
type Trees interface {
	RefreshReviewTree(cardID string) error
}

// Notice is what the hosting side has to tell a person who may not be looking:
// a review is waiting, or an MR under review moved.
type Notice struct {
	Kind      string // NoticeReview | NoticeUpdated
	CardID    string
	CardTitle string
	MR        string
}

const (
	NoticeReview  = "review"
	NoticeUpdated = "updated"
)

// SetInbox supplies where review items go, what moves their trees, who tells
// a person about them, and how the screens hear the inbox changed.
func (s *Service) SetInbox(in Ingester, trees Trees, notify func(Notice), emit func(event string)) {
	s.ingester, s.trees, s.notify, s.emit = in, trees, notify, emit
}

// ReviewSource is the review source of a project, when it has one.
func (s *Service) ReviewSource(projectID string) (model.Source, bool) {
	sources, err := s.store.Sources()
	if err != nil {
		return model.Source{}, false
	}
	for _, src := range sources {
		if src.Plugin == PluginReview && src.Config[ConfigProject] == projectID {
			return src, true
		}
	}
	return model.Source{}, false
}

// SetReviewInbox turns a project's review queue into an inbox source, or
// switches it off. Off is disabled rather than deleted: the cards it brought
// keep their source, and switching it on again finds them.
func (s *Service) SetReviewInbox(p model.Project, on bool) error {
	src, exists := s.ReviewSource(p.ID)
	if !on {
		if !exists || !src.Enabled {
			return nil
		}
		src.Enabled = false
		_, err := s.store.SaveSource(src)
		return err
	}
	if _, _, err := s.For(p); err != nil {
		return err
	}
	if !exists {
		src = model.Source{
			Name: s.freeSourceName(p.Name), Plugin: PluginReview,
			Update: model.UpdateInPlace, IntervalSeconds: int(pollEvery.Seconds()),
			Config: map[string]string{ConfigProject: p.ID},
			Rules:  []model.Rule{{Name: "MR", Then: model.ActionCard, SuggestFlow: ReviewFlow}},
		}
	}
	src.Enabled = true
	if _, err := s.store.SaveSource(src); err != nil {
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.Poll()
	}()
	return nil
}

// freeSourceName is the project's name, or the project's name with a number
// when a source is already called that. The inbox words the group by the
// plugin, so the name only has to be unique.
func (s *Service) freeSourceName(name string) string {
	taken := map[string]bool{}
	if sources, err := s.store.Sources(); err == nil {
		for _, src := range sources {
			taken[strings.ToLower(src.Name)] = true
		}
	}
	candidate := name
	for n := 2; taken[strings.ToLower(candidate)]; n++ {
		candidate = name + " " + strconv.Itoa(n)
	}
	return candidate
}

// pollReviews reads every enabled review source.
func (s *Service) pollReviews() {
	if s.ingester == nil {
		return
	}
	sources, err := s.store.Sources()
	if err != nil {
		s.log.Warn("could not read sources", "err", err)
		return
	}
	for _, src := range sources {
		if src.Plugin != PluginReview || !src.Enabled {
			continue
		}
		if err := s.pollReview(src); err != nil {
			s.log.Warn("review queue not read", "source", src.Name, "err", err)
		}
	}
}

func (s *Service) pollReview(src model.Source) error {
	project, err := s.store.Project(src.Config[ConfigProject])
	if err != nil {
		return err
	}
	remote, provider, err := s.For(project)
	if err != nil {
		return err
	}
	account, _ := s.store.Setting(accountKey(remote.Web), "")
	if account == "" {
		return msg.Err("hosting.notConnected", "project", project.Name, "server", remote.Web)
	}
	ctx := context.Background()
	queue, err := provider.ReviewQueue(ctx, remote.Path, account)
	if err != nil {
		return err
	}
	items := make([]model.Item, 0, len(queue))
	open := map[string]bool{}
	for _, mr := range queue {
		// A draft is its author saying «not yet». It comes in when it is
		// marked ready, as a new version like any other.
		if mr.Draft {
			continue
		}
		item := reviewItem(project, mr)
		open[item.ExternalID] = true
		items = append(items, item)
	}
	results, err := s.ingester.Ingest(src.Name, items)
	if err != nil {
		return err
	}
	for _, r := range results {
		switch r.Outcome {
		case inbox.OutcomeCreated:
			s.tell(Notice{Kind: NoticeReview, CardID: r.CardID, MR: r.Item})
		case inbox.OutcomeUpdated:
			s.mrMoved(r.CardID, r.Item)
		}
	}
	s.forgetClosed(ctx, src, project, remote, provider, open)
	return nil
}

// mrMoved is an MR under review that got new commits: its tree goes to the
// new head, the stage that waits for it hears, and a person who is not
// looking is told.
func (s *Service) mrMoved(cardID, number string) {
	if s.trees != nil {
		if err := s.trees.RefreshReviewTree(cardID); err != nil {
			s.note(cardID, model.EntryProblem, msg.New("journal.reviewTreeNotMoved").Because(err))
		}
	}
	s.note(cardID, model.EntrySource, msg.New("journal.mrNewCommits", "mr", number))
	s.fire(cardID, model.TriggerMRUpdated, msg.New("move.mrUpdated", "mr", number))
	s.tell(Notice{Kind: NoticeUpdated, CardID: cardID, MR: number})
}

// forgetClosed drops the inbox cards of MRs that were merged or closed before
// anybody took them: there is nothing left to review. A card already in work
// is not touched here — its flow hears mr.merged and mr.closed itself — and an
// MR that only left this account's queue is still open, and stays.
func (s *Service) forgetClosed(ctx context.Context, src model.Source, project model.Project, remote Remote, provider Provider, open map[string]bool) {
	cards, err := s.store.CardsInState(model.StateInbox)
	if err != nil {
		return
	}
	for _, card := range cards {
		if card.Source != src.Name || open[card.ExternalID] {
			continue
		}
		iid, ok := MRNumber(card.Prop(model.MRProperty))
		if !ok {
			continue
		}
		mr, err := provider.MR(ctx, remote.Path, iid)
		if err != nil || mr.State == StateOpen {
			continue
		}
		state := model.StateDropped
		if _, err := s.store.UpdateCard(card.ID, store.CardEdit{State: &state}); err != nil {
			continue
		}
		code := "journal.mrMergedUnreviewed"
		if mr.State == StateClosed {
			code = "journal.mrClosedUnreviewed"
		}
		s.note(card.ID, model.EntrySource, msg.New(code, "mr", "!"+strconv.Itoa(iid)))
		if s.emit != nil {
			s.emit(inbox.EventInbox)
		}
	}
}

func (s *Service) tell(n Notice) {
	if s.notify == nil || n.CardID == "" {
		return
	}
	if card, err := s.store.Card(n.CardID); err == nil {
		n.CardTitle = card.Title
	}
	s.notify(n)
}

// reviewItem is an MR as the pipeline takes items. The id is the MR's number
// and the version its last commit: a new commit is what «this MR changed»
// means to somebody reviewing it, and a new description is not.
func reviewItem(project model.Project, mr MR) model.Item {
	number := "!" + strconv.Itoa(mr.IID)
	props := map[string]string{model.MRProperty: mr.URL}
	if mr.Author != "" {
		props["Author"] = mr.Author
	}
	return model.Item{
		ExternalID: number, Version: mr.Head,
		Title: mr.Title, Body: mr.Body, URL: mr.URL, At: mr.At,
		Props: props,
		Workspace: &model.ItemWorkspace{
			Project: project.ID, WorkMode: model.WorkModeReview,
			Branch: mr.Source, Base: "origin/" + mr.Target,
		},
	}
}
