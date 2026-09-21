package engine

// Observe atomically initializes a bounded state observer and installs its event
// callback. Both callbacks run under the state lock: they must only copy into
// bounded memory and must never call back into the Agent. Existing Subscribe
// listeners keep their ordering, reentrancy, and awaited-completion semantics.
type stateObserver struct {
	initialize func(AgentState)
	event      func(AgentEvent)
}

func (agent *Agent) resetObserversLocked() {
	for _, observer := range agent.observers {
		if observer.initialize != nil {
			observer.initialize(copyAgentState(agent.state))
		}
	}
}

func (agent *Agent) Observe(initialize func(AgentState), event func(AgentEvent)) func() {
	agent.mu.Lock()
	if initialize != nil {
		initialize(copyAgentState(agent.state))
	}
	agent.nextObserver++
	id := agent.nextObserver
	if agent.observers == nil {
		agent.observers = map[uint64]stateObserver{}
	}
	agent.observers[id] = stateObserver{initialize, event}
	agent.mu.Unlock()
	return func() { agent.mu.Lock(); delete(agent.observers, id); agent.mu.Unlock() }
}
