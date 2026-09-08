package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
)

func TestHealthEndpoints(t *testing.T) {
	application := testApplication(t, map[string]string{})

	assertHealth(t, application.Handler(), "/health/live", http.StatusOK, "live")
	assertHealth(t, application.Handler(), "/health/ready", http.StatusServiceUnavailable, "not_ready")
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResponse := httptest.NewRecorder()
	application.Handler().ServeHTTP(metricsResponse, metricsRequest)
	if metricsResponse.Code != http.StatusOK || !strings.Contains(metricsResponse.Body.String(), "wazzap_model_calls_total 0") {
		t.Fatalf("metrics response = %d/%q", metricsResponse.Code, metricsResponse.Body.String())
	}

	application.ready.Store(true)
	assertHealth(t, application.Handler(), "/health/ready", http.StatusOK, "ready")
}

func TestRunStopsAfterCancellation(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "runtime-data")
	application := testApplication(t, map[string]string{
		"WAZZAP_DATA_DIR":         dataDir,
		"WAZZAP_HTTP_ADDRESS":     "127.0.0.1:0",
		"WAZZAP_SHUTDOWN_TIMEOUT": "2s",
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- application.Run(ctx)
	}()

	deadline := time.After(5 * time.Second)
	for !application.Ready() {
		select {
		case err := <-result:
			t.Fatalf("application stopped before ready: %v", err)
		case <-deadline:
			t.Fatal("application did not become ready")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("application shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("application did not stop after cancellation")
	}
	if application.Ready() {
		t.Fatal("application remained ready after shutdown")
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("stat prepared data directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("prepared data path is not a directory: %s", dataDir)
	}
}

func TestRunRejectsDataFile(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create data file: %v", err)
	}
	application := testApplication(t, map[string]string{
		"WAZZAP_DATA_DIR":     dataPath,
		"WAZZAP_HTTP_ADDRESS": "127.0.0.1:0",
	})
	if err := application.Run(context.Background()); err == nil {
		t.Fatal("application accepted file as data directory")
	}
	if application.Ready() {
		t.Fatal("application became ready after data directory failure")
	}
}

func testApplication(t *testing.T, values map[string]string) *Application {
	t.Helper()
	cfg, err := config.Load(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger, _, err := observability.NewLogger(&bytes.Buffer{}, "error", "json")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	return New(cfg, logger)
}

func assertHealth(t *testing.T, handler http.Handler, path string, wantStatus int, wantState string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s status = %d, want %d", path, response.Code, wantStatus)
	}
	var body healthResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s response: %v", path, err)
	}
	if body.Status != wantState {
		t.Fatalf("%s state = %q, want %q", path, body.Status, wantState)
	}
}
