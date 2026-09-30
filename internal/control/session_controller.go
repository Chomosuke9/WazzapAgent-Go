package control

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/config"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

const maxBotTokenBytes = 256

type sessionRun struct {
	id      string
	scope   SessionScope
	request SessionRunRequest
	runtime ManagedSession
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	state     SessionRuntimeState
	linked    bool
	botName   string
	errorCode agent.ErrorCode
}

// SessionController owns the session-only Discord lifecycle. It never
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
		status.BotName = run.botName
		status.ErrorCode = run.errorCode
		if run.linked {
			status.SessionPresent = true
			status.BindingState = SessionLinked
		}
	}
	return status, nil
}

// BeginLink links a Discord bot by its token. The token is verified by
// connecting with it, and saved in the account scope only once Discord
// accepted it.
func (controller *SessionController) BeginLink(ctx context.Context, request BeginLinkRequest) (SessionOperation, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	token, err := normalizeBotToken(request.Token)
	if err != nil {
		return SessionOperation{}, err
	}
	if err := controller.ensureOpen(); err != nil {
		return SessionOperation{}, err
	}
	binding, err := controller.bindings.LoadSessionBinding(ctx)
	if err != nil {
		return SessionOperation{}, sessionRepositoryError("load session binding", err)
	}
	if binding.State == SessionLinked {
		return SessionOperation{}, agent.NewError(agent.ErrorConflict, "begin Discord link", errors.New("a Discord bot is already linked; resume it or unlink it first"))
	}
	preferred, hasPreferred := binding.ActiveScope, binding.HasActiveScope
	if binding.HasPendingScope {
		// A process may stop while a link reservation is durable. Re-open
		// that exact scope before deciding whether it can be resumed.
		snapshot, scope, err := controller.sessionSnapshot(ctx, binding.PendingScope, true)
		if err != nil {
			return SessionOperation{}, err
		}
		managed, err := controller.factory.OpenSession(ctx, snapshot)
		if err != nil {
			return SessionOperation{}, sessionFactoryError("recover Discord link", err)
		}
		if managed.HasSession() {
			botID := managed.DiscordBotID()
			if botID == "" || controller.bindings.MarkSessionLinked(ctx, scope, botID) != nil {
				_ = managed.Close(context.Background())
				return SessionOperation{}, agent.NewError(agent.ErrorIntegrityFailure, "recover Discord link", errors.New("linked Discord bot could not be recorded"))
			}
			_ = managed.Close(context.Background())
			return SessionOperation{}, agent.NewError(agent.ErrorConflict, "begin Discord link", errors.New("the interrupted link already saved a bot; resume it"))
		}
		_ = managed.Close(context.Background())
		if err := controller.bindings.AbortSessionLink(ctx, scope); err != nil {
			return SessionOperation{}, sessionRepositoryError("recover pending Discord link", err)
		}
	}
	// Relinking the bot that was unlinked keeps its history; any other bot
	// gets a fresh scope so two bots' data never mix.
	if binding.State == SessionRevoked && binding.HasActiveScope && (binding.DiscordBotID == "" || BotIDFromToken(token) != binding.DiscordBotID) {
		newTenantID, tenantErr := identity.NewTenantID()
		if tenantErr != nil {
			return SessionOperation{}, agent.NewError(agent.ErrorInternal, "create Discord tenant scope", tenantErr)
		}
		newAccountID, idErr := identity.NewAccountID()
		if idErr != nil {
			return SessionOperation{}, agent.NewError(agent.ErrorInternal, "create Discord account scope", idErr)
		}
		preferred = SessionScope{TenantID: newTenantID, AccountID: newAccountID}
		hasPreferred = true
	}
	snapshot, scope, err := controller.sessionSnapshot(ctx, preferred, hasPreferred)
	if err != nil {
		return SessionOperation{}, err
	}
	if err := controller.bindings.BeginSessionLink(ctx, scope); err != nil {
		return SessionOperation{}, sessionRepositoryError("reserve session link", err)
	}
	managed, err := controller.factory.OpenSession(ctx, snapshot)
	if err != nil {
		_ = controller.bindings.AbortSessionLink(context.Background(), scope)
		return SessionOperation{}, sessionFactoryError("open Discord session", err)
	}
	if managed.HasSession() && binding.State != SessionRevoked {
		botID := managed.DiscordBotID()
		if botID != "" {
			_ = controller.bindings.MarkSessionLinked(ctx, scope, botID)
		} else {
			_ = controller.bindings.AbortSessionLink(context.Background(), scope)
		}
		_ = managed.Close(context.Background())
		return SessionOperation{}, agent.NewError(agent.ErrorConflict, "begin Discord link", errors.New("a bot token is already saved for this account; refresh status and resume it"))
	}
	run, err := controller.newRun(scope, managed, SessionRunRequest{Mode: SessionRunLink, Token: token})
	if err != nil {
		_ = controller.bindings.AbortSessionLink(context.Background(), scope)
		_ = managed.Close(context.Background())
		return SessionOperation{}, err
	}
	if !controller.launch(run) {
		_ = controller.bindings.AbortSessionLink(context.Background(), scope)
		_ = managed.Close(context.Background())
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "begin Discord link", errors.New("session controller is closing"))
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
	if binding.State != SessionLinked || !binding.HasActiveScope {
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "resume Discord session", errors.New("there is no linked Discord bot to resume"))
	}
	snapshot, scope, err := controller.sessionSnapshot(ctx, binding.ActiveScope, true)
	if err != nil {
		return SessionOperation{}, err
	}
	managed, err := controller.factory.OpenSession(ctx, snapshot)
	if err != nil {
		return SessionOperation{}, sessionFactoryError("open Discord session", err)
	}
	if !managed.HasSession() {
		_ = managed.Close(context.Background())
		if err := controller.bindings.MarkSessionRevoked(ctx, binding.ActiveScope); err != nil {
			return SessionOperation{}, sessionRepositoryError("mark missing Discord token", err)
		}
		return SessionOperation{}, agent.NewError(agent.ErrorIntegrityFailure, "resume Discord session", errors.New("the saved bot token is missing"))
	}
	run, err := controller.newRun(scope, managed, SessionRunRequest{Mode: SessionRunResume})
	if err != nil {
		_ = managed.Close(context.Background())
		return SessionOperation{}, err
	}
	run.linked = true
	if !controller.launch(run) {
		_ = managed.Close(context.Background())
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "resume Discord session", errors.New("session controller is closing"))
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
	if run.request.Mode == SessionRunLink && !run.linked {
		controller.mu.Unlock()
		return SessionStatus{}, agent.NewError(agent.ErrorConflict, "stop Discord session", errors.New("cancel linking to discard the pending link request"))
	}
	run.state = RuntimeStopping
	run.cancel()
	controller.mu.Unlock()
	if err := waitSessionRun(ctx, run); err != nil {
		return SessionStatus{}, err
	}
	return controller.GetStatus(ctx)
}

func (controller *SessionController) CancelLink(ctx context.Context, operationID string) (SessionStatus, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	controller.mu.Lock()
	run := controller.run
	if run == nil || run.id != operationID || run.request.Mode != SessionRunLink || run.linked {
		controller.mu.Unlock()
		return SessionStatus{}, agent.NewError(agent.ErrorConflict, "cancel Discord link", errors.New("link operation is no longer active"))
	}
	run.state = RuntimeStopping
	run.cancel()
	controller.mu.Unlock()
	if err := waitSessionRun(ctx, run); err != nil {
		return SessionStatus{}, err
	}
	if err := controller.bindings.AbortSessionLink(ctx, run.scope); err != nil {
		return SessionStatus{}, sessionRepositoryError("clear pending link", err)
	}
	return controller.GetStatus(ctx)
}

func (controller *SessionController) Reconnect(ctx context.Context) (SessionOperation, error) {
	controller.operations.Lock()
	defer controller.operations.Unlock()
	controller.mu.Lock()
	run := controller.run
	if run == nil || !run.linked {
		controller.mu.Unlock()
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "reconnect Discord session", errors.New("no linked session is running"))
	}
	if err := run.runtime.Reconnect(); err != nil {
		controller.mu.Unlock()
		return SessionOperation{}, agent.NewError(agent.ErrorProviderFailure, "reconnect Discord session", err)
	}
	run.state = RuntimeReconnecting
	status := controller.statusForRunLocked(run, SessionLinked)
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
	if binding.State != SessionLinked || !binding.HasActiveScope {
		return SessionOperation{}, agent.NewError(agent.ErrorNotReady, "unlink Discord bot", errors.New("there is no linked Discord bot to unlink"))
	}
	controller.mu.Lock()
	run := controller.run
	if run != nil && (!run.linked || run.scope != binding.ActiveScope) {
		controller.mu.Unlock()
		return SessionOperation{}, agent.NewError(agent.ErrorConflict, "unlink Discord bot", errors.New("a different Discord operation is active"))
	}
	if run != nil {
		run.state = RuntimeLoggingOut
	}
	controller.mu.Unlock()

	operationID, err := newSessionOperationID()
	if err != nil {
		return SessionOperation{}, agent.NewError(agent.ErrorInternal, "create unlink operation", err)
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
			return SessionOperation{}, sessionFactoryError("open Discord session to unlink", err)
		}
		if !temporary.HasSession() {
			_ = temporary.Close(context.Background())
			return SessionOperation{}, agent.NewError(agent.ErrorIntegrityFailure, "open Discord session to unlink", errors.New("the saved bot token is missing"))
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
		return SessionOperation{}, agent.NewError(agent.ErrorStorageFailure, "unlink Discord bot", errors.New("the saved bot token could not be removed; the link was kept"))
	}
	if err := controller.bindings.MarkSessionRevoked(ctx, binding.ActiveScope); err != nil {
		if temporary != nil {
			_ = temporary.Close(context.Background())
		}
		controller.restoreAfterFailedLogout(run)
		return SessionOperation{}, sessionRepositoryError("persist Discord unlink", err)
	}
	if temporary != nil {
		if closeErr := temporary.Close(context.Background()); closeErr != nil {
			return SessionOperation{}, sessionRepositoryError("close unlinked Discord session", closeErr)
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
		return config.Snapshot{}, SessionScope{}, safeRepositoryError("load settings for Discord session", err)
	}
	settings.Values.DataDir = controller.dataRoot
	preferredScope := SessionScope{}
	if hasPreferred {
		preferredScope = preferred
	}
	snapshot, err := controller.scopes.ResolveSessionSnapshot(ctx, controller.dataRoot, settings.Values, preferredScope)
	if err != nil {
		return config.Snapshot{}, SessionScope{}, agent.NewError(agent.ErrorInvalidArgument, "validate Discord session settings", errors.New("data root or session identity is not ready"))
	}
	scope := SessionScope{TenantID: snapshot.TenantID(), AccountID: snapshot.AccountID()}
	if scope.TenantID.IsZero() || scope.AccountID.IsZero() {
		return config.Snapshot{}, SessionScope{}, agent.NewError(agent.ErrorIntegrityFailure, "resolve Discord session identity", errors.New("session identity is incomplete"))
	}
	return snapshot, scope, nil
}

func (controller *SessionController) newRun(scope SessionScope, managed ManagedSession, request SessionRunRequest) (*sessionRun, error) {
	id, err := newSessionOperationID()
	if err != nil {
		return nil, agent.NewError(agent.ErrorInternal, "create Discord operation", err)
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
	linkMode := run.request.Mode == SessionRunLink
	linked := run.linked
	controller.mu.Unlock()
	if linkMode && !linked {
		_ = controller.bindings.AbortSessionLink(context.Background(), run.scope)
	}
	controller.mu.Lock()
	if controller.run == run {
		if err != nil {
			run.state = RuntimeFailed
			run.errorCode = agent.CodeOf(err)
		} else {
			run.state = RuntimeStopped
		}
		controller.run = nil
	}
	status := SessionStatus{BindingState: SessionUnlinked, RuntimeState: run.state, OperationID: run.id, ErrorCode: run.errorCode}
	if run.linked {
		status.BindingState, status.SessionPresent = SessionLinked, true
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
		botID := strings.TrimSpace(event.DiscordBotID)
		if botID == "" {
			botID = run.runtime.DiscordBotID()
		}
		if botID == "" {
			controller.failRun(run, agent.ErrorIntegrityFailure)
			return
		}
		if err := controller.bindings.MarkSessionLinked(context.Background(), run.scope, botID); err != nil {
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
		run.linked = true
		// The token is saved now, so a later run resumes it.
		run.request = SessionRunRequest{Mode: SessionRunResume}
		if name := strings.TrimSpace(event.BotName); name != "" {
			run.botName = name
		}
	}
	if event.State == RuntimeRevoked {
		run.linked = false
	}
	run.state = event.State
	if event.ErrorCode != "" {
		run.errorCode = event.ErrorCode
	}
	status := controller.statusForRunLocked(run, SessionUnlinked)
	if run.linked {
		status.BindingState = SessionLinked
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
	state := SessionUnlinked
	if run.linked {
		state = SessionLinked
	} else if run.state == RuntimeRevoked {
		state = SessionRevoked
	}
	return controller.statusForRunLocked(run, state)
}

func (controller *SessionController) statusForRunLocked(run *sessionRun, state SessionBindingState) SessionStatus {
	return SessionStatus{BindingState: state, RuntimeState: run.state, SessionPresent: run.linked, DiscordBotID: run.runtime.DiscordBotID(), BotName: run.botName, OperationID: run.id, ErrorCode: run.errorCode}
}

func (controller *SessionController) restoreAfterFailedLogout(run *sessionRun) {
	if run == nil {
		return
	}
	controller.mu.Lock()
	if controller.run == run {
		run.state = RuntimeConnected
	}
	status := controller.statusForRunLocked(run, SessionLinked)
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
		return agent.NewError(agent.ErrorNotReady, "manage Discord session", errors.New("session controller is closing"))
	}
	if controller.run != nil {
		return agent.NewError(agent.ErrorConflict, "manage Discord session", errors.New("another Discord operation is active"))
	}
	return nil
}

func waitSessionRun(ctx context.Context, run *sessionRun) error {
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return agent.NewError(agent.ErrorTimeout, "wait for Discord session shutdown", ctx.Err())
	}
}

func statusFromBinding(binding SessionBinding) SessionStatus {
	state := binding.State
	if state == "" {
		state = SessionUnlinked
	}
	return SessionStatus{BindingState: state, RuntimeState: RuntimeStopped, SessionPresent: state == SessionLinked, DiscordBotID: binding.DiscordBotID}
}

// normalizeBotToken trims a pasted token, and its "Bot " prefix when the
// user copied an Authorization header value.
func normalizeBotToken(value string) (string, error) {
	token := strings.TrimSpace(value)
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bot "))
	if token == "" || len(token) > maxBotTokenBytes {
		return "", agent.NewError(agent.ErrorInvalidArgument, "begin Discord link", errors.New("paste the bot token from the Discord Developer Portal"))
	}
	for _, character := range token {
		if character <= ' ' || character > '~' {
			return "", agent.NewError(agent.ErrorInvalidArgument, "begin Discord link", errors.New("the bot token contains characters a token never has"))
		}
	}
	return token, nil
}

// BotIDFromToken reads the bot's user ID from the token's first segment, which
// Discord encodes as base64. It returns "" when the token does not carry one;
// the ID is only a hint for reusing a scope, never proof of identity.
func BotIDFromToken(token string) string {
	first, _, _ := strings.Cut(token, ".")
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.StdEncoding} {
		decoded, err := encoding.DecodeString(first)
		if err != nil || len(decoded) == 0 || len(decoded) > 20 {
			continue
		}
		valid := true
		for _, digit := range decoded {
			if digit < '0' || digit > '9' {
				valid = false
				break
			}
		}
		if valid {
			return string(decoded)
		}
	}
	return ""
}

func newSessionOperationID() (string, error) {
	id, err := identity.NewInvocationID()
	if err != nil {
		return "", err
	}
	return id.String(), nil
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
