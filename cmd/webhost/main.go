//go:build web

// Command webhost serves the browser UI to other machines, for example from a
// Debian server. Unlike cmd/server it listens on a network address and puts the
// whole UI behind an access token.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/frontend"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/web"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	apphost "github.com/Chomosuke9/WazzapAgent-Go/internal/host"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/platform"
)

var version = "dev"

const (
	tokenFile    = "web-token"
	tokenEnv     = "WAZZAP_WEB_TOKEN"
	defaultAddr  = "0.0.0.0:8080"
	usageSummary = `Usage:
  wazzapagent-web [flags]        serve the web UI
  wazzapagent-web token [-rotate]  print the access token (-rotate makes a new one)

Flags:`
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "token" {
		return tokenCommand(args[1:])
	}
	flags := flag.NewFlagSet("wazzapagent-web", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), usageSummary)
		flags.PrintDefaults()
		fmt.Fprintf(flags.Output(), "\nThe access token comes from $%s, or from the token file (created on first start).\n", tokenEnv)
	}
	address := flags.String("addr", envOr("WAZZAP_WEB_ADDR", defaultAddr), "listen address (env WAZZAP_WEB_ADDR)")
	publicOrigin := flags.String("public-origin", os.Getenv("WAZZAP_WEB_PUBLIC_ORIGIN"), "HTTPS origin when behind a TLS reverse proxy (env WAZZAP_WEB_PUBLIC_ORIGIN)")
	tlsCert := flags.String("tls-cert", os.Getenv("WAZZAP_WEB_TLS_CERT"), "TLS certificate file to serve HTTPS directly (env WAZZAP_WEB_TLS_CERT)")
	tlsKey := flags.String("tls-key", os.Getenv("WAZZAP_WEB_TLS_KEY"), "TLS private key file (env WAZZAP_WEB_TLS_KEY)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("-tls-cert and -tls-key must be given together")
	}

	paths, err := platform.ResolvePaths()
	if err != nil {
		return err
	}
	token, created, err := resolveToken(paths, false)
	if err != nil {
		return err
	}
	auth, err := web.NewTokenAuth(token)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(os.Stderr, "\nAccess token (saved to %s):\n\n    %s\n\nEnter it once in the browser; the login is remembered. Show it again with: wazzapagent-web token\n\n", tokenPath(paths), token)
	}

	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return fmt.Errorf("listen for web UI: %w", err)
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
	handler, err := web.NewHandlerWithOptions(app.Service, assets, web.Options{PublicOrigin: *publicOrigin, Auth: auth})
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	if *tlsCert == "" && *publicOrigin == "" && !isLoopback(listener.Addr()) {
		logger.Warn("serving plain HTTP on a network address; the access token travels unencrypted. Use -tls-cert/-tls-key or a TLS reverse proxy with -public-origin")
	}
	app.StartOnLaunch(ctx)
	serveError := make(chan error, 1)
	go func() {
		if *tlsCert != "" {
			serveError <- server.ServeTLS(listener, *tlsCert, *tlsKey)
			return
		}
		serveError <- server.Serve(listener)
	}()
	logger.Info("web UI ready", "address", listener.Addr().String(), "tls", *tlsCert != "")
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

// tokenCommand prints (or rotates) the access token without starting the app,
// so it also works while the service is running.
func tokenCommand(args []string) error {
	flags := flag.NewFlagSet("token", flag.ContinueOnError)
	rotate := flags.Bool("rotate", false, "replace the token; every browser must sign in again after the service restarts")
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths, err := platform.ResolvePaths()
	if err != nil {
		return err
	}
	if os.Getenv(tokenEnv) != "" {
		return fmt.Errorf("the token is set by $%s; change it there", tokenEnv)
	}
	token, created, err := resolveToken(paths, *rotate)
	if err != nil {
		return err
	}
	fmt.Println(token)
	if created {
		fmt.Fprintf(os.Stderr, "New token saved to %s. Restart the service to apply it.\n", tokenPath(paths))
	}
	return nil
}

func resolveToken(paths platform.Paths, rotate bool) (token string, created bool, err error) {
	if env := os.Getenv(tokenEnv); env != "" {
		return env, false, nil
	}
	return web.LoadToken(tokenPath(paths), true, rotate)
}

func tokenPath(paths platform.Paths) string { return filepath.Join(paths.ConfigDir, tokenFile) }

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func isLoopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}
