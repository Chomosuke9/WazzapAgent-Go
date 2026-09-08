package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
)

type Application struct {
	config config.Snapshot
	logger *slog.Logger
	ready  atomic.Bool
}

type healthResponse struct {
	Status string `json:"status"`
}

func New(cfg config.Snapshot, logger *slog.Logger) *Application {
	if logger == nil {
		logger = slog.Default()
	}
	return &Application{config: cfg, logger: logger}
}

func (a *Application) Run(ctx context.Context) error {
	if err := prepareDataDir(a.config.DataDir()); err != nil {
		return fmt.Errorf("prepare data directory: %w", err)
	}

	listener, err := net.Listen("tcp", a.config.HTTPAddress())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.config.HTTPAddress(), err)
	}

	server := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		serverErrors <- server.Serve(listener)
	}()

	a.ready.Store(true)
	a.logger.Info("application started", "address", listener.Addr().String(), "config", a.config.Redacted())

	select {
	case err := <-serverErrors:
		a.ready.Store(false)
		workers.Wait()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		a.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.config.ShutdownTimeout())
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			workers.Wait()
			return fmt.Errorf("shutdown HTTP: %w", err)
		}
		err := <-serverErrors
		workers.Wait()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP during shutdown: %w", err)
		}
		a.logger.Info("application stopped")
		return nil
	}
}

func prepareDataDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("configured data path is not a directory")
	}
	return nil
}

func (a *Application) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(writer http.ResponseWriter, _ *http.Request) {
		writeHealth(writer, http.StatusOK, "live")
	})
	mux.HandleFunc("GET /health/ready", func(writer http.ResponseWriter, _ *http.Request) {
		if !a.ready.Load() {
			writeHealth(writer, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeHealth(writer, http.StatusOK, "ready")
	})
	return mux
}

func (a *Application) Ready() bool {
	return a.ready.Load()
}

func writeHealth(writer http.ResponseWriter, status int, value string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(healthResponse{Status: value})
}
