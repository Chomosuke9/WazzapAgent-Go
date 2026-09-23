package app

import (
	"context"
	"errors"
)

func (runtime *conversationRuntime) run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runners := []func(context.Context) error{
		runtime.account.Run,
		runtime.recovery.Run,
		runtime.effectRecovery.Run,
		runtime.inboundRecovery.Run,
		runtime.inboundDispatch.Run,
		runtime.maintenance.Run,
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
	if runtime.registry != nil {
		if err := runtime.registry.Close(ctx); err != nil {
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
