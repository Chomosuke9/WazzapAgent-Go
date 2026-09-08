package account_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/account"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestRuntimeBecomesReadyAndStopsConnectorOnCancellation(t *testing.T) {
	connector := newFakeConnector()
	runtime := newRuntime(t, connector)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runtime.Run(ctx) }()
	waitForState(t, runtime, account.StateOpen)
	if !runtime.Ready() {
		t.Fatal("open runtime is not ready")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("stop runtime: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	if connector.stops.Load() != 1 || runtime.Snapshot().State != account.StateStopped || runtime.Ready() {
		t.Fatalf("stopped runtime = %#v, stops=%d", runtime.Snapshot(), connector.stops.Load())
	}
}

func TestRuntimePropagatesFatalConnectorFailure(t *testing.T) {
	connector := newFakeConnector()
	runtime := newRuntime(t, connector)
	result := make(chan error, 1)
	go func() { result <- runtime.Run(context.Background()) }()
	waitForState(t, runtime, account.StateOpen)
	connector.ready.Store(false)
	connector.fatal <- agent.NewError(agent.ErrorUnavailable, "connector", context.DeadlineExceeded)
	select {
	case err := <-result:
		if !agent.IsCode(err, agent.ErrorUnavailable) {
			t.Fatalf("fatal runtime error = %v, want unavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not propagate fatal failure")
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != account.StateFailed || snapshot.ErrorCode != agent.ErrorUnavailable || connector.stops.Load() != 1 {
		t.Fatalf("failed runtime = %#v, stops=%d", snapshot, connector.stops.Load())
	}
}

func newRuntime(t *testing.T, connector *fakeConnector) *account.Runtime {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	runtime, err := account.NewRuntime(tenantID, accountID, connector, time.Second)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	return runtime
}

func waitForState(t *testing.T, runtime *account.Runtime, wanted account.State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.Snapshot().State == wanted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime state = %s, want %s", runtime.Snapshot().State.String(), wanted.String())
}

type fakeConnector struct {
	ready  atomic.Bool
	starts atomic.Int32
	stops  atomic.Int32
	events chan account.ConnectionEvent
	fatal  chan error
}

func newFakeConnector() *fakeConnector {
	return &fakeConnector{events: make(chan account.ConnectionEvent, 2), fatal: make(chan error, 1)}
}

func (connector *fakeConnector) Start(context.Context) error {
	connector.starts.Add(1)
	connector.ready.Store(true)
	return nil
}

func (connector *fakeConnector) Stop(context.Context) error {
	connector.stops.Add(1)
	connector.ready.Store(false)
	return nil
}

func (connector *fakeConnector) Ready() bool                            { return connector.ready.Load() }
func (connector *fakeConnector) Events() <-chan account.ConnectionEvent { return connector.events }
func (connector *fakeConnector) Fatal() <-chan error                    { return connector.fatal }
