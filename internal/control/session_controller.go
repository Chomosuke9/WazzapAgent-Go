package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const pairingPhoneMaxDigits = 15

type sessionRun struct {
	id      string
	scope   SessionScope
	request SessionRunRequest
	runtime ManagedSession
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	state     SessionRuntimeState
	pairing   *SessionPairing
	paired    bool
	errorCode agent.ErrorCode
}

// SessionController owns the session-only WhatsApp lifecycle. It never
// constructs the Agent pipeline or exposes a message/outbox operation.
type SessionController struct {
	settings   SettingsRepository
	bindings   SessionBindingRepository
	scopes     SessionScopeResolver
	factory    SessionRuntimeFactory
	events     SessionEventSink
	dataRoot   string
	rootCtx    context.Context
	rootStop   context.CancelFunc
	operations sync.Mutex
	mu         sync.Mutex
	run        *sessionRun
	closed     bool
	now        func() time.Time
}

func NewSessionController(
	settings SettingsRepository,
	bindings SessionBindingRepository,
	scopes SessionScopeResolver,
	factory SessionRuntimeFactory,
	events SessionEventSink,
	dataRoot string,
) (*SessionController, error) {
	if settings == nil || bindings == nil || scopes == nil || factory == nil || strings.TrimSpace(dataRoot) == "" {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create session controller", errors.New("settings, session persistence, scope resolver, runtime factory, and data root are required"))
	}
	rootCtx, rootStop := context.WithCancel(context.Background())
	return &SessionController{settings: settings, bindings: bindings, scopes: scopes, factory: factory, events: events, dataRoot: dataRoot, rootCtx: rootCtx, rootStop: rootStop, now: time.Now}, nil
}

func (controller *SessionController) GetStatus(ctx context.Context) (SessionStatus, error) {
	binding, err := controller.bindings.LoadSessionBinding(ctx)
	if err != nil {
		return SessionStatus{}, sessionRepositoryError("load session status", err)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	status := statusFromBinding(binding)
	if controller.run != nil {
		run := controller.run
		status.RuntimeState = run.state
		status.OperationID = run.id
		status.Pairing = clonePairing(run.pairing)
		status.ErrorCode = run.errorCode
		if run.paired {
			status.SessionPresent = true
			status.BindingState = SessionPaired
		}
		if status.Pairing != nil && !controller.now().Before(status.Pairing.ExpiresAt) {
			status.Pairing = nil
			run.pairing = nil
		}
	}
	return status, nil
}

func (controller *SessionController) GetPairingStatus(ctx context.Context) (SessionPairing, error) {
	status, err := controller.GetStatus(ctx)
	if err != nil {
		return SessionPairing{}, err
	}
	if status.Pairing == nil {
		return SessionPairing{}, agent.NewError(agent.ErrorNotFound, "get pairing status", errors.New("no unexpired pairing code is available"))
	}
	return *status.Pairing, nil
}

func (controller *SessionController) BeginPairing(ctx context.Context, request BeginPairingRequest) (SessionOperation, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	if request.Method != PairingQR && request.Method != PairingPhoneCode {
		return SessionOperation{}, agent.NewError(agent.ErrorInvalidArgument, "begin WhatsApp pairing", errors.New("pairing method is invalid"))
	}
	phone := ""
	if request.Method == PairingPhoneCode {
		var err error
		phone, err = normalizePairingPhone(request.Phone)
		if err != nil {
			return SessionOperation{}, err
		}
	} else if strings.TrimSpace(request.Phone) != "" {
		return SessionOperation{}, agent.NewError(agent.ErrorInvalidArgument, "begin WhatsApp pairing", errors.New("phone number is only used for phone-code pairing"))
	}
	if err := controller.ensureOpen(); err != nil {
		return SessionOperation{}, err
	}
	binding, err := controller.bindings.LoadSessionBinding(ctx)
	if err != nil {
		return SessionOperation{}, sessionRepositoryError("load session binding", err)
	}
	if binding.State == SessionPaired {
		return SessionOperation{}, agent.NewError(agent.ErrorConflict, "begin WhatsApp pairing", errors.New("a WhatsApp session already exists; resume it or log out first"))
	}
	preferred, hasPreferred := binding.ActiveScope, binding.HasActiveScope
	if binding.HasPendingScope {
		// A process may stop while a pairing reservation is durable. Re-open
		// that exact device store before deciding whether it can be resumed.
		snapshot, scope, err := controller.sessionSnapshot(ctx, binding.PendingScope, true)
		if err != nil {
			return SessionOperation{}, err
		}
		managed, err := controller.factory.OpenSession(ctx, snapshot)
		if err != nil {
			return SessionOperation{}, sessionFactoryError("recover WhatsApp pairing", err)
		}
		if managed.HasSession() {
			accountID := managed.WhatsAppAccountID()
			if accountID == "" || controller.bindings.MarkSessionPaired(ctx, scope, accountID) != nil {
				_ = managed.Close(context.Background())
				return SessionOperation{}, agent.NewError(agent.ErrorIntegrityFailure, "recover WhatsApp pairing", errors.New("linked WhatsApp device could not be recorded"))
			}
			_ = managed.Close(context.Background())
			return SessionOperation{}, agent.NewError(agent.ErrorConflict, "begin WhatsApp pairing", errors.New("the interrupted pairing already linked a device; resume it"))
		}
		_ = managed.Close(context.Background())
		if err := controller.bindings.AbortSessionPairing(ctx, scope); err != nil {
			return SessionOperation{}, sessionRepositoryError("recover pending WhatsApp pairing", err)
		}
	}
	if binding.State == SessionRevoked && binding.HasActiveScope {
		newTenantID, tenantErr := identity.NewTenantID()
		if tenantErr != nil {
			return SessionOperation{}, agent.NewError(agent.ErrorInternal, "create WhatsApp tenant scope", tenantErr)
		}
		newAccountID, idErr := identity.NewAccountID()
		if idErr != nil {
			return SessionOperation{}, agent.NewError(agent.ErrorInternal, "create WhatsApp account scope", idErr)
		}
		preferred = SessionScope{TenantID: newTenantID, AccountID: newAccountID}
		hasPreferred = true
	}
	snapshot, scope, err := controller.sessionSnapshot(ctx, preferred, hasPreferred)
	if err != nil {
		return SessionOperation{}, err
	}
	if err := controller.bindings.BeginSessionPairing(ctx, scope); err != nil {
		return SessionOperation{}, sessionRepositoryError("reserve session pairing", err)
	}
	managed, err := controller.factory.OpenSession(ctx, snapshot)
	if err != nil {
		_ = controller.bindings.AbortSessionPairing(context.Background(), scope)
		return SessionOperation{}, sessionFactoryError("open WhatsApp session", err)
	}
	if managed.HasSession() {
		accountID := managed.WhatsAppAccountID()
		if accountID != "" {
			_ = controller.bindings.MarkSessionPaired(ctx, scope, accountID)
		} else {
			_ = controller.bindings.AbortSessionPairing(context.Background(), scope)
		}
		_ = managed.Close(context.Background())
		return SessionOperation{}, agent.NewError(agent.ErrorConflict, "begin WhatsApp pairing", errors.New("a linked device already exists in this WhatsApp store; refresh status and resume it"))
	}
	run, err := controller.newRun(scope, managed, SessionRunRequest{Mode: SessionRunPairing, Method: request.Method, Phone: phone})
	if err != nil {
		_ = controller.bindings.AbortSessionPairing(context.Background(), scope)
		_ = managed.Close(context.Background())
		return SessionOperation{}, err
	}
	if !controller.launch(run) {
		_ = controller.bindings.AbortSessionPairing(context.Background(), scope)
		_ = managed.Close(context.Background())
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "begin WhatsApp pairing", errors.New("session controller is closing"))
	}
	status := controller.statusForRun(run)
	return SessionOperation{OperationID: run.id, Status: status}, nil
}

func (controller *SessionController) Resume(ctx context.Context) (SessionOperation, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	if err := controller.ensureOpen(); err != nil {
		return SessionOperation{}, err
	}
	binding, err := controller.bindings.LoadSessionBinding(ctx)
	if err != nil {
		return SessionOperation{}, sessionRepositoryError("load session binding", err)
	}
	if binding.State != SessionPaired || !binding.HasActiveScope {
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "resume WhatsApp session", errors.New("there is no saved WhatsApp session to resume"))
	}
	snapshot, scope, err := controller.sessionSnapshot(ctx, binding.ActiveScope, true)
	if err != nil {
		return SessionOperation{}, err
	}
	managed, err := controller.factory.OpenSession(ctx, snapshot)
	if err != nil {
		return SessionOperation{}, sessionFactoryError("open WhatsApp session", err)
	}
	if !managed.HasSession() {
		_ = managed.Close(context.Background())
		if err := controller.bindings.MarkSessionRevoked(ctx, binding.ActiveScope); err != nil {
			return SessionOperation{}, sessionRepositoryError("mark missing WhatsApp device", err)
		}
		return SessionOperation{}, agent.NewError(agent.ErrorIntegrityFailure, "resume WhatsApp session", errors.New("saved session is missing from its local device store"))
	}
	run, err := controller.newRun(scope, managed, SessionRunRequest{Mode: SessionRunResume})
	if err != nil {
		_ = managed.Close(context.Background())
		return SessionOperation{}, err
	}
	run.paired = true
	if !controller.launch(run) {
		_ = managed.Close(context.Background())
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "resume WhatsApp session", errors.New("session controller is closing"))
	}
	status, _ := controller.GetStatus(ctx)
	return SessionOperation{OperationID: run.id, Status: status}, nil
}

func (controller *SessionController) Stop(ctx context.Context) (SessionStatus, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	controller.mu.Lock()
	run := controller.run
	if run == nil {
		controller.mu.Unlock()
		return controller.GetStatus(ctx)
	}
	if run.request.Mode == SessionRunPairing && !run.paired {
		controller.mu.Unlock()
		return SessionStatus{}, agent.NewError(agent.ErrorConflict, "stop WhatsApp session", errors.New("cancel pairing to discard the pending link request"))
	}
	run.state = RuntimeStopping
	run.pairing = nil
	run.cancel()
	controller.mu.Unlock()
	if err := waitSessionRun(ctx, run); err != nil {
		return SessionStatus{}, err
	}
	return controller.GetStatus(ctx)
}

func (controller *SessionController) CancelPairing(ctx context.Context, operationID string) (SessionStatus, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	controller.mu.Lock()
	run := controller.run
	if run == nil || run.id != operationID || run.request.Mode != SessionRunPairing || run.paired {
		controller.mu.Unlock()
		return SessionStatus{}, agent.NewError(agent.ErrorConflict, "cancel WhatsApp pairing", errors.New("pairing operation is no longer active"))
	}
	run.state = RuntimeStopping
	run.pairing = nil
	run.cancel()
	controller.mu.Unlock()
	if err := waitSessionRun(ctx, run); err != nil {
		return SessionStatus{}, err
	}
	if err := controller.bindings.AbortSessionPairing(ctx, run.scope); err != nil {
		return SessionStatus{}, sessionRepositoryError("clear pending pairing", err)
	}
	return controller.GetStatus(ctx)
}

func (controller *SessionController) Reconnect(ctx context.Context) (SessionOperation, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	controller.mu.Lock()
	run := controller.run
	if run == nil || !run.paired {
		controller.mu.Unlock()
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "reconnect WhatsApp session", errors.New("no active paired session is running"))
	}
	if err := run.runtime.Reconnect(); err != nil {
		controller.mu.Unlock()
		return SessionOperation{}, agent.NewError(agent.ErrorProviderFailure, "reconnect WhatsApp session", err)
	}
	run.state = RuntimeReconnecting
	status := controller.statusForRunLocked(run, SessionPaired)
	controller.mu.Unlock()
	controller.publish(run.id, status)
	return SessionOperation{OperationID: run.id, Status: status}, nil
}

func (controller *SessionController) Logout(ctx context.Context) (SessionOperation, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	if err := controller.ensureOpen(); err != nil {
		return SessionOperation{}, err
	}
	binding, err := controller.bindings.LoadSessionBinding(ctx)
	if err != nil {
		return SessionOperation{}, sessionRepositoryError("load session binding", err)
	}
	if binding.State != SessionPaired || !binding.HasActiveScope {
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "logout WhatsApp session", errors.New("there is no paired WhatsApp session to log out"))
	}
	controller.mu.Lock()
	run := controller.run
	if run != nil && (!run.paired || run.scope != binding.ActiveScope) {
		controller.mu.Unlock()
		return SessionOperation{}, agent.NewError(agent.ErrorConflict, "logout WhatsApp session", errors.New("a different WhatsApp operation is active"))
	}
	if run != nil {
		run.state = RuntimeLoggingOut
		run.pairing = nil
	}
	controller.mu.Unlock()

	operationID, err := newSessionOperationID()
	if err != nil {
		return SessionOperation{}, agent.NewError(agent.ErrorInternal, "create logout operation", err)
	}
	var temporary ManagedSession
	managed := ManagedSession(nil)
	if run != nil {
		managed = run.runtime
	} else {
		snapshot, _, snapshotErr := controller.sessionSnapshot(ctx, binding.ActiveScope, true)
		if snapshotErr != nil {
			return SessionOperation{}, snapshotErr
		}
		temporary, err = controller.factory.OpenSession(ctx, snapshot)
		if err != nil {
			return SessionOperation{}, sessionFactoryError("open WhatsApp session for logout", err)
		}
		if !temporary.HasSession() {
			_ = temporary.Close(context.Background())
			return SessionOperation{}, agent.NewError(agent.ErrorIntegrityFailure, "open WhatsApp session for logout", errors.New("saved WhatsApp device is missing"))
		}
		managed = temporary
	}
	logoutCtx, cancelLogout := context.WithCancel(ctx)
	stopOnClose := context.AfterFunc(controller.rootCtx, cancelLogout)
	logoutErr := managed.Logout(logoutCtx)
	stopOnClose()
	cancelLogout()
	if logoutErr != nil {
		if temporary != nil {
			_ = temporary.Close(context.Background())
		}
		controller.restoreAfterFailedLogout(run)
		return SessionOperation{}, agent.NewError(agent.ErrorProviderFailure, "logout WhatsApp session", errors.New("WhatsApp did not confirm logout; the saved session was kept"))
	}
	if err := controller.bindings.MarkSessionRevoked(ctx, binding.ActiveScope); err != nil {
		if temporary != nil {
			_ = temporary.Close(context.Background())
		}
		controller.restoreAfterFailedLogout(run)
		return SessionOperation{}, sessionRepositoryError("persist WhatsApp logout", err)
	}
	if temporary != nil {
		if closeErr := temporary.Close(context.Background()); closeErr != nil {
			return SessionOperation{}, sessionRepositoryError("close logged out WhatsApp session", closeErr)
		}
	}
	if run != nil {
		run.cancel()
		if err := waitSessionRun(ctx, run); err != nil {
			return SessionOperation{}, err
		}
	}
	status, err := controller.GetStatus(ctx)
	if err != nil {
		return SessionOperation{}, err
	}
	status.OperationID = operationID
	controller.publish(operationID, status)
	return SessionOperation{OperationID: operationID, Status: status}, nil
}

func (controller *SessionController) Close(ctx context.Context) error {
	controller.mu.Lock()
	controller.closed = true
	controller.rootStop()
	run := controller.run
	if run != nil {
		run.state = RuntimeStopping
		run.pairing = nil
		run.cancel()
	}
	controller.mu.Unlock()
	controller.operations.Lock()
	defer controller.operations.Unlock()
	if run == nil {
		return nil
	}
	return waitSessionRun(ctx, run)
}

func (controller *SessionController) sessionSnapshot(ctx context.Context, preferred SessionScope, hasPreferred bool) (config.Snapshot, SessionScope, error) {
	settings, err := controller.settings.Load(ctx)
	if err != nil {
		return config.Snapshot{}, SessionScope{}, safeRepositoryError("load settings for WhatsApp session", err)
	}
	settings.Values.DataDir = controller.dataRoot
	preferredScope := SessionScope{}
	if hasPreferred {
		preferredScope = preferred
	}
	snapshot, err := controller.scopes.ResolveSessionSnapshot(ctx, controller.dataRoot, settings.Values, preferredScope)
	if err != nil {
		return config.Snapshot{}, SessionScope{}, agent.NewError(agent.ErrorInvalidArgument, "validate WhatsApp session settings", errors.New("data root or session identity is not ready"))
	}
	scope := SessionScope{TenantID: snapshot.TenantID(), AccountID: snapshot.AccountID()}
	if scope.TenantID.IsZero() || scope.AccountID.IsZero() {
		return config.Snapshot{}, SessionScope{}, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp session identity", errors.New("session identity is incomplete"))
	}
	return snapshot, scope, nil
}

func (controller *SessionController) newRun(scope SessionScope, managed ManagedSession, request SessionRunRequest) (*sessionRun, error) {
	id, err := newSessionOperationID()
	if err != nil {
		return nil, agent.NewError(agent.ErrorInternal, "create WhatsApp operation", err)
	}
	ctx, cancel := context.WithCancel(controller.rootCtx)
	return &sessionRun{id: id, scope: scope, request: request, runtime: managed, ctx: ctx, cancel: cancel, done: make(chan struct{}), state: RuntimeStarting}, nil
}

func (controller *SessionController) launch(run *sessionRun) bool {
	controller.mu.Lock()
	if controller.closed || controller.run != nil {
		controller.mu.Unlock()
		run.cancel()
		_ = run.runtime.Close(context.Background())
		return false
	}
	controller.run = run
	controller.mu.Unlock()
	go controller.runSession(run)
	controller.publish(run.id, controller.statusForRun(run))
	return true
}

func (controller *SessionController) runSession(run *sessionRun) {
	err := run.runtime.Run(run.ctx, run.request, func(event SessionRuntimeEvent) { controller.handleRuntimeEvent(run, event) })
	closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	closeErr := run.runtime.Close(closeCtx)
	cancel()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	err = errors.Join(err, closeErr)
	controller.mu.Lock()
	pairingMode := run.request.Mode == SessionRunPairing
	paired := run.paired
	controller.mu.Unlock()
	if pairingMode && !paired {
		_ = controller.bindings.AbortSessionPairing(context.Background(), run.scope)
	}
	controller.mu.Lock()
	if controller.run == run {
		if err != nil {
			run.state = RuntimeFailed
			run.errorCode = agent.CodeOf(err)
		} else {
			run.state = RuntimeStopped
		}
		run.pairing = nil
		controller.run = nil
	}
	status := SessionStatus{BindingState: SessionUnpaired, RuntimeState: run.state, OperationID: run.id, ErrorCode: run.errorCode}
	if run.paired {
		status.BindingState, status.SessionPresent = SessionPaired, true
	}
	if run.state == RuntimeRevoked {
		status.BindingState = SessionRevoked
	}
	if err != nil {
		status.ErrorCode = agent.CodeOf(err)
	}
	close(run.done)
	controller.mu.Unlock()
	if binding, loadErr := controller.bindings.LoadSessionBinding(context.Background()); loadErr == nil {
		status = statusFromBinding(binding)
		controller.mu.Lock()
		status.RuntimeState, status.OperationID, status.ErrorCode = run.state, run.id, run.errorCode
		controller.mu.Unlock()
	}
	controller.publish(run.id, status)
}

func (controller *SessionController) handleRuntimeEvent(run *sessionRun, event SessionRuntimeEvent) {
	if event.State == RuntimeConnected {
		accountID := strings.TrimSpace(event.WhatsAppAccountID)
		if accountID == "" {
			accountID = run.runtime.WhatsAppAccountID()
		}
		if accountID == "" {
			controller.failRun(run, agent.ErrorIntegrityFailure)
			return
		}
		if err := controller.bindings.MarkSessionPaired(context.Background(), run.scope, accountID); err != nil {
			controller.failRun(run, agent.CodeOf(err))
			return
		}
	}
	if event.State == RuntimeRevoked {
		if err := controller.bindings.MarkSessionRevoked(context.Background(), run.scope); err != nil {
			controller.failRun(run, agent.CodeOf(err))
			return
		}
	}
	controller.mu.Lock()
	if controller.run != run {
		controller.mu.Unlock()
		return
	}
	if event.State == RuntimeConnected {
		run.paired = true
		run.request.Mode = SessionRunResume
	}
	if event.State == RuntimeRevoked {
		run.paired = false
	}
	run.state = event.State
	if event.Pairing != nil {
		pairing := *event.Pairing
		if pairing.Generation == 0 {
			if run.pairing == nil {
				pairing.Generation = 1
			} else {
				pairing.Generation = run.pairing.Generation + 1
			}
		}
		run.pairing = &pairing
	} else if event.State == RuntimeConnected || event.State == RuntimeStopping || event.State == RuntimeFailed || event.State == RuntimeRevoked {
		run.pairing = nil
	}
	if event.ErrorCode != "" {
		run.errorCode = event.ErrorCode
	}
	status := controller.statusForRunLocked(run, SessionUnpaired)
	if run.paired {
		status.BindingState = SessionPaired
	} else if run.state == RuntimeRevoked {
		status.BindingState = SessionRevoked
	}
	controller.mu.Unlock()
	controller.publish(run.id, status)
}

func (controller *SessionController) failRun(run *sessionRun, code agent.ErrorCode) {
	controller.mu.Lock()
	if controller.run == run {
		run.state = RuntimeFailed
		run.errorCode = code
	}
	controller.mu.Unlock()
	run.cancel()
}

func (controller *SessionController) statusForRun(run *sessionRun) SessionStatus {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	state := SessionUnpaired
	if run.paired {
		state = SessionPaired
	} else if run.state == RuntimeRevoked {
		state = SessionRevoked
	}
	return controller.statusForRunLocked(run, state)
}

func (controller *SessionController) statusForRunLocked(run *sessionRun, state SessionBindingState) SessionStatus {
	return SessionStatus{BindingState: state, RuntimeState: run.state, SessionPresent: run.paired, WhatsAppAccountID: run.runtime.WhatsAppAccountID(), OperationID: run.id, Pairing: clonePairing(run.pairing), ErrorCode: run.errorCode}
}

func (controller *SessionController) restoreAfterFailedLogout(run *sessionRun) {
	if run == nil {
		return
	}
	controller.mu.Lock()
	if controller.run == run {
		run.state = RuntimeConnected
	}
	status := controller.statusForRunLocked(run, SessionPaired)
	controller.mu.Unlock()
	controller.publish(run.id, status)
}

func (controller *SessionController) publish(operationID string, status SessionStatus) {
	if controller.events != nil {
		controller.events.TryPublish(SessionEvent{OperationID: operationID, Status: status})
	}
}

func (controller *SessionController) ensureOpen() error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed || controller.rootCtx.Err() != nil {
		return agent.NewError(agent.ErrorNotReady, "manage WhatsApp session", errors.New("session controller is closing"))
	}
	if controller.run != nil {
		return agent.NewError(agent.ErrorConflict, "manage WhatsApp session", errors.New("another WhatsApp operation is active"))
	}
	return nil
}

func waitSessionRun(ctx context.Context, run *sessionRun) error {
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return agent.NewError(agent.ErrorTimeout, "wait for WhatsApp session shutdown", ctx.Err())
	}
}

func statusFromBinding(binding SessionBinding) SessionStatus {
	state := binding.State
	if state == "" {
		state = SessionUnpaired
	}
	return SessionStatus{BindingState: state, RuntimeState: RuntimeStopped, SessionPresent: state == SessionPaired, WhatsAppAccountID: binding.WhatsAppAccountID}
}

func normalizePairingPhone(value string) (string, error) {
	var digits strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		} else if r != '+' && r != ' ' && r != '-' && r != '(' && r != ')' && r != '.' {
			return "", agent.NewError(agent.ErrorInvalidArgument, "begin WhatsApp pairing", errors.New("phone number must use an international number format"))
		}
	}
	normalized := digits.String()
	if len(normalized) < 7 || len(normalized) > pairingPhoneMaxDigits || strings.HasPrefix(normalized, "0") {
		return "", agent.NewError(agent.ErrorInvalidArgument, "begin WhatsApp pairing", errors.New("phone number must include its country code"))
	}
	return normalized, nil
}

func newSessionOperationID() (string, error) {
	id, err := identity.NewInvocationID()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func clonePairing(pairing *SessionPairing) *SessionPairing {
	if pairing == nil {
		return nil
	}
	clone := *pairing
	return &clone
}

func sessionRepositoryError(operation string, err error) error {
	switch agent.CodeOf(err) {
	case agent.ErrorConflict, agent.ErrorInvalidArgument, agent.ErrorNotFound, agent.ErrorIntegrityFailure:
		return agent.NewError(agent.CodeOf(err), operation, errors.New("session state changed or is invalid"))
	default:
		return agent.NewError(agent.ErrorStorageFailure, operation, errors.New("session storage operation failed"))
	}
}

func sessionFactoryError(operation string, err error) error {
	code := agent.CodeOf(err)
	if code == agent.ErrorInternal {
		code = agent.ErrorUnavailable
	}
	return agent.NewError(code, operation, errors.New("session runtime could not be prepared"))
}
