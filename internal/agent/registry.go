package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Factory interface {
	NewAgent(context.Context, Key) (*Agent, error)
}

type FactoryFunc func(context.Context, Key) (*Agent, error)

func (function FactoryFunc) NewAgent(ctx context.Context, key Key) (*Agent, error) {
	return function(ctx, key)
}

type RegistryLimits struct {
	MaxLive             uint32
	IdleTTL             time.Duration
	ConstructionTimeout time.Duration
}

type Registry struct {
	factory Factory
	limits  RegistryLimits
	clock   Clock
	ctx     context.Context
	cancel  context.CancelFunc

	mu      sync.Mutex
	entries map[Key]*registryEntry
	closed  bool
	builds  sync.WaitGroup
}

type registryEntry struct {
	agent        *Agent
	ready        chan struct{}
	err          error
	lastAccessed time.Time
	hint         ConfigVersion
}

func NewRegistry(parent context.Context, factory Factory, limits RegistryLimits) (*Registry, error) {
	return newRegistry(parent, factory, limits, SystemClock{})
}

func newRegistry(parent context.Context, factory Factory, limits RegistryLimits, clock Clock) (*Registry, error) {
	if parent == nil || factory == nil || clock == nil {
		return nil, NewError(ErrorInvalidArgument, "create agent registry", fmt.Errorf("context, factory, and clock are required"))
	}
	if limits.MaxLive == 0 || limits.IdleTTL <= 0 || limits.ConstructionTimeout <= 0 {
		return nil, NewError(ErrorInvalidArgument, "create agent registry", fmt.Errorf("positive registry limits are required"))
	}
	ctx, cancel := context.WithCancel(parent)
	return &Registry{
		factory: factory,
		limits:  limits,
		clock:   clock,
		ctx:     ctx,
		cancel:  cancel,
		entries: make(map[Key]*registryEntry),
	}, nil
}

func (registry *Registry) AgentFor(ctx context.Context, key Key) (*Agent, error) {
	if err := key.Validate(); err != nil {
		return nil, NewError(ErrorInvalidArgument, "get agent", err)
	}
	for {
		registry.mu.Lock()
		if registry.closed {
			registry.mu.Unlock()
			return nil, NewError(ErrorNotReady, "get agent", fmt.Errorf("registry is closed"))
		}
		now := registry.clock.Now()
		registry.evictIdleLocked(now)
		if entry, exists := registry.entries[key]; exists {
			if entry.agent != nil {
				entry.lastAccessed = now
				agent := entry.agent
				registry.mu.Unlock()
				return agent, nil
			}
			ready := entry.ready
			registry.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, contextError("wait for agent construction", ctx.Err())
			case <-ready:
				registry.mu.Lock()
				agent, buildErr := entry.agent, entry.err
				registry.mu.Unlock()
				if buildErr != nil {
					return nil, buildErr
				}
				if agent == nil {
					return nil, NewError(ErrorIntegrityFailure, "get agent", fmt.Errorf("construction completed without result"))
				}
				return agent, nil
			}
		}
		if uint32(len(registry.entries)) >= registry.limits.MaxLive {
			registry.mu.Unlock()
			return nil, NewError(ErrorResourceExhausted, "get agent", fmt.Errorf("active agent limit reached"))
		}
		entry := &registryEntry{ready: make(chan struct{}), lastAccessed: now}
		registry.entries[key] = entry
		registry.builds.Add(1)
		go registry.construct(key, entry)
		ready := entry.ready
		registry.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, contextError("wait for agent construction", ctx.Err())
		case <-ready:
			registry.mu.Lock()
			agent, buildErr := entry.agent, entry.err
			registry.mu.Unlock()
			if buildErr != nil {
				return nil, buildErr
			}
			if agent == nil {
				return nil, NewError(ErrorIntegrityFailure, "get agent", fmt.Errorf("construction completed without result"))
			}
			return agent, nil
		}
	}
}

func (registry *Registry) NotifyConfigChanged(event ConfigChanged) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry, exists := registry.entries[event.Key]
	if !exists || entry.agent == nil {
		return
	}
	if event.Current > entry.hint {
		entry.hint = event.Current
		entry.agent.config.markStale(event.Current)
	}
}

func (registry *Registry) Close(ctx context.Context) error {
	registry.mu.Lock()
	if !registry.closed {
		registry.closed = true
		registry.cancel()
	}
	registry.mu.Unlock()

	done := make(chan struct{})
	go func() {
		registry.builds.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return contextError("close agent registry", ctx.Err())
	case <-done:
		return nil
	}
}

func (registry *Registry) construct(key Key, entry *registryEntry) {
	defer registry.builds.Done()
	ctx, cancel := context.WithTimeout(registry.ctx, registry.limits.ConstructionTimeout)
	defer cancel()
	agent, err := registry.factory.NewAgent(ctx, key)
	if err == nil && agent == nil {
		err = Errorf(ErrorIntegrityFailure, "construct agent", "factory returned nil agent")
	}
	if err == nil && agent.key != key {
		err = Errorf(ErrorIntegrityFailure, "construct agent", "factory returned wrong agent key")
		agent = nil
	}

	registry.mu.Lock()
	current, exists := registry.entries[key]
	if exists && current == entry {
		entry.agent = agent
		entry.err = err
		entry.lastAccessed = registry.clock.Now()
		if err != nil {
			delete(registry.entries, key)
		}
		close(entry.ready)
	}
	registry.mu.Unlock()
}

func (registry *Registry) evictIdleLocked(now time.Time) {
	for key, entry := range registry.entries {
		if entry.agent == nil || entry.agent.isInFlight() {
			continue
		}
		if now.Sub(entry.lastAccessed) >= registry.limits.IdleTTL {
			delete(registry.entries, key)
		}
	}
}
