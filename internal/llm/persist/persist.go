// Package persist keeps asking the model for invocations that must not give
// up on a passing failure. A request that was accepted, such as a direct
// invoke, has no user waiting on a reply to fall back on, so a rate limit,
// timeout, or outage is waited out instead of dropped.
package persist

import (
	"context"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	// DefaultMaxWait is how long an invocation keeps retrying, counted from
	// when it was registered.
	DefaultMaxWait   = 10 * time.Minute
	defaultFirstWait = 2 * time.Second
	defaultMaxStep   = 30 * time.Second
)

// Invoker passes every call to the model unchanged, except for registered
// invocations: those are retried with a growing delay until the call
// succeeds, fails in a way waiting cannot fix, or MaxWait has passed.
type Invoker struct {
	next agent.ModelInvoker

	// Tunables; tests lower them.
	MaxWait   time.Duration
	FirstWait time.Duration
	MaxStep   time.Duration
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error

	mu         sync.Mutex
	registered map[identity.InvocationID]time.Time
}

func New(next agent.ModelInvoker) *Invoker {
	return &Invoker{
		next: next, MaxWait: DefaultMaxWait, FirstWait: defaultFirstWait, MaxStep: defaultMaxStep,
		Now: time.Now, Sleep: sleep, registered: make(map[identity.InvocationID]time.Time),
	}
}

// Persist marks the invocation as one to retry. The returned function
// unmarks it and must be called once the invocation is over.
func (invoker *Invoker) Persist(id identity.InvocationID) (release func()) {
	invoker.mu.Lock()
	invoker.registered[id] = invoker.Now()
	invoker.mu.Unlock()
	return func() {
		invoker.mu.Lock()
		delete(invoker.registered, id)
		invoker.mu.Unlock()
	}
}

func (invoker *Invoker) Generate(ctx context.Context, request agent.ModelRequest) (agent.ModelResult, error) {
	invoker.mu.Lock()
	since, persistent := invoker.registered[request.InvocationID]
	invoker.mu.Unlock()
	if !persistent {
		return invoker.next.Generate(ctx, request)
	}
	deadline := since.Add(invoker.MaxWait)
	wait := invoker.FirstWait
	for {
		result, err := invoker.next.Generate(ctx, request)
		if err == nil || ctx.Err() != nil || !transient(agent.CodeOf(err)) {
			return result, err
		}
		if !invoker.Now().Add(wait).Before(deadline) {
			return result, err
		}
		if sleepErr := invoker.Sleep(ctx, wait); sleepErr != nil {
			return result, err
		}
		wait = min(wait*2, invoker.MaxStep)
	}
}

// transient lists the failures that can pass by themselves.
func transient(code agent.ErrorCode) bool {
	switch code {
	case agent.ErrorRateLimited, agent.ErrorTimeout, agent.ErrorUnavailable, agent.ErrorProviderFailure, agent.ErrorInternal:
		return true
	default:
		return false
	}
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
