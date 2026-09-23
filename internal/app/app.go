package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/account"
	whatsapp "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/whatsapp/hypermeow"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
)

// Options contains process-owned dependencies which are intentionally kept out
// of config.Snapshot. GUI callers can provide a pairing sink without making
// the core runtime write to a terminal.
type Options struct {
	SystemPolicy string
	Pairing      whatsapp.PairingSink
}

type runtimeHandle interface {
	run(context.Context) error
	close(context.Context) error
}

type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleRunning
	lifecycleDone
	lifecycleClosed
)

type Application struct {
	config  config.Snapshot
	logger  *slog.Logger
	options Options

	ready        atomic.Bool
	accountState atomic.Pointer[account.Runtime]
	adapterState atomic.Pointer[whatsapp.Adapter]
	runtimeState atomic.Pointer[conversationRuntime]
	metrics      *observability.Metrics

	mu             sync.Mutex
	state          lifecycleState
	runCancel      context.CancelFunc
	runDone        chan struct{}
	runStopping    <-chan struct{}
	runErr         error
	runtimeFactory func(context.Context) (runtimeHandle, error)
}

// New creates a single-use application owner. A stopped application cannot be
// restarted; create a fresh Application for a subsequent run.
func New(cfg config.Snapshot, logger *slog.Logger, options Options) *Application {
	if logger == nil {
		logger = slog.Default()
	}
	application := &Application{
		config:  cfg,
		logger:  logger,
		options: options,
		metrics: observability.NewMetrics(),
		state:   lifecycleNew,
	}
	application.runtimeFactory = func(ctx context.Context) (runtimeHandle, error) {
		return application.composeRuntime(ctx)
	}
	return application
}

// Run owns only the core runtime. It never binds the optional diagnostics HTTP
// listener, even when WAZZAP_HTTP_ADDRESS is configured.
func (application *Application) Run(ctx context.Context) error {
	return application.run(ctx, false, nil)
}

// RunCLI owns the diagnostics HTTP listener and the same core runtime used by
// Run. GUI callers should use Run and obtain status through their controller.
func (application *Application) RunCLI(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- application.run(ctx, true, started) }()
	select {
	case err := <-result:
		return err
	case <-started:
	}
	application.mu.Lock()
	stopping := application.runStopping
	application.mu.Unlock()
	select {
	case err := <-result:
		return err
	case <-stopping:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), application.config.ShutdownTimeout())
		closeErr := application.Close(shutdownCtx)
		cancel()
		if closeErr != nil {
			return closeErr
		}
		return <-result
	}
}

func (application *Application) run(parent context.Context, diagnostics bool, started chan struct{}) (result error) {
	if parent == nil {
		parent = context.Background()
	}
	runCtx, cancel, done, err := application.begin(parent)
	if err != nil {
		return err
	}
	if started != nil {
		close(started)
	}
	defer func() { application.finish(done, result) }()
	defer cancel()

	if err := prepareDataDir(application.config.DataDir()); err != nil {
		return fmt.Errorf("prepare data directory: %w", err)
	}

	var runtime runtimeHandle
	if application.config.WhatsAppEnabled() {
		runtime, err = application.runtimeFactory(runCtx)
		if err != nil {
			return fmt.Errorf("compose conversation runtime: %w", err)
		}
		application.setRuntime(runtime)
		defer func() {
			if err := runtime.close(context.Background()); err != nil {
				result = errors.Join(result, fmt.Errorf("close conversation runtime: %w", err))
			}
		}()
	}
	if runCtx.Err() != nil {
		return nil
	}

	var server *http.Server
	var listener net.Listener
	serverErrors := make(chan error, 1)
	if diagnostics {
		listener, err = net.Listen("tcp", application.config.HTTPAddress())
		if err != nil {
			return fmt.Errorf("listen on %s: %w", application.config.HTTPAddress(), err)
		}
		server = &http.Server{
			Handler:           application.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() { serverErrors <- server.Serve(listener) }()
	}
	if runCtx.Err() != nil {
		if server != nil {
			_ = server.Close()
		}
		return nil
	}

	application.ready.Store(true)
	if listener != nil {
		application.logger.Info("application started", "address", listener.Addr().String(), "config", application.config.Redacted())
	} else {
		application.logger.Info("application started", "config", application.config.Redacted())
	}
	runtimeErrors := make(chan error, 1)
	if runtime != nil {
		go func() { runtimeErrors <- runtime.run(runCtx) }()
	}

	var runtimeErr error
	runtimeFinished := false
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve HTTP: %w", err)
		}
	case err := <-runtimeErrors:
		runtimeErr = err
		runtimeFinished = true
		if err != nil {
			result = fmt.Errorf("run conversation runtime: %w", err)
		}
	case <-runCtx.Done():
	}

	// Cancellation is shared by the CLI server and runtime. Resource cleanup
	// is deliberately allowed to finish after a caller's Close timeout;
	// this prevents a late worker from using a database which was already closed.
	application.ready.Store(false)
	cancel()
	if server != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), application.config.ShutdownTimeout())
		shutdownErr := server.Shutdown(shutdownCtx)
		shutdownCancel()
		if shutdownErr != nil {
			_ = server.Close()
			result = errors.Join(result, fmt.Errorf("shutdown HTTP: %w", shutdownErr))
		}
	}
	if runtime != nil && !runtimeFinished {
		runtimeErr = <-runtimeErrors
		if runtimeErr != nil {
			result = errors.Join(result, fmt.Errorf("stop conversation runtime: %w", runtimeErr))
		}
	}
	return result
}

func (application *Application) begin(parent context.Context) (context.Context, context.CancelFunc, chan struct{}, error) {
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.state != lifecycleNew {
		return nil, nil, nil, agent.NewError(agent.ErrorConflict, "start application", errors.New("application is single-use and has already been started or closed"))
	}
	runCtx, cancel := context.WithCancel(parent)
	application.state = lifecycleRunning
	application.runCancel = cancel
	application.runDone = make(chan struct{})
	application.runStopping = runCtx.Done()
	return runCtx, cancel, application.runDone, nil
}

func (application *Application) setRuntime(runtime runtimeHandle) {
	if concrete, ok := runtime.(*conversationRuntime); ok {
		application.accountState.Store(concrete.account)
		application.adapterState.Store(concrete.adapter)
		application.runtimeState.Store(concrete)
	}
}

func (application *Application) finish(done chan struct{}, runErr error) {
	application.ready.Store(false)
	application.accountState.Store(nil)
	application.adapterState.Store(nil)
	application.runtimeState.Store(nil)
	application.mu.Lock()
	application.runCancel = nil
	application.runErr = runErr
	application.state = lifecycleDone
	close(done)
	application.mu.Unlock()
	application.logger.Info("application stopped")
}

// Close requests shutdown and waits for the complete lifecycle, including
// worker termination and resource cleanup. If ctx expires, ownership remains
// with this application and a later Close call may wait again.
func (application *Application) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	application.mu.Lock()
	switch application.state {
	case lifecycleNew:
		application.state = lifecycleClosed
		application.mu.Unlock()
		return nil
	case lifecycleDone:
		err := application.runErr
		application.mu.Unlock()
		return err
	case lifecycleClosed:
		application.mu.Unlock()
		return nil
	case lifecycleRunning:
		application.ready.Store(false)
		cancel := application.runCancel
		done := application.runDone
		application.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		select {
		case <-done:
			application.mu.Lock()
			err := application.runErr
			application.mu.Unlock()
			return err
		case <-ctx.Done():
			return fmt.Errorf("close application: %w", ctx.Err())
		}
	default:
		application.mu.Unlock()
		return nil
	}
}
