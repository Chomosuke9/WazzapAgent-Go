package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
)

type AgentRuntimeState string

const (
	BotStopped  AgentRuntimeState = "stopped"
	BotStarting AgentRuntimeState = "starting"
	BotRunning  AgentRuntimeState = "running"
	BotStopping AgentRuntimeState = "stopping"
	BotFailed   AgentRuntimeState = "failed"
)

// AgentRuntimeSnapshot contains only safe process state. It never crosses the
// UI boundary with a settings snapshot, prompt, or credential.
type AgentRuntimeSnapshot struct {
	Started           bool
	WhatsAppState     string
	WhatsAppErrorCode agent.ErrorCode
}

type AgentRuntimeStatus struct {
	State          AgentRuntimeState
	SavedRevision  uint64
	ActiveRevision uint64
	PendingChanges bool
	WhatsAppState  string
	ErrorCode      agent.ErrorCode
	OperationID    string
}

type agentRuntimeRun struct {
	runtime         ManagedAgentRuntime
	cancel          context.CancelFunc
	done            chan struct{}
	revision        uint64
	operationID     string
	shutdownTimeout time.Duration
	err             error // guarded by AgentController.mu; read after done closes
}

// AgentController owns the bot runtime for one leased data root. Pairing is
// still performed by SessionController; starting bot mode first stops that
// session-only client so only one WhatsApp client owns the device store.
type AgentController struct {
	settings SettingsRepository
	bindings SessionBindingRepository
	sessions AgentSessionControl
	factory  AgentRuntimeFactory
	dataRoot string

	rootCtx     context.Context
	rootCancel  context.CancelFunc
	operations  sync.Mutex
	mu          sync.RWMutex
	run         *agentRuntimeRun
	state       AgentRuntimeState
	activeRev   uint64
	operationID string
	lastError   agent.ErrorCode
	closed      bool
}

func NewAgentController(
	settings SettingsRepository,
	bindings SessionBindingRepository,
	sessions AgentSessionControl,
	factory AgentRuntimeFactory,
	dataRoot string,
) (*AgentController, error) {
	if settings == nil || bindings == nil || sessions == nil || factory == nil || strings.TrimSpace(dataRoot) == "" {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create Agent controller", errors.New("settings, session binding, session controller, runtime factory, and data root are required"))
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	return &AgentController{
		settings: settings, bindings: bindings, sessions: sessions, factory: factory,
		dataRoot: dataRoot, rootCtx: rootCtx, rootCancel: rootCancel, state: BotStopped,
	}, nil
}

func (controller *AgentController) GetStatus(ctx context.Context) (AgentRuntimeStatus, error) {
	if controller == nil || controller.settings == nil {
		return AgentRuntimeStatus{}, agent.NewError(agent.ErrorUnavailable, "get Agent status", errors.New("Agent controller is unavailable"))
	}
	ctx = nonNilContext(ctx)
	saved, err := controller.settings.Load(ctx)
	if err != nil {
		return AgentRuntimeStatus{}, safeRepositoryError("load settings for Agent status", err)
	}

	controller.mu.RLock()
	state, activeRevision, operationID, lastError, run := controller.state, controller.activeRev, controller.operationID, controller.lastError, controller.run
	controller.mu.RUnlock()

	status := AgentRuntimeStatus{
		State: state, SavedRevision: saved.Revision, ActiveRevision: activeRevision,
		PendingChanges: activeRevision != 0 && activeRevision != saved.Revision,
		WhatsAppState:  string(RuntimeStopped), ErrorCode: lastError, OperationID: operationID,
	}
	if run != nil {
		runtimeStatus := run.runtime.Snapshot()
		if runtimeStatus.WhatsAppState != "" {
			status.WhatsAppState = runtimeStatus.WhatsAppState
		}
		if runtimeStatus.WhatsAppErrorCode != "" {
			status.ErrorCode = runtimeStatus.WhatsAppErrorCode
		}
	} else if sessionStatus, sessionErr := controller.sessions.GetStatus(ctx); sessionErr == nil {
		status.WhatsAppState = string(sessionStatus.RuntimeState)
		if sessionStatus.ErrorCode != "" {
			status.ErrorCode = sessionStatus.ErrorCode
		}
	}
	return status, nil
}

func (controller *AgentController) Start(ctx context.Context) (AgentRuntimeStatus, error) {
	ctx = nonNilContext(ctx)
	controller.operations.Lock()
	defer controller.operations.Unlock()

	snapshot, revision, err := controller.runtimeSnapshot(ctx, nil)
	if err != nil {
		return AgentRuntimeStatus{}, err
	}
	controller.mu.RLock()
	run, state, closed := controller.run, controller.state, controller.closed
	controller.mu.RUnlock()
	if closed {
		return AgentRuntimeStatus{}, agent.NewError(agent.ErrorNotReady, "start Agent", errors.New("Agent controller is closing"))
	}
	if run != nil {
		if state == BotRunning && run.revision == revision {
			return controller.GetStatus(ctx)
		}
		return AgentRuntimeStatus{}, agent.NewError(agent.ErrorConflict, "start Agent", errors.New("Agent is already active; apply the saved settings to restart it"))
	}
	return controller.launch(ctx, snapshot, revision)
}

// ApplySettings restarts a running Agent with the selected saved revision. It
// deliberately does not start a stopped Agent; starting remains explicit via
// the UI action or the user's saved start-on-launch preference.
func (controller *AgentController) ApplySettings(ctx context.Context, expectedRevision uint64) (AgentRuntimeStatus, error) {
	ctx = nonNilContext(ctx)
	controller.operations.Lock()
	defer controller.operations.Unlock()

	settingsSnapshot, err := controller.settings.Load(ctx)
	if err != nil {
		return AgentRuntimeStatus{}, safeRepositoryError("load settings for Agent apply", err)
	}
	if settingsSnapshot.Revision != expectedRevision {
		return AgentRuntimeStatus{}, agent.NewError(agent.ErrorConflict, "apply Agent settings", errors.New("settings revision changed; reload before applying"))
	}
	controller.mu.RLock()
	run, state, closed := controller.run, controller.state, controller.closed
	controller.mu.RUnlock()
	if closed {
		return AgentRuntimeStatus{}, agent.NewError(agent.ErrorNotReady, "apply Agent settings", errors.New("Agent controller is closing"))
	}
	if !settingsSnapshot.Values.AgentEnabled {
		if run != nil && state == BotRunning {
			if err := controller.stopCurrent(ctx); err != nil {
				return AgentRuntimeStatus{}, err
			}
		}
		return controller.GetStatus(ctx)
	}

	snapshot, revision, err := controller.runtimeSnapshot(ctx, &expectedRevision)
	if err != nil {
		return AgentRuntimeStatus{}, err
	}
	controller.mu.RLock()
	run, state = controller.run, controller.state
	controller.mu.RUnlock()
	if run == nil || state != BotRunning {
		return controller.GetStatus(ctx)
	}
	if err := controller.stopCurrent(ctx); err != nil {
		return AgentRuntimeStatus{}, err
	}
	return controller.launch(ctx, snapshot, revision)
}

func (controller *AgentController) Stop(ctx context.Context) (AgentRuntimeStatus, error) {
	ctx = nonNilContext(ctx)
	controller.operations.Lock()
	defer controller.operations.Unlock()
	if err := controller.stopCurrent(ctx); err != nil {
		return AgentRuntimeStatus{}, err
	}
	return controller.GetStatus(ctx)
}

// IsActive is used by the Wails adapter to prevent session-only controls from
// operating against a device store currently owned by bot mode.
func (controller *AgentController) IsActive() bool {
	if controller == nil {
		return false
	}
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	return controller.run != nil && (controller.state == BotStarting || controller.state == BotRunning || controller.state == BotStopping)
}

// WithSessionControl serializes session-only mutations with Agent startup and
// rejects them while the bot owns the WhatsApp client.
func (controller *AgentController) WithSessionControl(action func() error) error {
	if controller == nil || action == nil {
		return agent.NewError(agent.ErrorUnavailable, "change WhatsApp session", errors.New("Agent controller is unavailable"))
	}
	controller.operations.Lock()
	defer controller.operations.Unlock()
	controller.mu.RLock()
	closed := controller.closed
	active := controller.run != nil && (controller.state == BotStarting || controller.state == BotRunning || controller.state == BotStopping)
	controller.mu.RUnlock()
	if closed {
		return agent.NewError(agent.ErrorNotReady, "change WhatsApp session", errors.New("Agent controller is closing"))
	}
	if active {
		return agent.NewError(agent.ErrorConflict, "change WhatsApp session", errors.New("stop the Agent before changing its WhatsApp session"))
	}
	return action()
}

func (controller *AgentController) Close(ctx context.Context) error {
	if controller == nil {
		return nil
	}
	ctx = nonNilContext(ctx)
	controller.operations.Lock()
	defer controller.operations.Unlock()
	controller.mu.Lock()
	controller.closed = true
	controller.rootCancel()
	controller.mu.Unlock()
	return controller.stopCurrent(ctx)
}

func (controller *AgentController) runtimeSnapshot(ctx context.Context, expectedRevision *uint64) (config.Snapshot, uint64, error) {
	settingsSnapshot, err := controller.settings.Load(ctx)
	if err != nil {
		return config.Snapshot{}, 0, safeRepositoryError("load settings for Agent", err)
	}
	if expectedRevision != nil && settingsSnapshot.Revision != *expectedRevision {
		return config.Snapshot{}, 0, agent.NewError(agent.ErrorConflict, "apply Agent settings", errors.New("settings revision changed; reload before applying"))
	}
	settings := settingsSnapshot.Values
	if !settings.AgentEnabled {
		return config.Snapshot{}, 0, agent.NewError(agent.ErrorNotReady, "start Agent", errors.New("enable Agent mode in settings before starting the runtime"))
	}
	if err := config.ValidateAgent(settings); err != nil {
		return config.Snapshot{}, 0, validationError(err)
	}
	binding, err := controller.bindings.LoadSessionBinding(ctx)
	if err != nil {
		return config.Snapshot{}, 0, sessionRepositoryError("load WhatsApp binding for Agent", err)
	}
	if binding.State != SessionPaired || !binding.HasActiveScope {
		return config.Snapshot{}, 0, agent.NewError(agent.ErrorNotReady, "start Agent", errors.New("pair a WhatsApp account before starting the Agent"))
	}
	settings.DataDir = controller.dataRoot
	settings.TenantID = binding.ActiveScope.TenantID
	settings.AccountID = binding.ActiveScope.AccountID
	snapshot, err := config.SnapshotFromSettings(settings)
	if err != nil {
		return config.Snapshot{}, 0, validationError(err)
	}
	return snapshot, settingsSnapshot.Revision, nil
}

func (controller *AgentController) launch(ctx context.Context, snapshot config.Snapshot, revision uint64) (AgentRuntimeStatus, error) {
	if _, err := controller.sessions.Stop(ctx); err != nil {
		return AgentRuntimeStatus{}, err
	}
	operationID, err := newSessionOperationID()
	if err != nil {
		return AgentRuntimeStatus{}, agent.NewError(agent.ErrorInternal, "create Agent operation", errors.New("could not allocate an operation identifier"))
	}
	runtime, err := controller.factory.OpenAgentRuntime(ctx, snapshot)
	if err != nil {
		return AgentRuntimeStatus{}, safeAgentRuntimeError("create Agent runtime", err)
	}
	if runtime == nil {
		return AgentRuntimeStatus{}, agent.NewError(agent.ErrorInternal, "create Agent runtime", errors.New("runtime factory returned no runtime"))
	}

	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		closeCtx, cancel := context.WithTimeout(context.Background(), snapshot.ShutdownTimeout())
		_ = runtime.Close(closeCtx)
		cancel()
		return AgentRuntimeStatus{}, agent.NewError(agent.ErrorNotReady, "start Agent", errors.New("Agent controller is closing"))
	}
	runCtx, cancel := context.WithCancel(controller.rootCtx)
	shutdownTimeout := snapshot.ShutdownTimeout()
	if shutdownTimeout <= 0 {
		shutdownTimeout = 10 * time.Second
	}
	run := &agentRuntimeRun{runtime: runtime, cancel: cancel, done: make(chan struct{}), revision: revision, operationID: operationID, shutdownTimeout: shutdownTimeout}
	controller.run = run
	controller.state = BotStarting
	controller.activeRev = 0
	controller.operationID = operationID
	controller.lastError = ""
	controller.mu.Unlock()

	go controller.runAgent(runCtx, run)
	startupTimeout := snapshot.ConnectTimeout() + 5*time.Second
	if startupTimeout < 5*time.Second {
		startupTimeout = 5 * time.Second
	}
	timer := time.NewTimer(startupTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if runtime.Snapshot().Started {
			controller.mu.Lock()
			if controller.run == run && controller.state == BotStarting {
				controller.state = BotRunning
				controller.activeRev = revision
			}
			controller.mu.Unlock()
			return controller.GetStatus(ctx)
		}
		select {
		case <-run.done:
			runErr := run.err
			if runErr == nil {
				runErr = errors.New("runtime stopped before becoming ready")
			}
			return AgentRuntimeStatus{}, safeAgentRuntimeError("start Agent runtime", runErr)
		case <-ctx.Done():
			_ = controller.abortStarting(run, snapshot.ShutdownTimeout())
			return AgentRuntimeStatus{}, agent.NewError(agent.ErrorCancelled, "start Agent runtime", errors.New("startup was cancelled"))
		case <-timer.C:
			_ = controller.abortStarting(run, snapshot.ShutdownTimeout())
			return AgentRuntimeStatus{}, agent.NewError(agent.ErrorTimeout, "start Agent runtime", errors.New("runtime did not finish startup before the configured timeout"))
		case <-ticker.C:
		}
	}
}

func (controller *AgentController) runAgent(ctx context.Context, run *agentRuntimeRun) {
	err := run.runtime.Run(ctx)
	controller.mu.Lock()
	run.err = err
	if controller.run == run {
		stopping := controller.state == BotStopping || controller.closed
		controller.run = nil
		controller.activeRev = 0
		if stopping && (err == nil || errors.Is(err, context.Canceled)) {
			controller.state = BotStopped
			controller.lastError = ""
		} else if err == nil {
			controller.state = BotFailed
			controller.lastError = agent.ErrorUnavailable
		} else {
			controller.state = BotFailed
			controller.lastError = agent.CodeOf(err)
		}
	}
	close(run.done)
	controller.mu.Unlock()
}

func (controller *AgentController) abortStarting(run *agentRuntimeRun, timeout time.Duration) error {
	run.cancel()
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	closeErr := run.runtime.Close(closeCtx)
	select {
	case <-run.done:
		return closeErr
	case <-closeCtx.Done():
		return errors.Join(closeErr, closeCtx.Err())
	}
}

func (controller *AgentController) stopCurrent(ctx context.Context) error {
	controller.mu.Lock()
	run := controller.run
	if run == nil {
		if controller.state != BotFailed {
			controller.state = BotStopped
			controller.activeRev = 0
		}
		controller.mu.Unlock()
		return nil
	}
	controller.state = BotStopping
	controller.activeRev = 0
	controller.mu.Unlock()
	run.cancel()
	closeCtx := ctx
	cancel := func() {}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		closeCtx, cancel = context.WithTimeout(ctx, run.shutdownTimeout)
	}
	defer cancel()
	if err := run.runtime.Close(closeCtx); err != nil {
		return safeAgentRuntimeError("stop Agent runtime", err)
	}
	select {
	case <-run.done:
		if run.err != nil && !errors.Is(run.err, context.Canceled) {
			return safeAgentRuntimeError("stop Agent runtime", run.err)
		}
		return nil
	case <-closeCtx.Done():
		return agent.NewError(agent.ErrorTimeout, "stop Agent runtime", errors.New("runtime is still shutting down"))
	}
}

func (controller *AgentController) statusFromSnapshot(ctx context.Context) (AgentRuntimeStatus, error) {
	return controller.GetStatus(ctx)
}

func safeAgentRuntimeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	code := agent.CodeOf(err)
	if code == agent.ErrorInternal {
		code = agent.ErrorUnavailable
	}
	return agent.NewError(code, operation, errors.New("Agent runtime operation failed; inspect the application status for the safe error code"))
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
