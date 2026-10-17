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

func TestRuntimeDoesNotOpenUntilConnectorIsReady(t *testing.T) {
	connector := newFakeConnector()
	connector.startReady.Store(false)
	runtime := newRuntime(t, connector)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runtime.Run(ctx) }()

	waitForState(t, runtime, account.StateConnecting)
	if runtime.Ready() {
		t.Fatal("connecting runtime is ready")
	}
	connector.events <- account.ConnectionEvent{Connected: true}
	time.Sleep(10 * time.Millisecond)
	if runtime.Snapshot().State != account.StateConnecting {
		t.Fatalf("runtime opened before connector readiness: %s", runtime.Snapshot().State)
	}
	connector.ready.Store(true)
	connector.events <- account.ConnectionEvent{Connected: true}
	waitForState(t, runtime, account.StateOpen)

	cancel()
	if err := waitResult(t, result); err != nil {
		t.Fatalf("stop runtime: %v", err)
	}
}

func TestRuntimeHandlesClosedChannelsUntilCancellation(t *testing.T) {
	connector := newFakeConnector()
	runtime := newRuntime(t, connector)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runtime.Run(ctx) }()
	waitForState(t, runtime, account.StateOpen)
	close(connector.events)
	close(connector.fatal)

	select {
	case err := <-result:
		t.Fatalf("runtime returned after channel closure: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := waitResult(t, result); err != nil {
		t.Fatalf("stop runtime after closed channels: %v", err)
	}
}

func TestRuntimeRejectsConcurrentRun(t *testing.T) {
	connector := newFakeConnector()
	connector.startGate = make(chan struct{})
	runtime := newRuntime(t, connector)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runtime.Run(ctx) }()
	waitForState(t, runtime, account.StateConnecting)
	if err := runtime.Run(context.Background()); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("concurrent run error = %v, want conflict", err)
	}
	close(connector.startGate)
	waitForState(t, runtime, account.StateOpen)
	cancel()
	if err := waitResult(t, result); err != nil {
		t.Fatalf("stop runtime: %v", err)
	}
}

func TestRuntimePreservesStopFailure(t *testing.T) {
	connector := newFakeConnector()
	connector.stopErr = agent.NewError(agent.ErrorTimeout, "stop connector", context.DeadlineExceeded)
	runtime := newRuntime(t, connector)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runtime.Run(ctx) }()
	waitForState(t, runtime, account.StateOpen)
	cancel()
	if err := waitResult(t, result); !agent.IsCode(err, agent.ErrorTimeout) {
		t.Fatalf("stop error = %v, want timeout", err)
	}
	if snapshot := runtime.Snapshot(); snapshot.State != account.StateFailed || snapshot.ErrorCode != agent.ErrorTimeout {
		t.Fatalf("runtime after stop failure = %#v", snapshot)
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

func waitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("runtime did not finish")
		return nil
	}
}

type fakeConnector struct {
	ready      atomic.Bool
	startReady atomic.Bool
	starts     atomic.Int32
	stops      atomic.Int32
	events     chan account.ConnectionEvent
	fatal      chan error
	startGate  chan struct{}
	stopErr    error
}

func newFakeConnector() *fakeConnector {
	connector := &fakeConnector{events: make(chan account.ConnectionEvent, 2), fatal: make(chan error, 1)}
	connector.startReady.Store(true)
	return connector
}

func (connector *fakeConnector) Start(ctx context.Context) error {
	connector.starts.Add(1)
	if connector.startGate != nil {
		select {
		case <-connector.startGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	connector.ready.Store(connector.startReady.Load())
	return nil
}

func (connector *fakeConnector) Stop(context.Context) error {
	connector.stops.Add(1)
	connector.ready.Store(false)
	return connector.stopErr
}

func (connector *fakeConnector) Ready() bool                            { return connector.ready.Load() }
func (connector *fakeConnector) Events() <-chan account.ConnectionEvent { return connector.events }
func (connector *fakeConnector) Fatal() <-chan error                    { return connector.fatal }
