package app

import (
	"github.com/artipop/xxvi/internal/acp"
	"github.com/artipop/xxvi/internal/model"
)

func (a *App) terminalHistory(id string) []byte {
	run, err := a.Store.Session(id)
	if err != nil || run.Work != model.WorkTerminal || run.ACPSessionID == "" {
		return nil
	}
	agent, err := a.Store.Agent(run.AgentName)
	if err != nil {
		agent = model.Agent{Name: run.AgentName}
	}
	agent.Kind = run.AgentKind
	return acp.TerminalHistory(agent, run.ACPSessionID, run.FinishedAt)
}
