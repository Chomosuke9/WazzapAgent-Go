//go:build web

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/frontend"
	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/web"
	whatsapp "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/whatsapp/hypermeow"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	coreapp "github.com/Chomosuke9/WazzapAgent-Go/internal/app"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/platform"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	address := os.Getenv("WAZZAP_WEB_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || !strings.EqualFold(host, "localhost") && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		return errors.New("web UI address must use localhost or a loopback IP")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for web UI: %w", err)
	}
	defer listener.Close()
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		return errors.New("web UI must listen on a loopback address; use an authenticated HTTPS proxy or SSH tunnel for remote access")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	paths, err := platform.ResolvePaths()
	if err != nil {
		return err
	}
	lease, err := platform.AcquireDataRootLease(ctx, paths.EffectiveDataRoot)
	if err != nil {
		return err
	}
	defer lease.Close()
	logs := observability.NewLogBuffer(500)
	consoleLogger, _, err := observability.NewLogger(os.Stderr, "info", "compact")
	if err != nil {
		return err
	}
	logger := slog.New(observability.NewMultiHandler(consoleLogger.Handler(), logs.Handler()))
	slog.SetDefault(logger)
	store, err := appsqlite.OpenSettings(ctx, appsqlite.SettingsPath(lease.Root()))
	if err != nil {
		return err
	}
	defer store.Close()
	repository, err := appsqlite.NewControlSettingsRepository(store)
	if err != nil {
		return err
	}
	settings, err := control.NewController(repository)
	if err != nil {
		return err
	}
	if err := platform.WriteBootstrapDataRoot(paths.ConfigDir, lease.Root()); err != nil {
		return err
	}
	bindings, err := appsqlite.NewSessionBindingRepository(store)
	if err != nil {
		return err
	}
	sessions, err := control.NewSessionController(repository, bindings, platform.SessionScopeResolver{}, whatsapp.NewSessionFactory(), web.SessionEventSink{Logs: logs}, lease.Root())
	if err != nil {
		return err
	}
	agents, err := control.NewAgentController(repository, bindings, sessions, coreapp.ManagedAgentRuntimeFactory{Logger: logger}, lease.Root())
	if err != nil {
		_ = sessions.Close(context.Background())
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := agents.Close(shutdownCtx); err != nil {
			logger.Error("stop Agent runtime", "code", agent.CodeOf(err))
			return
		}
		if err := sessions.Close(shutdownCtx); err != nil {
			logger.Error("stop WhatsApp session", "code", agent.CodeOf(err))
			return
		}
		if err := store.Checkpoint(shutdownCtx); err != nil {
			logger.Error("checkpoint settings database", "error", err)
		}
	}()
	reader, err := appsqlite.NewConversationReader(lease.Root())
	if err != nil {
		return err
	}
	conversations, err := control.NewConversationController(bindings, reader)
	if err != nil {
		return err
	}
	service := web.NewAppService(version, settings, sessions, agents, conversations, lease.Root(), logs)
	assets, err := fs.Sub(frontend.WebAssets, "web-dist")
	if err != nil {
		return err
	}
	handler, err := web.NewHandler(service, assets, os.Getenv("WAZZAP_WEB_PUBLIC_ORIGIN"))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	if startup, err := settings.GetSettings(ctx); err != nil {
		logger.Warn("read start-on-launch preference", "code", agent.CodeOf(err))
	} else if startup.Values.Settings.StartOnLaunch {
		go func() {
			if _, err := agents.Start(ctx); err != nil {
				logger.Warn("start Agent on launch", "code", agent.CodeOf(err))
			}
		}()
	}
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(listener) }()
	logger.Info("web UI ready", "address", listener.Addr().String())
	select {
	case <-ctx.Done():
	case err := <-serveError:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
