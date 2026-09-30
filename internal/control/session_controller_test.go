package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/config"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
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

func (repository *sessionTestBindings) BeginSessionLink(_ context.Context, scope SessionScope) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.binding.State == SessionLinked || repository.binding.HasPendingScope {
		return errors.New("busy")
	}
	repository.binding.PendingScope = scope
	repository.binding.HasPendingScope = true
	return nil
}

func (repository *sessionTestBindings) MarkSessionLinked(_ context.Context, scope SessionScope, accountID string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.binding.HasPendingScope && repository.binding.PendingScope != scope {
		return errors.New("scope changed")
	}
	repository.binding.ActiveScope = scope
	repository.binding.HasActiveScope = true
	repository.binding.HasPendingScope = false
	repository.binding.PendingScope = SessionScope{}
	repository.binding.DiscordBotID = accountID
	repository.binding.State = SessionLinked
	return nil
}

func (repository *sessionTestBindings) AbortSessionLink(_ context.Context, scope SessionScope) error {
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
	mu          sync.Mutex
	session     bool
	connect     bool
	revoke      bool
	logoutErr   error
	logoutWait  bool
	logoutStart chan struct{}
	accountID   string
	request     SessionRunRequest
	closed      bool
	linkReady   chan struct{}
}

func (runtime *sessionTestRuntime) HasSession() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.session
}

func (runtime *sessionTestRuntime) DiscordBotID() string {
	return runtime.accountID
}

func (runtime *sessionTestRuntime) Run(ctx context.Context, request SessionRunRequest, emit func(SessionRuntimeEvent)) error {
	runtime.mu.Lock()
	runtime.request = request
	runtime.mu.Unlock()
	if request.Mode == SessionRunLink {
		emit(SessionRuntimeEvent{State: RuntimeLinking})
		if runtime.linkReady != nil {
			close(runtime.linkReady)
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
			runtime.accountID = "123456789"
		}
		emit(SessionRuntimeEvent{State: RuntimeConnected, DiscordBotID: runtime.accountID, BotName: "Vivy"})
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

func TestSessionControllerLinkCancelClearsPendingScope(t *testing.T) {
	controller, bindings, runtime := newSessionTestController(t, SessionBinding{State: SessionUnlinked}, &sessionTestRuntime{linkReady: make(chan struct{})})
	operation, err := controller.BeginLink(context.Background(), BeginLinkRequest{Token: testBotToken})
	if err != nil {
		t.Fatal(err)
	}
	waitChannel(t, runtime.linkReady)
	status, err := controller.CancelLink(context.Background(), operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if status.BindingState != SessionUnlinked || status.RuntimeState != RuntimeStopped {
		t.Fatalf("cancel status = %+v", status)
	}
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if binding.HasPendingScope {
		t.Fatalf("pending scope remains after cancellation: %+v", binding)
	}
}

func TestSessionControllerCompletesLinkIntoPersistentSession(t *testing.T) {
	runtime := &sessionTestRuntime{connect: true, linkReady: make(chan struct{})}
	controller, bindings, _ := newSessionTestController(t, SessionBinding{State: SessionUnlinked}, runtime)
	if _, err := controller.BeginLink(context.Background(), BeginLinkRequest{Token: testBotToken}); err != nil {
		t.Fatal(err)
	}
	waitForSessionState(t, controller, RuntimeConnected)
	status, err := controller.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BindingState != SessionLinked || status.RuntimeState != RuntimeStopped || !status.SessionPresent {
		t.Fatalf("completed link status = %+v", status)
	}
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if binding.State != SessionLinked || binding.HasPendingScope || binding.DiscordBotID == "" {
		t.Fatalf("successful link was not made durable: %+v", binding)
	}
}

func TestSessionControllerResumeStopPreservesLinkedBinding(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionLinked, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, DiscordBotID: "123456789"}}
	runtime := &sessionTestRuntime{session: true, accountID: "123456789", connect: true}
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
	if status.BindingState != SessionLinked || status.RuntimeState != RuntimeStopped || !status.SessionPresent {
		t.Fatalf("stop status = %+v", status)
	}
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if binding.State != SessionLinked || binding.ActiveScope.AccountID != accountID {
		t.Fatalf("stop changed durable binding: %+v (operation %s)", binding, operation.OperationID)
	}
}

func TestSessionControllerExternalLogoutMarksSessionRevoked(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionLinked, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, DiscordBotID: "123456789"}}
	runtime := &sessionTestRuntime{session: true, accountID: "123456789", revoke: true}
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

func TestSessionControllerMissingTokenMakesLinkingAvailableAgain(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionLinked, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, DiscordBotID: "123456789"}}
	runtime := &sessionTestRuntime{session: false}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	if _, err := controller.Resume(context.Background()); err == nil {
		t.Fatal("resume unexpectedly succeeded without a saved token")
	}
	status, err := controller.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BindingState != SessionRevoked || status.SessionPresent {
		t.Fatalf("missing-token status = %+v", status)
	}
}

func TestSessionControllerLogoutWhenStoppedRevokesPersistence(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionLinked, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, DiscordBotID: "123456789"}}
	runtime := &sessionTestRuntime{session: true, accountID: "123456789"}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	operation, err := controller.Logout(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status.BindingState != SessionRevoked || operation.Status.SessionPresent {
		t.Fatalf("logout status = %+v", operation.Status)
	}
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if binding.State != SessionRevoked {
		t.Fatalf("logout did not persist revocation: %+v", binding)
	}
}

func TestSessionControllerCloseCancelsLogoutBeforeWaitingForOperationLock(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionLinked, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, DiscordBotID: "123456789"}}
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
	runtime := &sessionTestRuntime{linkReady: make(chan struct{})}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	operation, err := controller.BeginLink(context.Background(), BeginLinkRequest{Token: testBotToken})
	if err != nil {
		t.Fatal(err)
	}
	waitChannel(t, runtime.linkReady)
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if !binding.HasPendingScope || binding.PendingScope.AccountID == accountID || binding.PendingScope.TenantID == tenantID {
		t.Fatalf("relink did not isolate the new bot scope: %+v", binding)
	}
	if _, err := controller.CancelLink(context.Background(), operation.OperationID); err != nil {
		t.Fatal(err)
	}
}

// testBotToken is shaped like a bot token whose first part encodes the bot
// ID 123456789.
const testBotToken = "MTIzNDU2Nzg5.GabcDE.abcdefghijklmnopqrstuvwxyz0123456789"

func TestSessionControllerRelinkingTheSameBotKeepsItsScope(t *testing.T) {
	tenantID, accountID := newTestSessionIDs(t)
	bindings := &sessionTestBindings{binding: SessionBinding{State: SessionRevoked, ActiveScope: SessionScope{TenantID: tenantID, AccountID: accountID}, HasActiveScope: true, DiscordBotID: "123456789"}}
	runtime := &sessionTestRuntime{linkReady: make(chan struct{})}
	controller := newSessionTestControllerWith(t, bindings, runtime)
	operation, err := controller.BeginLink(context.Background(), BeginLinkRequest{Token: "Bot " + testBotToken})
	if err != nil {
		t.Fatal(err)
	}
	waitChannel(t, runtime.linkReady)
	binding, _ := bindings.LoadSessionBinding(context.Background())
	if !binding.HasPendingScope || binding.PendingScope != (SessionScope{TenantID: tenantID, AccountID: accountID}) {
		t.Fatalf("relinking the same bot moved it to a new scope: %+v", binding)
	}
	runtime.mu.Lock()
	token := runtime.request.Token
	runtime.mu.Unlock()
	if token != testBotToken {
		t.Fatalf("link token = %q, want the token without its Bot prefix", token)
	}
	if _, err := controller.CancelLink(context.Background(), operation.OperationID); err != nil {
		t.Fatal(err)
	}
}

func TestSessionControllerRejectsMalformedTokens(t *testing.T) {
	controller, _, _ := newSessionTestController(t, SessionBinding{State: SessionUnlinked}, &sessionTestRuntime{})
	for _, token := range []string{"", "   ", "Bot ", "Bot", "has space.in.it", "tab\tin.si.de", "no-dots", "one.dot", "Bot two words.x.y"} {
		if _, err := controller.BeginLink(context.Background(), BeginLinkRequest{Token: token}); err == nil {
			t.Fatalf("token %q was accepted", token)
		}
	}
}

func TestBotIDFromToken(t *testing.T) {
	for token, want := range map[string]string{
		testBotToken:                   "123456789",
		"MTIzNDU2Nzg5MDEyMzQ1Njc4.x.y": "123456789012345678",
		"bm90LWFuLWlk.x.y":             "",
		"!!!.x.y":                      "",
		"":                             "",
	} {
		if got := BotIDFromToken(token); got != want {
			t.Fatalf("BotIDFromToken(%q) = %q, want %q", token, got, want)
		}
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
