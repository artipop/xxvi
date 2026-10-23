package engine

import (
	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
)

// StartStage is tied to a visit so a delayed click cannot start the next node.
func (e *Engine) StartStage(cardID string, visit int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, onFlow, err := e.store.FlowState(cardID)
	if err != nil {
		return err
	}
	last, ok, err := e.store.LastFlowEvent(cardID)
	if err != nil {
		return err
	}
	if !onFlow || !ok || last.ID != visit || last.Started {
		return msg.Err("stage.notReady")
	}
	flow, err := e.store.Flow(state.FlowID)
	if err != nil {
		return err
	}
	stage, ok := flow.Stage(state.StageID)
	if !ok || stage.Final || !model.Runs(stage.Action) {
		return msg.Err("stage.notReady")
	}
	card, err := e.store.Card(cardID)
	if err != nil {
		return err
	}
	e.record(cardID, model.EntryMove, msg.New("journal.startedByHand"))
	e.runStage(card, flow, stage)
	e.emitCard(cardID)
	return nil
}

// Revisit creates a new visit with the selected conversation. Past results stay
// in their visits; a new report follows the graph from this stage again.
func (e *Engine) Revisit(cardID, sessionID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	past, err := e.store.Session(sessionID)
	if err != nil {
		return err
	}
	if past.CardID != cardID || past.Work != model.WorkTerminal || !past.Status.Terminal() || past.ACPSessionID == "" {
		return msg.Err("stage.cannotRevisit")
	}
	card, err := e.store.Card(cardID)
	if err != nil {
		return err
	}
	last, ok, err := e.store.LastFlowEvent(cardID)
	if err != nil {
		return err
	}
	if !ok || last.FlowID != past.FlowID || card.State == model.StateDropped {
		return msg.Err("stage.cannotRevisit")
	}
	flow, err := e.store.Flow(past.FlowID)
	if err != nil {
		return err
	}
	stage, ok := flow.Stage(past.StageID)
	if !ok || stage.Action != model.ActionAgent || stage.Work == model.WorkSession || stage.Final {
		return msg.Err("stage.cannotRevisit")
	}
	agent, err := e.store.Agent(past.AgentName)
	if err != nil {
		return err
	}
	if agent.Kind != past.AgentKind {
		return msg.Err("stage.cannotRevisit")
	}
	if e.Publishing(cardID) {
		return msg.Err("stage.revisitBusy")
	}
	if stage.MaxRunning > 0 && e.runner != nil && e.runner.RunningOnStage(stage.ID) >= stage.MaxRunning {
		return msg.Err("stage.revisitBusy")
	}
	e.cancel(cardID, msg.New("cancel.movedByHand"))
	e.dequeue(cardID)
	// Enter without dispatching a task: reopening is an explicit, silent launch.
	stage.ManualStart = true
	e.enterStage(card, flow, stage, "", msg.New("move.revisited"))
	state, onFlow, err := e.store.FlowState(cardID)
	if err != nil {
		return err
	}
	if !onFlow || state.StageID != stage.ID {
		return msg.Err("stage.cannotRevisit")
	}
	card.Assignee = agent.Name
	e.runStageSaying(card, flow, stage, "", true, past.ACPSessionID)
	e.emitCard(cardID)
	return nil
}
