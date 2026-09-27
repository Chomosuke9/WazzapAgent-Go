package app

import (
	"context"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

func (runtime *conversationRuntime) run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runners := []func(context.Context) error{
		runtime.account.Run,
		runtime.inboundDispatch.Run,
		runtime.maintenance.Run,
		runtime.redeliver,
	}
	errorsChannel := make(chan error, len(runners))
	for _, runner := range runners {
		runner := runner
		go func() { errorsChannel <- runner(runCtx) }()
	}
	joined := <-errorsChannel
	cancel()
	for index := 1; index < len(runners); index++ {
		joined = errors.Join(joined, <-errorsChannel)
	}
	return joined
}

// redeliver resumes the messages the last run left unanswered, then sends
// whatever the outbox holds each time the account connects. A send that
// cannot run while the account is offline stays pending until then.
func (runtime *conversationRuntime) redeliver(ctx context.Context) error {
	if err := runtime.inboundDispatch.Recover(ctx, runtime.tenantID); err != nil && ctx.Err() == nil {
		runtime.logger.Error("inbound recovery failed", "code", agent.CodeOf(err), "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-runtime.account.Opened():
			runtime.flushOutbox(ctx)
		}
	}
}

func (runtime *conversationRuntime) flushOutbox(ctx context.Context) {
	report := func(what string, err error) {
		if err != nil && ctx.Err() == nil && !agent.IsCode(err, agent.ErrorNotReady) {
			runtime.logger.Warn("outbox redelivery failed", "kind", what, "code", agent.CodeOf(err), "error", err)
		}
	}
	actions, err := runtime.store.Actions().ListPending(ctx, runtime.tenantID)
	report("list actions", err)
	for _, ref := range actions {
		_, err := runtime.dispatcher.Dispatch(ctx, ref)
		report("action", err)
	}
	// Effects go second: a model effect waits for the reply it follows.
	effects, err := runtime.store.Effects().ListPending(ctx, runtime.tenantID)
	report("list effects", err)
	for _, ref := range effects {
		report("effect", runtime.effectDispatcher.Dispatch(ctx, ref))
	}
}

// close is called only after run has joined every worker. It is retryable: a
// caller that times out while an adapter or registry drains can call Close
// again, and the store is not touched until those owners have finished.
func (runtime *conversationRuntime) close(ctx context.Context) error {
	runtime.closeMu.Lock()
	defer runtime.closeMu.Unlock()
	if runtime.closed {
		return runtime.closeErr
	}
	var joined error
	if runtime.adapter != nil {
		if err := runtime.adapter.Stop(ctx); err != nil {
			joined = errors.Join(joined, err)
			if cleanupTimedOut(ctx, err) {
				return joined
			}
		}
	}
	storeCloseFailed := false
	if runtime.store != nil {
		if err := runtime.store.Checkpoint(ctx); err != nil {
			joined = errors.Join(joined, err)
		}
		if err := runtime.store.Close(); err != nil {
			joined = errors.Join(joined, err)
			storeCloseFailed = true
		}
	}
	if runtime.langSmith != nil {
		langCtx := ctx
		cancel := func() {}
		if runtime.shutdownTimeout > 0 {
			langCtx, cancel = context.WithTimeout(ctx, runtime.shutdownTimeout)
		}
		if err := runtime.langSmith.Shutdown(langCtx); err != nil {
			joined = errors.Join(joined, err)
		}
		cancel()
	}
	if !storeCloseFailed {
		runtime.closed = true
		runtime.closeErr = joined
	}
	return joined
}

func cleanupTimedOut(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
