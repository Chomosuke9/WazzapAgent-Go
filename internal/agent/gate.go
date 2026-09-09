package agent

import (
	"context"
	"sync/atomic"
)

type operationGate struct {
	token    chan struct{}
	inFlight atomic.Int64
}

func newOperationGate() *operationGate {
	gate := &operationGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

func (gate *operationGate) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return contextError("wait for agent operation gate", ctx.Err())
	case <-gate.token:
		gate.inFlight.Add(1)
		return nil
	}
}

func (gate *operationGate) release() {
	gate.inFlight.Add(-1)
	gate.token <- struct{}{}
}

func (gate *operationGate) isInFlight() bool { return gate.inFlight.Load() > 0 }
