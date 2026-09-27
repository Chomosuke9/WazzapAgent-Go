package agent

import (
	"context"
	"fmt"
	"sync"
)

type Factory interface {
	NewAgent(context.Context, Key) (*Agent, error)
}

type FactoryFunc func(context.Context, Key) (*Agent, error)

func (function FactoryFunc) NewAgent(ctx context.Context, key Key) (*Agent, error) {
	return function(ctx, key)
}

// Registry hands out the one Agent for each chat. An Agent holds only shared
// dependencies and its chat's operation gate, so it is kept for the life of
// the process: evicting it could let two gates exist for one chat.
type Registry struct {
	factory Factory

	mu     sync.Mutex
	agents map[Key]*Agent
}

func NewRegistry(factory Factory) (*Registry, error) {
	if factory == nil {
		return nil, NewError(ErrorInvalidArgument, "create agent registry", fmt.Errorf("factory is required"))
	}
	return &Registry{factory: factory, agents: make(map[Key]*Agent)}, nil
}

// AgentFor returns the chat's Agent, creating it on first use. Creation runs
// outside the lock so one slow config load does not stall other chats. If two
// callers race on a new chat, both build one and the first stored wins; the
// loser is dropped before anyone uses it.
func (registry *Registry) AgentFor(ctx context.Context, key Key) (*Agent, error) {
	if err := key.Validate(); err != nil {
		return nil, NewError(ErrorInvalidArgument, "get agent", err)
	}
	registry.mu.Lock()
	existing, exists := registry.agents[key]
	registry.mu.Unlock()
	if exists {
		return existing, nil
	}
	agent, err := registry.factory.NewAgent(ctx, key)
	if err != nil {
		return nil, err
	}
	if agent == nil || agent.key != key {
		return nil, Errorf(ErrorIntegrityFailure, "construct agent", "factory returned no agent or the wrong chat's agent")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing, exists := registry.agents[key]; exists {
		return existing, nil
	}
	registry.agents[key] = agent
	return agent, nil
}
