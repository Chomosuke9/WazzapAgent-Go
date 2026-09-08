package maintenance_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/maintenance"
)

func TestWorkerRunsImmediatelyAndStopsWithContext(t *testing.T) {
	tenantID, _ := identity.NewTenantID()
	store := &recordingStore{called: make(chan maintenance.Request, 1)}
	worker, err := maintenance.NewWorker(
		tenantID, store, fixedClock{now: time.Date(2026, 9, 8, 5, 0, 0, 0, time.UTC)},
		time.Hour, 24*time.Hour, 30*24*time.Hour, 500,
	)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- worker.Run(ctx) }()
	request := <-store.called
	if request.TenantID != tenantID || request.BatchSize != 500 || !request.DeleteBefore.Before(request.ScrubBefore) {
		t.Fatalf("maintenance request = %#v", request)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("stop worker: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("maintenance worker did not stop")
	}
	if store.calls.Load() != 1 {
		t.Fatalf("maintenance calls = %d, want 1", store.calls.Load())
	}
}

type recordingStore struct {
	calls  atomic.Int32
	called chan maintenance.Request
}

func (store *recordingStore) Maintain(_ context.Context, request maintenance.Request) (maintenance.Result, error) {
	store.calls.Add(1)
	store.called <- request
	return maintenance.Result{}, nil
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

var _ agent.Clock = fixedClock{}
