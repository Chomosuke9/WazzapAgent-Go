package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
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

type diagnosticsLogWriter func([]byte) (int, error)

func (write diagnosticsLogWriter) Write(data []byte) (int, error) { return write(data) }

func TestCLIDiagnosticsAcrossFreshRuns(t *testing.T) {
	dataDir := t.TempDir()
	for range 3 {
		application := testApplication(t, map[string]string{
			"WAZZAP_DATA_DIR": dataDir, "WAZZAP_HTTP_ADDRESS": "127.0.0.1:0",
		})
		address := make(chan string, 1)
		application.logger = slog.New(slog.NewJSONHandler(diagnosticsLogWriter(func(data []byte) (int, error) {
			var entry struct {
				Address string `json:"address"`
			}
			if json.Unmarshal(data, &entry) == nil && entry.Address != "" {
				address <- entry.Address
			}
			return len(data), nil
		}), nil))
		result := make(chan error, 1)
		go func() { result <- application.RunCLI(context.Background()) }()
		t.Cleanup(func() { _ = application.Close(context.Background()) })
		var endpoint string
		select {
		case endpoint = <-address:
		case err := <-result:
			t.Fatalf("CLI exited before listening: %v", err)
		case <-time.After(time.Second):
			t.Fatal("CLI did not report its listener")
		}
		client := &http.Client{Timeout: time.Second}
		for _, path := range []string{"/health/live", "/health/ready", "/metrics"} {
			response, err := client.Get("http://" + endpoint + path)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("%s: status %d", path, response.StatusCode)
			}
		}
		if err := application.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		response, err := client.Get("http://" + endpoint + "/health/live")
		if err == nil {
			_ = response.Body.Close()
			t.Fatal("CLI listener still responds after Close")
		}
		client.CloseIdleConnections()
	}
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

func TestRunDoesNotBindConfiguredDiagnosticsAddress(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve diagnostics address: %v", err)
	}
	defer occupied.Close()
	application := testApplication(t, map[string]string{
		"WAZZAP_HTTP_ADDRESS": occupied.Addr().String(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	select {
	case err := <-result:
		t.Fatalf("core runtime stopped before cancellation: %v", err)
	case <-time.After(2 * time.Second):
	}
	if !application.Ready() {
		t.Fatal("core runtime did not become ready while diagnostics address was occupied")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("core runtime shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("core runtime did not stop after cancellation")
	}
}

func TestRunCLIReportsDiagnosticsListenerFailure(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve diagnostics address: %v", err)
	}
	defer occupied.Close()
	application := testApplication(t, map[string]string{
		"WAZZAP_HTTP_ADDRESS": occupied.Addr().String(),
	})
	err = application.RunCLI(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("RunCLI error = %v, want diagnostics listener failure", err)
	}
	if application.Ready() {
		t.Fatal("application remained ready after diagnostics listener failure")
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
	effectiveValues := make(map[string]string, len(values)+1)
	for key, value := range values {
		effectiveValues[key] = value
	}
	if _, configured := effectiveValues["WAZZAP_DATA_DIR"]; !configured {
		effectiveValues["WAZZAP_DATA_DIR"] = t.TempDir()
	}
	if _, configured := effectiveValues["WAZZAP_WHATSAPP_ENABLED"]; !configured {
		effectiveValues["WAZZAP_WHATSAPP_ENABLED"] = "false"
	}
	cfg, err := config.Load(func(key string) (string, bool) {
		value, ok := effectiveValues[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger, _, err := observability.NewLogger(&bytes.Buffer{}, "error", "json")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	return New(cfg, logger, Options{})
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
