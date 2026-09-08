package inbound

import (
	"context"
	"fmt"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type RecoveryStore interface {
	ListRecoverableInbound(context.Context, identity.TenantID, time.Time, time.Time, uint32) ([]conversation.IncomingMessage, error)
}

type MessageResumer interface {
	Resume(context.Context, conversation.IncomingMessage) error
}

type RecoveryWorker struct {
	tenantID  identity.TenantID
	store     RecoveryStore
	resumer   MessageResumer
	clock     agent.Clock
	interval  time.Duration
	grace     time.Duration
	batchSize uint32
}

func NewRecoveryWorker(
	tenantID identity.TenantID,
	store RecoveryStore,
	resumer MessageResumer,
	clock agent.Clock,
	interval time.Duration,
	grace time.Duration,
	batchSize uint32,
) (*RecoveryWorker, error) {
	if tenantID.IsZero() || store == nil || resumer == nil || clock == nil || interval <= 0 || grace <= 0 || batchSize == 0 || batchSize > 10_000 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create inbound recovery worker", fmt.Errorf("valid identity, dependencies, timing, and batch size are required"))
	}
	return &RecoveryWorker{tenantID: tenantID, store: store, resumer: resumer, clock: clock, interval: interval, grace: grace, batchSize: batchSize}, nil
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
	now := worker.clock.Now()
	messages, err := worker.store.ListRecoverableInbound(ctx, worker.tenantID, now, now.Add(-worker.grace), worker.batchSize)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if ctx.Err() != nil {
			return nil
		}
		// Per-message errors remain represented by their durable state or lease.
		// They must not prevent recovery of unrelated chats.
		_ = worker.resumer.Resume(ctx, message)
	}
	return nil
}
