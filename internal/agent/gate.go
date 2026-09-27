package agent

import "context"

type operationGate struct {
	token chan struct{}
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
		return nil
	}
}

func (gate *operationGate) release() {
	gate.token <- struct{}{}
}
