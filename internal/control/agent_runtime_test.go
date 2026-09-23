package control

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type agentTestBindings struct{ binding SessionBinding }

func (repository *agentTestBindings) LoadSessionBinding(context.Context) (SessionBinding, error) {
	return repository.binding, nil
}
func (*agentTestBindings) BeginSessionPairing(context.Context, SessionScope) error { return nil }
func (*agentTestBindings) MarkSessionPaired(context.Context, SessionScope, string) error {
	return nil
}
func (*agentTestBindings) AbortSessionPairing(context.Context, SessionScope) error { return nil }
func (*agentTestBindings) MarkSessionRevoked(context.Context, SessionScope) error  { return nil }

type agentTestSessions struct {
	mu        sync.Mutex
	status    SessionStatus
	stopCalls int
	stopErr   error
}

func (sessions *agentTestSessions) GetStatus(context.Context) (SessionStatus, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.status, nil
}
func (sessions *agentTestSessions) Stop(context.Context) (SessionStatus, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.stopCalls++
	if sessions.stopErr != nil {
		return SessionStatus{}, sessions.stopErr
	}
	sessions.status.RuntimeState = RuntimeStopped
	return sessions.status, nil
}

type agentTestRuntimeFactory struct {
	mu        sync.Mutex
	runtimes  []*agentTestManagedRuntime
	snapshots []config.Snapshot
	openErr   error
}

func (factory *agentTestRuntimeFactory) OpenAgentRuntime(_ context.Context, snapshot config.Snapshot) (ManagedAgentRuntime, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if factory.openErr != nil {
		return nil, factory.openErr
	}
	runtime := &agentTestManagedRuntime{done: make(chan struct{}), closeErr: nil}
	factory.runtimes = append(factory.runtimes, runtime)
	factory.snapshots = append(factory.snapshots, snapshot)
	return runtime, nil
}

type agentTestManagedRuntime struct {
	started   atomic.Bool
	runOnce   sync.Once
	done      chan struct{}
	closeErr  error
	runErr    error
	closeCall atomic.Int32
}

func (runtime *agentTestManagedRuntime) Run(ctx context.Context) error {
	runtime.started.Store(true)
	<-ctx.Done()
	runtime.runOnce.Do(func() { close(runtime.done) })
	return runtime.runErr
}
func (runtime *agentTestManagedRuntime) Close(ctx context.Context) error {
	runtime.closeCall.Add(1)
	select {
	case <-runtime.done:
		return runtime.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (runtime *agentTestManagedRuntime) Snapshot() AgentRuntimeSnapshot {
	state := "stopped"
	if runtime.started.Load() {
		state = "open"
	}
	return AgentRuntimeSnapshot{Started: runtime.started.Load(), WhatsAppState: state}
}

func validAgentSettings() config.Settings {
	settings := config.DefaultSettings()
	settings.AssistantName = "Test Assistant"
	settings.BasePrompt = "Answer messages helpfully."
	settings.OwnerJID = "628123456789@s.whatsapp.net"
	settings.ChatAllowlist = []string{"628123456789@s.whatsapp.net"}
	settings.LLMEndpoint = "https://llm.example/v1"
	settings.LLMAPIKey = "synthetic-test-key"
	settings.LLMModel = "test-model"
	settings.AgentEnabled = true
	settings.WhatsAppEnabled = true
	return settings
}

func newAgentControllerForTest(t *testing.T, settings config.Settings, bindingState SessionBindingState) (*AgentController, *memorySettingsRepository, *agentTestSessions, *agentTestRuntimeFactory) {
	t.Helper()
	tenantID, err := identity.NewTenantID()
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := identity.NewAccountID()
	if err != nil {
		t.Fatal(err)
	}
	settings.TenantID, settings.AccountID = tenantID, accountID
	repository := newMemorySettingsRepository(settings)
	bindings := &agentTestBindings{binding: SessionBinding{
		State: bindingState, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true,
	}}
	sessions := &agentTestSessions{status: SessionStatus{BindingState: bindingState, RuntimeState: RuntimeConnected, SessionPresent: bindingState == SessionPaired}}
	factory := &agentTestRuntimeFactory{}
	controller, err := NewAgentController(repository, bindings, sessions, factory, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = controller.Close(ctx)
	})
	return controller, repository, sessions, factory
}

func waitAgentRuntimeState(t *testing.T, controller *AgentController, state AgentRuntimeState) AgentRuntimeStatus {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, err := controller.GetStatus(context.Background())
		if err != nil {
			t.Fatalf("get Agent status: %v", err)
		}
		if status.State == state {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	status, _ := controller.GetStatus(context.Background())
	t.Fatalf("Agent state = %s, want %s", status.State, state)
	return AgentRuntimeStatus{}
}

func TestAgentControllerStartsExistingRuntimeAndStopsSessionOnlyOwner(t *testing.T) {
	controller, _, sessions, factory := newAgentControllerForTest(t, validAgentSettings(), SessionPaired)
	status, err := controller.Start(context.Background())
	if err != nil {
		t.Fatalf("start Agent: %v", err)
	}
	if status.State != BotRunning || status.ActiveRevision != 1 || status.PendingChanges || status.WhatsAppState != "open" {
		t.Fatalf("unexpected started status: %#v", status)
	}
	if len(factory.snapshots) != 1 || factory.snapshots[0].TenantID().IsZero() || factory.snapshots[0].AccountID().IsZero() {
		t.Fatalf("runtime did not use the paired session identity: %#v", factory.snapshots)
	}
	sessions.mu.Lock()
	stopCalls := sessions.stopCalls
	sessions.mu.Unlock()
	if stopCalls != 1 {
		t.Fatalf("session-only runtime stop calls = %d, want 1", stopCalls)
	}
	if !controller.IsActive() {
		t.Fatal("running Agent is not reported active")
	}

	stopped, err := controller.Stop(context.Background())
	if err != nil {
		t.Fatalf("stop Agent: %v", err)
	}
	if stopped.State != BotStopped || stopped.ActiveRevision != 0 || controller.IsActive() {
		t.Fatalf("unexpected stopped status: %#v", stopped)
	}
}

func TestAgentControllerApplyRestartsOnlyActiveRuntimeAtExpectedRevision(t *testing.T) {
	controller, repository, _, factory := newAgentControllerForTest(t, validAgentSettings(), SessionPaired)
	if _, err := controller.Start(context.Background()); err != nil {
		t.Fatalf("start Agent: %v", err)
	}

	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed := loaded.Values
	changed.LLMModel = "updated-model"
	if _, err := repository.Save(context.Background(), loaded.Revision, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.ApplySettings(context.Background(), loaded.Revision); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("stale Apply error = %v, want conflict", err)
	}
	status, err := controller.ApplySettings(context.Background(), loaded.Revision+1)
	if err != nil {
		t.Fatalf("apply current settings: %v", err)
	}
	if status.State != BotRunning || status.ActiveRevision != loaded.Revision+1 || status.PendingChanges {
		t.Fatalf("unexpected applied status: %#v", status)
	}
	if len(factory.snapshots) != 2 || factory.snapshots[1].LLMModel() != "updated-model" {
		t.Fatalf("new runtime did not receive saved model: %#v", factory.snapshots)
	}
}

func TestAgentControllerDoesNotStartWhenApplyingWhileStopped(t *testing.T) {
	controller, _, _, factory := newAgentControllerForTest(t, validAgentSettings(), SessionPaired)
	status, err := controller.ApplySettings(context.Background(), 1)
	if err != nil {
		t.Fatalf("apply while stopped: %v", err)
	}
	if status.State != BotStopped || len(factory.runtimes) != 0 {
		t.Fatalf("Apply unexpectedly started the runtime: status=%#v runtimes=%d", status, len(factory.runtimes))
	}
}

func TestAgentControllerApplyDisablingAgentStopsWithoutLaunchingAnotherRuntime(t *testing.T) {
	controller, repository, _, factory := newAgentControllerForTest(t, validAgentSettings(), SessionPaired)
	if _, err := controller.Start(context.Background()); err != nil {
		t.Fatalf("start Agent: %v", err)
	}
	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed := loaded.Values
	changed.AgentEnabled = false
	if _, err := repository.Save(context.Background(), loaded.Revision, changed); err != nil {
		t.Fatalf("save disabled Agent setting: %v", err)
	}
	status, err := controller.ApplySettings(context.Background(), loaded.Revision+1)
	if err != nil {
		t.Fatalf("apply disabled Agent setting: %v", err)
	}
	if status.State != BotStopped || status.ActiveRevision != 0 || len(factory.runtimes) != 1 {
		t.Fatalf("disabled Agent was not stopped cleanly: status=%#v runtimes=%d", status, len(factory.runtimes))
	}
}

func TestAgentControllerRequiresPairedSessionAndCompleteSettings(t *testing.T) {
	controller, _, _, _ := newAgentControllerForTest(t, validAgentSettings(), SessionUnpaired)
	if _, err := controller.Start(context.Background()); !agent.IsCode(err, agent.ErrorNotReady) {
		t.Fatalf("unpaired start error = %v, want not ready", err)
	}

	settings := validAgentSettings()
	settings.AgentEnabled = false
	controller, _, _, _ = newAgentControllerForTest(t, settings, SessionPaired)
	if _, err := controller.Start(context.Background()); !agent.IsCode(err, agent.ErrorNotReady) {
		t.Fatalf("disabled Agent start error = %v, want not ready", err)
	}

	settings = validAgentSettings()
	settings.LLMAPIKey = ""
	controller, _, _, _ = newAgentControllerForTest(t, settings, SessionPaired)
	if _, err := controller.Start(context.Background()); !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("incomplete settings start error = %v, want invalid argument", err)
	}
}

func TestAgentControllerShutdownClosesRuntime(t *testing.T) {
	controller, _, _, factory := newAgentControllerForTest(t, validAgentSettings(), SessionPaired)
	if _, err := controller.Start(context.Background()); err != nil {
		t.Fatalf("start Agent: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Close(ctx); err != nil {
		t.Fatalf("close controller: %v", err)
	}
	if len(factory.runtimes) != 1 || factory.runtimes[0].closeCall.Load() == 0 {
		t.Fatal("shutdown did not close the managed runtime")
	}
	if _, err := controller.Start(context.Background()); !agent.IsCode(err, agent.ErrorNotReady) {
		t.Fatalf("start after shutdown error = %v, want not ready", err)
	}
}

func TestAgentControllerSerializesSessionMutationsWithRuntimeOwnership(t *testing.T) {
	controller, _, _, _ := newAgentControllerForTest(t, validAgentSettings(), SessionPaired)
	if _, err := controller.Start(context.Background()); err != nil {
		t.Fatalf("start Agent: %v", err)
	}
	called := false
	if err := controller.WithSessionControl(func() error { called = true; return nil }); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("session mutation while Agent is active: %v, want conflict", err)
	}
	if called {
		t.Fatal("session mutation ran while the Agent owned the WhatsApp client")
	}
	if _, err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("stop Agent: %v", err)
	}
	if err := controller.WithSessionControl(func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("session mutation after Agent stopped: called=%t err=%v", called, err)
	}
}
