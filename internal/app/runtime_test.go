package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
)

type fakeRuntime struct {
	runStarted chan struct{}
	release    chan struct{}
	closeCalls atomic.Int32
	closeErr   error
}

func (runtime *fakeRuntime) run(context.Context) error {
	close(runtime.runStarted)
	<-runtime.release
	return nil
}

func (runtime *fakeRuntime) close(context.Context) error {
	runtime.closeCalls.Add(1)
	return runtime.closeErr
}

func TestRunCLITimeoutKeepsWorkersOwned(t *testing.T) {
	application := testEnabledApplicationWith(t, map[string]string{
		"WAZZAP_HTTP_ADDRESS": "127.0.0.1:0", "WAZZAP_SHUTDOWN_TIMEOUT": "20ms",
	})
	fake := &fakeRuntime{runStarted: make(chan struct{}), release: make(chan struct{})}
	application.runtimeFactory = func(context.Context) (runtimeHandle, error) { return fake, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- application.RunCLI(ctx) }()
	awaitSignal(t, fake.runStarted)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("CLI shutdown = %v, want timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CLI exceeded shutdown budget")
	}
	if application.Ready() || fake.closeCalls.Load() != 0 {
		t.Fatal("timeout reported ready or closed resources owned by a worker")
	}
	if err := application.Run(context.Background()); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("restart while draining = %v", err)
	}
	close(fake.release)
	if err := application.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedCLIRunCannotCancelExistingOwner(t *testing.T) {
	application := testApplication(t, map[string]string{"WAZZAP_DATA_DIR": t.TempDir()})
	result := make(chan error, 1)
	go func() { result <- application.Run(context.Background()) }()
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	deadline := time.After(time.Second)
	for !application.Ready() {
		select {
		case <-deadline:
			t.Fatal("first runtime did not become ready")
		case <-time.After(time.Millisecond):
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := application.RunCLI(canceled); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("second CLI Run = %v, want conflict", err)
	}
	if !application.Ready() {
		t.Fatal("rejected caller canceled the active owner")
	}
	if err := application.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestCanceledConstructionClosesWithoutStartingAndPreservesError(t *testing.T) {
	application := testEnabledApplication(t)
	entered, release := make(chan struct{}), make(chan struct{})
	wantErr := errors.New("cleanup failed")
	fake := &fakeRuntime{runStarted: make(chan struct{}), closeErr: wantErr}
	application.runtimeFactory = func(context.Context) (runtimeHandle, error) {
		close(entered)
		<-release
		return fake, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	awaitSignal(t, entered)
	cancel()
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Run lost cleanup error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled construction did not finish")
	}
	select {
	case <-fake.runStarted:
		t.Fatal("worker started after construction was canceled")
	default:
	}
	for range 2 {
		if err := application.Close(context.Background()); !errors.Is(err, wantErr) {
			t.Fatalf("Close lost terminal error: %v", err)
		}
	}
	if fake.closeCalls.Load() != 1 {
		t.Fatal("cleanup repeated")
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("runtime did not reach expected phase")
	}
}

func TestRunRejectsConcurrentAndRepeatedUse(t *testing.T) {
	application := testEnabledApplication(t)
	fake := &fakeRuntime{runStarted: make(chan struct{}), release: make(chan struct{})}
	application.runtimeFactory = func(context.Context) (runtimeHandle, error) { return fake, nil }
	firstResult := make(chan error, 1)
	go func() { firstResult <- application.Run(context.Background()) }()
	select {
	case <-fake.runStarted:
	case <-time.After(time.Second):
		t.Fatal("fake runtime did not start")
	}
	if err := application.Run(context.Background()); err == nil {
		t.Fatal("concurrent Run was accepted")
	}
	close(fake.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := application.Run(context.Background()); err == nil {
		t.Fatal("repeated Run was accepted")
	}
	if got := fake.closeCalls.Load(); got != 1 {
		t.Fatalf("runtime close calls = %d, want 1", got)
	}
}

func TestCloseTimeoutRetainsLifecycleOwnership(t *testing.T) {
	application := testEnabledApplication(t)
	fake := &fakeRuntime{runStarted: make(chan struct{}), release: make(chan struct{})}
	application.runtimeFactory = func(context.Context) (runtimeHandle, error) { return fake, nil }
	result := make(chan error, 1)
	go func() { result <- application.Run(context.Background()) }()
	select {
	case <-fake.runStarted:
	case <-time.After(time.Second):
		t.Fatal("fake runtime did not start")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := application.Close(closeCtx)
	cancel()
	if err == nil {
		t.Fatal("Close unexpectedly waited past its timeout")
	}
	if got := fake.closeCalls.Load(); got != 0 {
		t.Fatalf("runtime closed before workers ended: %d", got)
	}
	if err := application.Run(context.Background()); err == nil {
		t.Fatal("Run was accepted after timed-out Close")
	}
	close(fake.release)
	if err := application.Close(context.Background()); err != nil {
		t.Fatalf("final Close: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("Run after release: %v", err)
	}
	if got := fake.closeCalls.Load(); got != 1 {
		t.Fatalf("runtime close calls = %d, want 1", got)
	}
}

func TestRunHandlesNilRuntimeResult(t *testing.T) {
	application := testEnabledApplication(t)
	application.runtimeFactory = func(context.Context) (runtimeHandle, error) {
		return &fakeRuntime{runStarted: make(chan struct{}), release: make(chan struct{})}, nil
	}
	// Releasing immediately exercises the nil result path without a second
	// receive from the runtime error channel.
	runtime := application.runtimeFactory
	application.runtimeFactory = func(ctx context.Context) (runtimeHandle, error) {
		value, err := runtime(ctx)
		if err == nil {
			close(value.(*fakeRuntime).release)
		}
		return value, err
	}
	result := make(chan error, 1)
	go func() { result <- application.Run(context.Background()) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run hung after nil runtime result")
	}
}

func TestCompositionFailureClosesStore(t *testing.T) {
	application := testEnabledApplicationWith(t, map[string]string{"ASSISTANT_NAME": ""})
	err := application.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "create context builder") {
		t.Fatalf("composition error = %v, want context-builder failure", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, statErr := os.Stat(filepath.FromSlash(application.config.AppDatabasePath() + suffix)); statErr == nil {
			t.Fatalf("composition failure left SQLite sidecar %s", suffix)
		}
	}
	store, err := appsqlite.Open(context.Background(), application.config.AppDatabasePath())
	if err != nil {
		t.Fatalf("reopen store after composition failure: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
}

func testEnabledApplication(t *testing.T) *Application {
	return testEnabledApplicationWith(t, nil)
}

func testEnabledApplicationWith(t *testing.T, overrides map[string]string) *Application {
	t.Helper()
	dataDir := t.TempDir()
	values := map[string]string{
		"WAZZAP_DATA_DIR":         dataDir,
		"WAZZAP_WHATSAPP_ENABLED": "true",
		"WAZZAP_AGENT_ENABLED":    "false",
		"WAZZAP_TENANT_ID":        "11111111-1111-4111-8111-111111111111",
		"WAZZAP_ACCOUNT_ID":       "22222222-2222-4222-8222-222222222222",
		"WAZZAP_OWNER_JID":        "15550000001@s.whatsapp.net",
		"WAZZAP_CHAT_ALLOWLIST":   "15550000002@s.whatsapp.net",
		"WAZZAP_LLM_ENDPOINT":     "https://llm.example.invalid/v1/chat/completions",
		"WAZZAP_LLM_API_KEY":      "test-key",
		"WAZZAP_LLM_MODEL":        "test-model",
		"WAZZAP_BASE_PROMPT":      "test prompt",
	}
	for key, value := range overrides {
		values[key] = value
	}
	cfg, err := config.Load(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("load enabled config: %v", err)
	}
	logger, _, err := observability.NewLogger(&bytes.Buffer{}, "error", "json")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	return New(cfg, logger, Options{SystemPolicy: "policy"})
}
