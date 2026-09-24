//go:build gui

package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/frontend"
	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/wails"
	whatsapp "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/whatsapp/hypermeow"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	coreapp "github.com/Chomosuke9/WazzapAgent-Go/internal/app"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// version is overridden by the release build when a version is available.
var version = "dev"

func main() {
	ctx := context.Background()
	paths, err := resolveAppPaths()
	if err != nil {
		log.Fatal(err)
	}
	lease, err := platform.AcquireDataRootLease(ctx, paths.EffectiveDataRoot)
	if err != nil {
		log.Fatal(err)
	}
	logBuffer := observability.NewLogBuffer(500)
	consoleLogger, _, err := observability.NewLogger(os.Stderr, "info", "compact")
	if err != nil {
		_ = lease.Close()
		log.Fatal(err)
	}
	logger := slog.New(observability.NewMultiHandler(consoleLogger.Handler(), logBuffer.Handler()))
	slog.SetDefault(logger)
	settingsStore, err := appsqlite.OpenSettings(ctx, appsqlite.SettingsPath(lease.Root()))
	if err != nil {
		_ = lease.Close()
		log.Fatal(err)
	}
	repository, err := appsqlite.NewControlSettingsRepository(settingsStore)
	if err != nil {
		_ = settingsStore.Close()
		_ = lease.Close()
		log.Fatal(err)
	}
	controller, err := control.NewController(repository)
	if err != nil {
		_ = settingsStore.Close()
		_ = lease.Close()
		log.Fatal(err)
	}
	if err := platform.WriteBootstrapDataRoot(paths.ConfigDir, lease.Root()); err != nil {
		_ = settingsStore.Close()
		_ = lease.Close()
		log.Fatal(err)
	}
	application.RegisterEvent[wails.PingEvent]("app:ping")
	application.RegisterEvent[wails.WhatsAppSessionEventDTO](wails.WhatsAppSessionEventName)

	app := application.New(application.Options{
		Name:        wails.AppName,
		Description: "WazzapAgent desktop application",
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(frontend.Assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	sessionBindings, err := appsqlite.NewSessionBindingRepository(settingsStore)
	if err != nil {
		_ = settingsStore.Close()
		_ = lease.Close()
		log.Fatal(err)
	}
	eventSink := wails.NewSessionEventSink(app, logBuffer)
	sessionController, err := control.NewSessionController(repository, sessionBindings, platform.SessionScopeResolver{}, whatsapp.NewSessionFactory(), eventSink, lease.Root())
	if err != nil {
		_ = settingsStore.Close()
		_ = lease.Close()
		log.Fatal(err)
	}
	agentController, err := control.NewAgentController(repository, sessionBindings, sessionController, coreapp.ManagedAgentRuntimeFactory{Logger: logger}, lease.Root())
	if err != nil {
		_ = sessionController.Close(context.Background())
		_ = settingsStore.Close()
		_ = lease.Close()
		log.Fatal(err)
	}
	conversationReader, err := appsqlite.NewConversationReader(lease.Root())
	if err != nil {
		_ = agentController.Close(context.Background())
		_ = sessionController.Close(context.Background())
		_ = settingsStore.Close()
		_ = lease.Close()
		log.Fatal(err)
	}
	conversationController, err := control.NewConversationController(sessionBindings, conversationReader)
	if err != nil {
		_ = agentController.Close(context.Background())
		_ = sessionController.Close(context.Background())
		_ = settingsStore.Close()
		_ = lease.Close()
		log.Fatal(err)
	}
	var cleanupOnce sync.Once
	var autoStartCancel context.CancelFunc
	cleanup := func() {
		cleanupOnce.Do(func() {
			if autoStartCancel != nil {
				autoStartCancel()
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := agentController.Close(shutdownCtx); err != nil {
				log.Printf("stop Agent runtime: %v", err)
				cancel()
				// Keep storage and the root lease alive until the Agent owner exits.
				return
			}
			if err := sessionController.Close(shutdownCtx); err != nil {
				log.Printf("stop WhatsApp session: %v", err)
				cancel()
				// Keep the data-root lease and SQLite connection alive until process
				// exit; closing storage under a still-running session would be unsafe.
				return
			}
			cancel()
			if err := settingsStore.Checkpoint(context.Background()); err != nil {
				log.Printf("checkpoint settings database: %v", err)
			}
			if err := settingsStore.Close(); err != nil {
				log.Printf("close settings database: %v", err)
			}
			if err := lease.Close(); err != nil {
				log.Printf("release data root: %v", err)
			}
		})
	}
	defer cleanup()

	appService := wails.NewAppServiceWithConversations(app, version, controller, sessionController, lease.Root(), agentController, conversationController, logBuffer)
	app.RegisterService(application.NewService(appService))
	logger.Info("desktop application started", "platform", runtime.GOOS)
	app.OnShutdown(cleanup)
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     wails.AppName,
		URL:       "/",
		Width:     1160,
		Height:    800,
		MinWidth:  360,
		MinHeight: 500,
	})
	if startupSettings, err := controller.GetSettings(ctx); err != nil {
		log.Printf("read Agent start-on-launch preference: %s", agent.CodeOf(err))
	} else if startupSettings.Values.Settings.StartOnLaunch {
		autoStartCtx, cancel := context.WithCancel(context.Background())
		autoStartCancel = cancel
		go func() {
			if _, err := agentController.Start(autoStartCtx); err != nil {
				log.Printf("start Agent on launch: %s", agent.CodeOf(err))
			}
		}()
	}

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
