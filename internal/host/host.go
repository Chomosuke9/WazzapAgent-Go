// Package host starts the settings-driven application shared by the desktop
// app and the web server: the data-root lock, settings, the Discord session,
// the Agent runtime and the UI service on top of them.
package host

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	discordadapter "github.com/Chomosuke9/DiscordAgent-Go/internal/adapters/discord"
	appsqlite "github.com/Chomosuke9/DiscordAgent-Go/internal/adapters/sqlite"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	coreapp "github.com/Chomosuke9/DiscordAgent-Go/internal/app"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/control"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/observability"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/platform"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/ui"
)

const shutdownTimeout = 20 * time.Second

type Host struct {
	Service *ui.AppService
	Logger  *slog.Logger

	lease         *platform.DataRootLease
	settingsStore *appsqlite.SettingsStore
	settings      *control.Controller
	sessions      *control.SessionController
	agents        *control.AgentController
	reader        *appsqlite.ConversationReader

	closeOnce sync.Once
	closeErr  error
	stopStart context.CancelFunc
}

// Open locks the data root and builds everything the UI serves. On error
// nothing is left open.
func Open(ctx context.Context, paths platform.Paths, version string) (_ *Host, resultErr error) {
	host := &Host{stopStart: func() {}}
	var cleanups []func()
	defer func() {
		if resultErr != nil {
			for index := len(cleanups) - 1; index >= 0; index-- {
				cleanups[index]()
			}
		}
	}()
	lease, err := platform.AcquireDataRootLease(ctx, paths.EffectiveDataRoot)
	if err != nil {
		return nil, err
	}
	host.lease = lease
	cleanups = append(cleanups, func() { _ = lease.Close() })

	logs := observability.NewLogBuffer(500)
	consoleLogger, _, err := observability.NewLogger(os.Stderr, "info", "compact")
	if err != nil {
		return nil, err
	}
	host.Logger = slog.New(observability.NewMultiHandler(consoleLogger.Handler(), logs.Handler()))
	slog.SetDefault(host.Logger)
	if err := logs.Persist(filepath.Join(lease.Root(), observability.ProblemLogFile)); err != nil {
		host.Logger.Warn("earlier warnings and errors could not be loaded", "error", err)
	}

	settingsStore, err := appsqlite.OpenSettings(ctx, appsqlite.SettingsPath(lease.Root()))
	if err != nil {
		return nil, err
	}
	host.settingsStore = settingsStore
	cleanups = append(cleanups, func() { _ = settingsStore.Close() })
	repository, err := appsqlite.NewControlSettingsRepository(settingsStore)
	if err != nil {
		return nil, err
	}
	if host.settings, err = control.NewController(repository); err != nil {
		return nil, err
	}
	if err := platform.WriteBootstrapDataRoot(paths.ConfigDir, lease.Root()); err != nil {
		return nil, err
	}
	bindings, err := appsqlite.NewSessionBindingRepository(settingsStore)
	if err != nil {
		return nil, err
	}
	host.sessions, err = control.NewSessionController(repository, bindings, platform.SessionScopeResolver{}, &discordadapter.SessionFactory{Logger: host.Logger}, ui.SessionLog{Logs: logs}, lease.Root())
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, func() { _ = host.sessions.Close(context.Background()) })
	host.agents, err = control.NewAgentController(repository, bindings, host.sessions, coreapp.ManagedAgentRuntimeFactory{Logger: host.Logger}, lease.Root())
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, func() { _ = host.agents.Close(context.Background()) })
	if host.reader, err = appsqlite.NewConversationReader(lease.Root()); err != nil {
		return nil, err
	}
	cleanups = append(cleanups, func() { _ = host.reader.Close() })
	conversations, err := control.NewConversationController(bindings, host.reader)
	if err != nil {
		return nil, err
	}
	host.Service = ui.NewAppService(ui.Options{
		Version:       version,
		Settings:      host.settings,
		Sessions:      host.sessions,
		Agent:         host.agents,
		Conversations: conversations,
		DataRoot:      lease.Root(),
		Logs:          logs,
	})
	return host, nil
}

// StartOnLaunch starts the Agent in the background when the settings ask for
// it. Close stops a start still in progress.
func (host *Host) StartOnLaunch(ctx context.Context) {
	current, err := host.settings.GetSettings(ctx)
	if err != nil {
		host.Logger.Warn("read start-on-launch preference", "code", agent.CodeOf(err), "error", err)
		return
	}
	if !current.Values.Settings.StartOnLaunch {
		return
	}
	startCtx, cancel := context.WithCancel(context.Background())
	host.stopStart = cancel
	go func() {
		if _, err := host.agents.Start(startCtx); err != nil {
			host.Logger.Warn("start Agent on launch", "code", agent.CodeOf(err), "error", err)
		}
	}()
}

// Close stops the Agent and the Discord session, then releases storage and
// the data root. If a runtime does not stop in time, storage and the lock stay
// held until the process exits: closing them under a live runtime is unsafe.
func (host *Host) Close() error {
	host.closeOnce.Do(func() {
		host.stopStart()
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := host.agents.Close(ctx); err != nil {
			host.closeErr = err
			return
		}
		if err := host.sessions.Close(ctx); err != nil {
			host.closeErr = err
			return
		}
		host.closeErr = errors.Join(
			host.reader.Close(),
			host.settingsStore.Checkpoint(context.Background()),
			host.settingsStore.Close(),
			host.lease.Close(),
		)
	})
	return host.closeErr
}
