package maintenance

import (
	"context"
	"fmt"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type Request struct {
	TenantID          identity.TenantID
	Now               time.Time
	ScrubBefore       time.Time
	DeleteBefore      time.Time
	HistoryBefore     time.Time
	HistoryKeepLatest uint32
	BatchSize         uint32
}

type Result struct {
	InboundScrubbed int64
	ActionsScrubbed int64
	TurnsDeleted    int64
	HistoryRemoved  int64
}

type Store interface {
	Maintain(context.Context, Request) (Result, error)
}

type Worker struct {
	tenantID      identity.TenantID
	store         Store
	clock         agent.Clock
	interval      time.Duration
	scrubAge      time.Duration
	retainAge     time.Duration
	batchSize     uint32
	historyPolicy agent.RetentionPolicy
}

func NewWorker(
	tenantID identity.TenantID,
	store Store,
	clock agent.Clock,
	interval time.Duration,
	scrubAge time.Duration,
	retainAge time.Duration,
	batchSize uint32,
) (*Worker, error) {
	return NewWorkerWithHistory(tenantID, store, clock, interval, scrubAge, retainAge, batchSize, agent.RetentionPolicy{})
}

func NewWorkerWithHistory(
	tenantID identity.TenantID,
	store Store,
	clock agent.Clock,
	interval time.Duration,
	scrubAge time.Duration,
	retainAge time.Duration,
	batchSize uint32,
	historyPolicy agent.RetentionPolicy,
) (*Worker, error) {
	if tenantID.IsZero() || store == nil || clock == nil || interval <= 0 || scrubAge <= 0 ||
		retainAge <= scrubAge || batchSize == 0 || batchSize > 10_000 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create maintenance worker", fmt.Errorf("valid identity, dependencies, retention, and batch size are required"))
	}
	if (historyPolicy.KeepLatest == 0) != (historyPolicy.MaxAge == 0) || historyPolicy.MaxAge < 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create maintenance worker", fmt.Errorf("history retention requires both keep-latest and max-age"))
	}
	return &Worker{
		tenantID: tenantID, store: store, clock: clock, interval: interval,
		scrubAge: scrubAge, retainAge: retainAge, batchSize: batchSize,
		historyPolicy: historyPolicy,
	}, nil
}

func (worker *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(worker.interval)
	defer ticker.Stop()
	for {
		if _, err := worker.maintain(ctx); err != nil {
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

func (worker *Worker) maintain(ctx context.Context) (Result, error) {
	now := worker.clock.Now()
	request := Request{
		TenantID: worker.tenantID, Now: now, ScrubBefore: now.Add(-worker.scrubAge),
		DeleteBefore: now.Add(-worker.retainAge), BatchSize: worker.batchSize,
	}
	if worker.historyPolicy.MaxAge > 0 {
		request.HistoryBefore = now.Add(-worker.historyPolicy.MaxAge)
		request.HistoryKeepLatest = worker.historyPolicy.KeepLatest
	}
	return worker.store.Maintain(ctx, request)
}
