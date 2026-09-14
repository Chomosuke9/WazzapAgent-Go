package effect

import (
	"context"
	"errors"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// RecoveryStore exposes only refs that are safe to re-enter. The dispatcher
// remains the state-machine owner: an expired executing durable effect becomes
// unknown, never a blind native retry.
type RecoveryStore interface {
	ListRecoverableEffects(context.Context, identity.TenantID, time.Time, uint32) ([]Ref, error)
}

type RecoveryWorker struct {
	tenantID  identity.TenantID
	store     RecoveryStore
	dispatch  *Dispatcher
	clock     agent.Clock
	interval  time.Duration
	batchSize uint32
}

func NewRecoveryWorker(
	tenantID identity.TenantID,
	store RecoveryStore,
	dispatch *Dispatcher,
	clock agent.Clock,
	interval time.Duration,
	batchSize uint32,
) (*RecoveryWorker, error) {
	if tenantID.IsZero() || store == nil || dispatch == nil || clock == nil || interval <= 0 || batchSize == 0 || batchSize > 10_000 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create effect recovery worker", errors.New("valid identity, dependencies, interval, and batch size are required"))
	}
	return &RecoveryWorker{tenantID: tenantID, store: store, dispatch: dispatch, clock: clock, interval: interval, batchSize: batchSize}, nil
}

func (worker *RecoveryWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(worker.interval)
	defer ticker.Stop()
	for {
		if err := worker.recover(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (worker *RecoveryWorker) recover(ctx context.Context) error {
	refs, err := worker.store.ListRecoverableEffects(ctx, worker.tenantID, worker.clock.Now(), worker.batchSize)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return nil
		}
		// Errors are durable on the effect row and must not starve unrelated
		// chats in this batch.
		_ = worker.dispatch.Dispatch(ctx, ref)
	}
	return nil
}
