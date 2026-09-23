package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type sessionTestSettings struct{ settings config.Settings }

func (repository sessionTestSettings) Load(context.Context) (SettingsSnapshot, error) {
	return SettingsSnapshot{Revision: 1, Values: repository.settings}, nil
}

func (sessionTestSettings) Save(context.Context, uint64, config.Settings) (SettingsSnapshot, error) {
	return SettingsSnapshot{}, errors.New("not used")
}

type sessionTestBindings struct {
	mu      sync.Mutex
	binding SessionBinding
}

func (repository *sessionTestBindings) LoadSessionBinding(context.Context) (SessionBinding, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.binding, nil
}

func (repository *sessionTestBindings) BeginSessionPairing(_ context.Context, scope SessionScope) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.binding.State == SessionPaired || repository.binding.HasPendingScope {
		return errors.New("busy")
	}
	repository.binding.PendingScope = scope
	repository.binding.HasPendingScope = true
	return nil
}

func (repository *sessionTestBindings) MarkSessionPaired(_ context.Context, scope SessionScope, accountID string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.binding.HasPendingScope && repository.binding.PendingScope != scope {
		return errors.New("scope changed")
	}
	repository.binding.ActiveScope = scope
	repository.binding.HasActiveScope = true
	repository.binding.HasPendingScope = false
	repository.binding.PendingScope = SessionScope{}
	repository.binding.WhatsAppAccountID = accountID
	repository.binding.State = SessionPaired
	return nil
}

func (repository *sessionTestBindings) AbortSessionPairing(_ context.Context, scope SessionScope) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.binding.HasPendingScope && repository.binding.PendingScope != scope {
		return errors.New("scope changed")
	}
	repository.binding.HasPendingScope = false
	repository.binding.PendingScope = SessionScope{}
	return nil
}

func (repository *sessionTestBindings) MarkSessionRevoked(_ context.Context, scope SessionScope) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.binding.HasActiveScope || repository.binding.ActiveScope != scope {
		return errors.New("scope changed")
	}
	repository.binding.State = SessionRevoked
	repository.binding.WhatsAppAccountID = ""
	return nil
}

type sessionTestScopes struct{ tenantID identity.TenantID }

func (resolver sessionTestScopes) ResolveSessionSnapshot(_ context.Context, _ string, settings config.Settings, preferred SessionScope) (config.Snapshot, error) {
	if preferred.TenantID.IsZero() {
		preferred.TenantID = resolver.tenantID
	}
	if preferred.AccountID.IsZero() {
		accountID, err := identity.NewAccountID()
		if err != nil {
			return config.Snapshot{}, err
		}
		preferred.AccountID = accountID
	}
	return config.SessionSnapshotWithIdentity(settings, preferred.TenantID, preferred.AccountID)
}

type sessionTestFactory struct {
	mu       sync.Mutex
	runtimes []*sessionTestRuntime
}

func (factory *sessionTestFactory) OpenSession(context.Context, config.Snapshot) (ManagedSession, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.runtimes) == 0 {
		return nil, errors.New("no fake runtime available")
	}
	runtime := factory.runtimes[0]
	factory.runtimes = factory.runtimes[1:]
	return runtime, nil
}

type sessionTestRuntime struct {
	mu           sync.Mutex
	session      bool
	connect      bool
	revoke       bool
	logoutErr    error
	logoutWait   bool
	logoutStart  chan struct{}
	accountID    string
	request      SessionRunRequest
	closed       bool
	pairingReady chan struct{}
}

func (runtime *sessionTestRuntime) HasSession() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.session
}

func (runtime *sessionTestRuntime) WhatsAppAccountID() string {
	return runtime.accountID
}

func (runtime *sessionTestRuntime) Run(ctx context.Context, request SessionRunRequest, emit func(SessionRuntimeEvent)) error {
	runtime.mu.Lock()
	runtime.request = request
	runtime.mu.Unlock()
	if request.Mode == SessionRunPairing {
		emit(SessionRuntimeEvent{State: RuntimePairing, Pairing: &SessionPairing{Method: request.Method, Code: "TEST", ExpiresAt: time.Now().Add(time.Minute)}})
		if runtime.pairingReady != nil {
			close(runtime.pairingReady)
		}
	}
	if runtime.revoke {
		emit(SessionRuntimeEvent{State: RuntimeRevoked})
		return nil
	}
	if runtime.connect || request.Mode == SessionRunResume {
		runtime.mu.Lock()
		runtime.session = true
		runtime.mu.Unlock()
		if runtime.accountID == "" {
			runtime.accountID = "123456789@s.whatsapp.net"
		}
		emit(SessionRuntimeEvent{State: RuntimeConnected, WhatsAppAccountID: runtime.accountID})
	}
	<-ctx.Done()
	return nil
}

func (runtime *sessionTestRuntime) Logout(ctx context.Context) error {
	if runtime.logoutWait {
		if runtime.logoutStart != nil {
			close(runtime.logoutStart)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if runtime.logoutErr != nil {
		return runtime.logoutErr
	}
	runtime.mu.Lock()
	runtime.session = false
	runtime.mu.Unlock()
	return nil
}

func (*sessionTestRuntime) Reconnect() error { return nil }

func (runtime *sessionTestRuntime) Close(context.Context) error {
	runtime.mu.Lock()
	runtime.closed = true
	runtime.mu.Unlock()
	return nil
}

type sessionTestEvents struct{ events chan SessionEvent }

func (sink sessionTestEvents) TryPublish(event SessionEvent) bool {
	select {
	case sink.events <- event:
		return true
	default:
		return false
	}
}

func TestSessionControllerPairingCancelClearsPendingScope(t *testing.T) {
	controller, bindings, runtime := newSessionTestController(t, SessionBinding{State: SessionUnpaired}, &sessionTestRuntime{pairingReady: make(chan struct{})})
	operation, err := controller.BeginPairing(context.Background(), BeginPairingRequest{Method: PairingQR})
	if err != nil {
		t.Fatal(err)
	}
	waitChannel(t, runtime.pairingReady)
	status, err := controller.CancelPairing(context.Background(), operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if status.BindingState != SessionUnpaired || status.RuntimeState != RuntimeStopped {
		t.Fatalf("cancel status = %+v", status)
	}
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if binding.HasPendingScope {
		t.Fatalf("pending scope remains after cancellation: %+v", binding)
	}
}

func TestSessionControllerCompletesPairingIntoPersistentSession(t *testing.T) {
	runtime := &sessionTestRuntime{connect: true, pairingReady: make(chan struct{})}
	controller, bindings, _ := newSessionTestController(t, SessionBinding{State: SessionUnpaired}, runtime)
	if _, err := controller.BeginPairing(context.Background(), BeginPairingRequest{Method: PairingQR}); err != nil {
		t.Fatal(err)
	}
	waitForSessionState(t, controller, RuntimeConnected)
	status, err := controller.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BindingState != SessionPaired || status.RuntimeState != RuntimeStopped || !status.SessionPresent {
		t.Fatalf("completed pairing status = %+v", status)
	}
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if binding.State != SessionPaired || binding.HasPendingScope || binding.WhatsAppAccountID == "" {
		t.Fatalf("successful pairing was not made durable: %+v", binding)
	}
}

func TestSessionControllerResumeStopPreservesPairedBinding(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionPaired, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, WhatsAppAccountID: "123456789@s.whatsapp.net"}}
	runtime := &sessionTestRuntime{session: true, accountID: "123456789@s.whatsapp.net", connect: true}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	operation, err := controller.Resume(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionState(t, controller, RuntimeConnected)
	status, err := controller.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BindingState != SessionPaired || status.RuntimeState != RuntimeStopped || !status.SessionPresent {
		t.Fatalf("stop status = %+v", status)
	}
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if binding.State != SessionPaired || binding.ActiveScope.AccountID != accountID {
		t.Fatalf("stop changed durable binding: %+v (operation %s)", binding, operation.OperationID)
	}
}

func TestSessionControllerExternalLogoutMarksSessionRevoked(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionPaired, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, WhatsAppAccountID: "123456789@s.whatsapp.net"}}
	runtime := &sessionTestRuntime{session: true, accountID: "123456789@s.whatsapp.net", revoke: true}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	if _, err := controller.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForBindingState(t, bindings, SessionRevoked)
	status, err := controller.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BindingState != SessionRevoked || status.SessionPresent {
		t.Fatalf("revoked status = %+v", status)
	}
}

func TestSessionControllerMissingLocalDeviceMakesPairingAvailableAgain(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionPaired, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, WhatsAppAccountID: "123456789@s.whatsapp.net"}}
	runtime := &sessionTestRuntime{session: false}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	if _, err := controller.Resume(context.Background()); err == nil {
		t.Fatal("resume unexpectedly succeeded without a local device")
	}
	status, err := controller.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BindingState != SessionRevoked || status.SessionPresent {
		t.Fatalf("missing-device status = %+v", status)
	}
}

func TestSessionControllerLogoutWhenStoppedRevokesPersistence(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionPaired, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, WhatsAppAccountID: "123456789@s.whatsapp.net"}}
	runtime := &sessionTestRuntime{session: true, accountID: "123456789@s.whatsapp.net"}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	operation, err := controller.Logout(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status.BindingState != SessionRevoked || operation.Status.SessionPresent {
		t.Fatalf("logout status = %+v", operation.Status)
	}
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if binding.State != SessionRevoked || binding.WhatsAppAccountID != "" {
		t.Fatalf("logout did not persist revocation: %+v", binding)
	}
}

func TestSessionControllerCloseCancelsLogoutBeforeWaitingForOperationLock(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionPaired, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, WhatsAppAccountID: "123456789@s.whatsapp.net"}}
	runtime := &sessionTestRuntime{session: true, logoutWait: true, logoutStart: make(chan struct{})}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	logoutDone := make(chan error, 1)
	go func() {
		_, err := controller.Logout(context.Background())
		logoutDone <- err
	}()
	waitChannel(t, runtime.logoutStart)
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := controller.Close(closeCtx); err != nil {
		t.Fatalf("close while logout was in progress: %v", err)
	}
	select {
	case err := <-logoutDone:
		if err == nil {
			t.Fatal("logout unexpectedly succeeded after shutdown cancelled its context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("logout did not release the operation lock after shutdown")
	}
}

func TestSessionControllerRevokedRelinkRotatesAccountScope(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionRevoked, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true}}
	runtime := &sessionTestRuntime{pairingReady: make(chan struct{})}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	operation, err := controller.BeginPairing(context.Background(), BeginPairingRequest{Method: PairingQR})
	if err != nil {
		t.Fatal(err)
	}
	waitChannel(t, runtime.pairingReady)
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if !binding.HasPendingScope || binding.PendingScope.AccountID == accountID || binding.PendingScope.TenantID == tenantID {
		t.Fatalf("re-pair did not isolate the new account scope: %+v", binding)
	}
	if _, err := controller.CancelPairing(context.Background(), operation.OperationID); err != nil {
		t.Fatal(err)
	}
}

func newSessionTestController(t *testing.T, binding SessionBinding, runtime *sessionTestRuntime) (*SessionController, *sessionTestBindings, *sessionTestRuntime) {
	t.Helper()
	bindings := &sessionTestBindings{binding: binding}
	return newSessionTestControllerWith(t, bindings, runtime), bindings, runtime
}

func newSessionTestControllerWith(t *testing.T, bindings *sessionTestBindings, runtimes ...*sessionTestRuntime) *SessionController {
	t.Helper()
	tenantID, _, err := newTestSessionIDsErr()
	if err != nil {
		t.Fatal(err)
	}
	factory := &sessionTestFactory{runtimes: runtimes}
	sink := sessionTestEvents{events: make(chan SessionEvent, 16)}
	controller, err := NewSessionController(sessionTestSettings{settings: config.DefaultSettings()}, bindings, sessionTestScopes{tenantID: tenantID}, factory, sink, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = controller.Close(ctx)
	})
	return controller
}

func newTestSessionIDs(t *testing.T) (identity.TenantID, identity.AccountID) {
	t.Helper()
	tenantID, accountID, err := newTestSessionIDsErr()
	if err != nil {
		t.Fatal(err)
	}
	return tenantID, accountID
}

func newTestSessionIDsErr() (identity.TenantID, identity.AccountID, error) {
	tenantID, err := identity.NewTenantID()
	if err != nil {
		return identity.TenantID{}, identity.AccountID{}, err
	}
	accountID, err := identity.NewAccountID()
	return tenantID, accountID, err
}

func waitChannel(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session runtime event")
	}
}

func waitForSessionState(t *testing.T, controller *SessionController, want SessionRuntimeState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := controller.GetStatus(context.Background())
		if err == nil && status.RuntimeState == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session did not reach runtime state %q", want)
}

func waitForBindingState(t *testing.T, bindings *sessionTestBindings, want SessionBindingState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		binding, _ := bindings.LoadSessionBinding(context.Background())
		if binding.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session did not reach binding state %q", want)
}
