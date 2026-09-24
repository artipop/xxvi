package hosting

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// The hosting is asked, not listened to: a webhook needs an address the
// server can reach, and a laptop behind a NAT has none. So once a minute the
// application asks — and only about what somebody is waiting for.

// pollEvery is how often the hosting is asked. A merge noticed a minute late
// costs nothing; a server asked every few seconds by every laptop on the team
// is a server somebody has to answer for.
const pollEvery = time.Minute

// Start begins asking the hosting on a timer.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	ctx, stop := context.WithCancel(context.Background())
	s.stop = stop
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Not at once: starting is opening the database and the terminals,
		// and none of that should queue behind a request to another server.
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.Poll()
				timer.Reset(pollEvery)
			}
		}
	}()
}

// Stop ends the timer and waits for any poll in progress, the timer's or one
// started by switching a review queue on.
func (s *Service) Stop() {
	s.mu.Lock()
	stop := s.stop
	s.stop = nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	s.wg.Wait()
}

// Poll asks the hosting everything it is asked on the timer: the review
// queues, then the state of the MRs somebody waits on.
func (s *Service) Poll() {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	s.pollReviews()
	s.pollWaiting()
}

// pollWaiting asks about the MR of every card whose stage waits for the MR to
// be merged or closed. The rest are not asked about: a card in review does
// not need to know, and every question is a request to somebody's server.
func (s *Service) pollWaiting() {
	cards, err := s.store.CardsInState(model.StateFlow)
	if err != nil {
		s.log.Warn("could not read the cards in work", "err", err)
		return
	}
	flows := map[string]model.Flow{}
	for _, card := range cards {
		iid, ok := MRNumber(card.Prop(model.MRProperty))
		if !ok || card.Project == "" {
			continue
		}
		st, onFlow, err := s.store.FlowState(card.ID)
		if err != nil || !onFlow {
			continue
		}
		flow, known := flows[st.FlowID]
		if !known {
			if flow, err = s.store.Flow(st.FlowID); err != nil {
				continue
			}
			flows[st.FlowID] = flow
		}
		if !flow.HasEdge(st.StageID, model.TriggerMRMerged) && !flow.HasEdge(st.StageID, model.TriggerMRClosed) {
			continue
		}
		project, err := s.store.Project(card.Project)
		if err != nil {
			continue
		}
		remote, provider, err := s.For(project)
		if err != nil {
			continue
		}
		mr, err := provider.MR(context.Background(), remote.Path, iid)
		if err != nil {
			s.log.Warn("could not ask the hosting about an MR", "card", card.ID, "err", err)
			continue
		}
		s.stateChanged(card.ID, mr)
	}
}

// stateChanged fires the event for an MR that is no longer open.
func (s *Service) stateChanged(cardID string, mr MR) {
	number := "!" + strconv.Itoa(mr.IID)
	switch mr.State {
	case StateMerged:
		s.fire(cardID, model.TriggerMRMerged, msg.New("move.mrMerged", "mr", number))
	case StateClosed:
		s.fire(cardID, model.TriggerMRClosed, msg.New("move.mrClosed", "mr", number))
	}
}

func (s *Service) fire(cardID, trigger string, detail msg.Msg) bool {
	if s.reporter == nil {
		return false
	}
	return s.reporter.HostingEvent(cardID, trigger, detail)
}

// pollState is the timer's own bookkeeping.
type pollState struct {
	mu     sync.Mutex
	stop   context.CancelFunc
	wg     sync.WaitGroup
	pollMu sync.Mutex
}
