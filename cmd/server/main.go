//go:build web

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/frontend"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/web"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	apphost "github.com/Chomosuke9/WazzapAgent-Go/internal/host"
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
	app, err := apphost.Open(ctx, paths, version)
	if err != nil {
		return err
	}
	defer func() {
		if err := app.Close(); err != nil {
			app.Logger.Error("stop application", "code", agent.CodeOf(err), "error", err)
		}
	}()
	logger := app.Logger
	assets, err := fs.Sub(frontend.WebAssets, "web-dist")
	if err != nil {
		return err
	}
	handler, err := web.NewHandler(app.Service, assets, os.Getenv("WAZZAP_WEB_PUBLIC_ORIGIN"))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	app.StartOnLaunch(ctx)
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
