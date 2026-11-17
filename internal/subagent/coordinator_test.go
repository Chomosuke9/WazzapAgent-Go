package subagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// memoryStore is an in-memory Store.
type memoryStore struct {
	mu       sync.Mutex
	tasks    map[string]Task
	parts    map[string]bool
	enabled  map[agent.Key]bool
	fallback *bool
}

func newMemoryStore() *memoryStore {
	return &memoryStore{tasks: map[string]Task{}, parts: map[string]bool{}, enabled: map[agent.Key]bool{}}
}

func (store *memoryStore) CreateTask(_ context.Context, task Task) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.tasks {
		if existing.Key == task.Key && existing.Status == StatusRunning {
			return agent.NewError(agent.ErrorConflict, "create", nil)
		}
	}
	task.Status = StatusRunning
	store.tasks[task.SessionID] = task
	return nil
}

func (store *memoryStore) MarkTaskSubmitted(_ context.Context, _ identity.TenantID, session string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[session]
	task.Submitted = true
	store.tasks[session] = task
	return nil
}

func (store *memoryStore) FinishTask(_ context.Context, _ identity.TenantID, session string, status Status, report string, result Result, at time.Time) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.tasks[session]
	if !ok || task.Status != StatusRunning {
		return false, nil
	}
	task.Status, task.Report, task.Result, task.EndedAt = status, report, result, at
	store.tasks[session] = task
	return true, nil
}

func (store *memoryStore) LoadTask(_ context.Context, _ identity.TenantID, session string) (Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.tasks[session]
	if !ok {
		return Task{}, agent.NewError(agent.ErrorNotFound, "load", nil)
	}
	return task, nil
}

func (store *memoryStore) find(match func(Task) bool) (*Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, task := range store.tasks {
		if match(task) {
			copyTask := task
			return &copyTask, nil
		}
	}
	return nil, nil
}

func (store *memoryStore) ActiveTask(_ context.Context, key agent.Key) (*Task, error) {
	return store.find(func(task Task) bool { return task.Key == key && task.Status == StatusRunning })
}

func (store *memoryStore) TaskByInvocation(_ context.Context, key agent.Key, id identity.InvocationID) (*Task, error) {
	return store.find(func(task Task) bool { return task.Key == key && task.InvocationID == id })
}

func (store *memoryStore) LatestUndelivered(_ context.Context, key agent.Key) (*Task, error) {
	return store.find(func(task Task) bool {
		return task.Key == key && task.Status != StatusRunning && task.DeliveredAt.IsZero()
	})
}

func (store *memoryStore) ListUndelivered(context.Context, identity.TenantID) ([]Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var tasks []Task
	for _, task := range store.tasks {
		if task.DeliveredAt.IsZero() {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (store *memoryStore) DeliveryPartDone(_ context.Context, _ identity.TenantID, session, part string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.parts[session+"|"+part], nil
}

func (store *memoryStore) MarkDeliveryPartDone(_ context.Context, _ identity.TenantID, session, part string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.parts[session+"|"+part] = true
	return nil
}

func (store *memoryStore) MarkTaskDelivered(_ context.Context, _ identity.TenantID, session string, at time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[session]
	task.DeliveredAt = at
	store.tasks[session] = task
	return nil
}

func (store *memoryStore) ChatExists(context.Context, agent.Key) (bool, error) { return true, nil }

func (store *memoryStore) SubagentEnabled(_ context.Context, key agent.Key) (bool, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	enabled, ok := store.enabled[key]
	return enabled, ok, nil
}

func (store *memoryStore) SetSubagentEnabled(_ context.Context, key agent.Key, _ Scope, enabled bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.enabled[key] = enabled
	return nil
}

// recorder is the Messenger, Inputs and Turns of a test.
type recorder struct {
	mu    sync.Mutex
	texts []string
	files []StagedFile
	turns []string
	// turn decides what the model does on a result turn.
	turn func(key agent.Key, invocation identity.InvocationID, text string) (agent.InvokeResult, error)
	sent chan struct{}
}

func (rec *recorder) SendText(_ context.Context, _ agent.Key, text string, _ identity.MessageID) error {
	rec.mu.Lock()
	rec.texts = append(rec.texts, text)
	rec.mu.Unlock()
	return nil
}

func (rec *recorder) SendFile(_ context.Context, _ agent.Key, file StagedFile) error {
	rec.mu.Lock()
	rec.files = append(rec.files, file)
	rec.mu.Unlock()
	return nil
}

func (rec *recorder) MessageInput(_ context.Context, _ agent.Key, _ identity.MessageID, dir string) (MessageInput, error) {
	path := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(path, []byte("hello input"), 0o600); err != nil {
		return MessageInput{}, err
	}
	return MessageInput{Paths: []string{path}, Text: "the caption"}, nil
}

func (rec *recorder) SubagentTurn(_ context.Context, key agent.Key, invocation identity.InvocationID, _ identity.CausationID, text, _ string, _ []agent.ModelImage, _ time.Time) (agent.InvokeResult, error) {
	rec.mu.Lock()
	rec.turns = append(rec.turns, text)
	turn := rec.turn
	rec.mu.Unlock()
	defer func() { rec.sent <- struct{}{} }()
	if turn == nil {
		actionID, _ := identity.NewActionID()
		return agent.InvokeResult{Text: "summary", ActionID: actionID}, nil
	}
	return turn(key, invocation, text)
}

// fakeService is a WazzapSubAgents stand-in that accepts every request and
// records what it got.
type fakeService struct {
	mu       sync.Mutex
	executes []map[string]any
	steers   []map[string]any
	server   *httptest.Server
	output   []byte
}

func newFakeService(t *testing.T) *fakeService {
	service := &fakeService{output: bytes.Repeat([]byte("x"), 2048)}
	mux := http.NewServeMux()
	ack := func(writer http.ResponseWriter, body map[string]any, extra map[string]any) {
		files, _ := body["input_files_content"].([]any)
		staged := make([]any, 0, len(files))
		for _, raw := range files {
			item := raw.(map[string]any)
			staged = append(staged, map[string]any{"sha256": item["sha256"], "size": item["size"]})
		}
		reply := map[string]any{"accepted": true, "session_id": body["session_id"], "requested_file_count": len(files),
			"staged_file_count": len(files), "staged_files": staged, "file_errors": []any{}}
		for key, value := range extra {
			reply[key] = value
		}
		_ = json.NewEncoder(writer).Encode(reply)
	}
	mux.HandleFunc("/execute", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer api-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		service.mu.Lock()
		service.executes = append(service.executes, body)
		service.mu.Unlock()
		ack(writer, body, map[string]any{"status": "processing", "request_fingerprint": "fp"})
	})
	mux.HandleFunc("/steer", func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		service.mu.Lock()
		service.steers = append(service.steers, body)
		service.mu.Unlock()
		ack(writer, body, map[string]any{"state": "queued", "steering_id": body["steering_id"]})
	})
	mux.HandleFunc("/sessions/", func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/steering/") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"state": "consumed"})
			return
		}
		if strings.Contains(request.URL.Path, "/outputs/") {
			digest := sha256.Sum256(service.output)
			writer.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
			_, _ = writer.Write(service.output)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	})
	service.server = httptest.NewServer(mux)
	t.Cleanup(service.server.Close)
	return service
}

func newTestCoordinator(t *testing.T, service *fakeService, rec *recorder, store *memoryStore) (*Coordinator, agent.Key) {
	tenant, _ := identity.NewTenantID()
	account, _ := identity.NewAccountID()
	chat, _ := identity.NewChatID()
	key := agent.Key{TenantID: tenant, AccountID: account, ChatID: chat}
	coordinator, err := NewCoordinator(Config{
		URL: service.server.URL, APIToken: "api-token", WebhookURL: "http://localhost/subagent/callback",
		WebhookToken: "hook-token", DataDir: t.TempDir(), EnabledDefault: true,
		RetryBaseBackoff: time.Millisecond, RetryMaxBackoff: time.Millisecond,
	}, Dependencies{TenantID: tenant, AccountID: account, Store: store, Messenger: rec, Inputs: rec})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.BindTurns(rec)
	t.Cleanup(coordinator.Close)
	return coordinator, key
}

func subtaskEffect(key agent.Key, invocation identity.InvocationID, files ...identity.MessageID) effect.Stored {
	effectID, _ := identity.NewEffectID()
	return effect.Stored{Request: effect.PlanRequest{Ref: effect.Ref{Key: key, EffectID: effectID}, InvocationID: invocation}}
}

func callback(t *testing.T, coordinator *Coordinator, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, "/subagent/callback", bytes.NewReader(encoded))
	if token != "" {
		request.Header.Set("X-Subagent-Webhook-Token", token)
	}
	response := httptest.NewRecorder()
	coordinator.Handler().ServeHTTP(response, request)
	return response
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTaskRunsFromSubmitThroughSteeringToDelivery(t *testing.T) {
	service := newFakeService(t)
	rec := &recorder{sent: make(chan struct{}, 4)}
	store := newMemoryStore()
	coordinator, key := newTestCoordinator(t, service, rec, store)
	invocation, _ := identity.NewInvocationID()
	fileMessage, _ := identity.NewMessageID()

	receipt, err := coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, invocation), effect.Subtask{
		Instruction: "Convert it", FileMessageIDs: []identity.MessageID{fileMessage}, HighQuality: true,
	})
	if err != nil || !strings.HasPrefix(receipt, "task:") {
		t.Fatalf("execute = %q, %v", receipt, err)
	}
	session := strings.TrimPrefix(receipt, "task:")
	if !strings.HasPrefix(session, key.ChatID.String()+"_") {
		t.Fatalf("session %q does not name its chat", session)
	}
	waitFor(t, "submit", func() bool { service.mu.Lock(); defer service.mu.Unlock(); return len(service.executes) == 1 })
	submitted := service.executes[0]
	inline, _ := submitted["input_files_content"].([]any)
	if submitted["high_quality"] != true || len(inline) != 2 || submitted["callback_url"] != "http://localhost/subagent/callback" {
		t.Fatalf("execute body = %#v", submitted)
	}

	// A second call while the task runs steers it.
	other, _ := identity.NewInvocationID()
	receipt, err = coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, other), effect.Subtask{Instruction: "Make it a cat"})
	if err != nil || receipt != "steer:"+session {
		t.Fatalf("steer = %q, %v", receipt, err)
	}
	waitFor(t, "steering", func() bool { service.mu.Lock(); defer service.mu.Unlock(); return len(service.steers) == 1 })

	if response := callback(t, coordinator, "wrong", map[string]any{"type": "progress", "session_id": session}); response.Code != http.StatusUnauthorized {
		t.Fatalf("bad token = %d", response.Code)
	}
	callback(t, coordinator, "hook-token", map[string]any{"type": "progress", "session_id": session,
		"entry": map[string]any{"step": "bash", "detail": "pandoc in.pdf", "reason": "convert the file"}})
	status := coordinator.ChatContext(context.Background(), key).Status
	if !strings.Contains(status, "## Active sub-agent task") || !strings.Contains(status, "- bash: convert the file") {
		t.Fatalf("status = %s", status)
	}

	callback(t, coordinator, "hook-token", map[string]any{"type": "queued", "session_id": session, "position": 2, "queue_size": 3})
	callback(t, coordinator, "hook-token", map[string]any{"type": "queued", "session_id": session, "position": 2, "queue_size": 3})

	inlineBytes := []byte("%PDF-1.4 result")
	inlineDigest := sha256.Sum256(inlineBytes)
	bigDigest := sha256.Sum256(service.output)
	response := callback(t, coordinator, "hook-token", map[string]any{"type": "complete", "session_id": session, "result": map[string]any{
		"success": true, "report": "Converted.",
		"output_files_content": []any{map[string]any{"name": "../result.pdf", "content_base64": base64.StdEncoding.EncodeToString(inlineBytes),
			"size": len(inlineBytes), "sha256": hex.EncodeToString(inlineDigest[:])}},
		"output_files_omitted": []any{map[string]any{"name": "big.png", "download_url": "/sessions/" + session + "/outputs/f1",
			"size_bytes": len(service.output), "sha256": hex.EncodeToString(bigDigest[:]), "file_id": "f1"}},
	}})
	if response.Code != http.StatusOK {
		t.Fatalf("complete = %d %s", response.Code, response.Body)
	}
	<-rec.sent
	waitFor(t, "delivery", func() bool {
		task, _ := store.LoadTask(context.Background(), key.TenantID, session)
		return !task.DeliveredAt.IsZero()
	})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.turns) != 1 || !strings.Contains(rec.turns[0], "【SUBTASK FINISHED】") || !strings.Contains(rec.turns[0], "Result: Converted.") ||
		!strings.Contains(rec.turns[0], "- result.pdf (document") {
		t.Fatalf("turn text = %q", rec.turns)
	}
	if len(rec.files) != 2 || rec.files[0].Name != "result.pdf" || rec.files[0].Kind != "document" || rec.files[1].Name != "big.png" {
		t.Fatalf("files = %#v", rec.files)
	}
	// One queue notice (the repeat is deduplicated), and no raw report: the
	// model replied.
	if len(rec.texts) != 1 || !strings.Contains(rec.texts[0], "current queue: 2") {
		t.Fatalf("texts = %q", rec.texts)
	}
	if again := callback(t, coordinator, "hook-token", map[string]any{"type": "complete", "session_id": session, "result": map[string]any{"success": true}}); !strings.Contains(again.Body.String(), "duplicate") {
		t.Fatalf("repeated completion = %s", again.Body)
	}
}

func TestResultTurnCanCorrectAndFallsBackToTheRawReport(t *testing.T) {
	turnRetryDelay = time.Millisecond
	t.Cleanup(func() { turnRetryDelay = 2 * time.Second })
	service := newFakeService(t)
	rec := &recorder{sent: make(chan struct{}, 4)}
	store := newMemoryStore()
	coordinator, key := newTestCoordinator(t, service, rec, store)
	invocation, _ := identity.NewInvocationID()
	receipt, err := coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, invocation), effect.Subtask{Instruction: "Draw a dog"})
	if err != nil {
		t.Fatal(err)
	}
	first := strings.TrimPrefix(receipt, "task:")
	waitFor(t, "submit", func() bool { service.mu.Lock(); defer service.mu.Unlock(); return len(service.executes) == 1 })

	// The model corrects the result on its delivering turn.
	rec.turn = func(key agent.Key, turn identity.InvocationID, _ string) (agent.InvokeResult, error) {
		if _, err := coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, turn), effect.Subtask{Instruction: "Send a PNG, not a zip"}); err != nil {
			t.Errorf("correction: %v", err)
		}
		actionID, _ := identity.NewActionID()
		return agent.InvokeResult{Text: "Redoing it as a PNG", ActionID: actionID}, nil
	}
	callback(t, coordinator, "hook-token", map[string]any{"type": "complete", "session_id": first, "result": map[string]any{"success": true, "report": "zip made"}})
	<-rec.sent
	waitFor(t, "correction submit", func() bool { service.mu.Lock(); defer service.mu.Unlock(); return len(service.executes) == 2 })
	if service.executes[1]["previous_session_id"] != first {
		t.Fatalf("correction body = %#v", service.executes[1])
	}
	active, _ := store.ActiveTask(context.Background(), key)
	if active == nil || active.PreviousSessionID != first {
		t.Fatalf("correction task = %#v", active)
	}

	// The model fails on the correction's turn: the raw report goes out.
	rec.turn = func(agent.Key, identity.InvocationID, string) (agent.InvokeResult, error) {
		return agent.InvokeResult{}, agent.NewError(agent.ErrorProviderFailure, "model", nil)
	}
	callback(t, coordinator, "hook-token", map[string]any{"type": "complete", "session_id": active.SessionID, "result": map[string]any{"success": false, "error": "renderer crashed"}})
	waitFor(t, "raw report", func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return len(rec.texts) == 1 })
	if rec.texts[0] != "renderer crashed" {
		t.Fatalf("raw report = %q", rec.texts)
	}
}

func TestSubmitFailureIsDeliveredAndUnknownSessionsAreKept(t *testing.T) {
	service := newFakeService(t)
	service.server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	})
	rec := &recorder{sent: make(chan struct{}, 4)}
	store := newMemoryStore()
	coordinator, key := newTestCoordinator(t, service, rec, store)
	invocation, _ := identity.NewInvocationID()
	if _, err := coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, invocation), effect.Subtask{Instruction: "x"}); err != nil {
		t.Fatal(err)
	}
	<-rec.sent
	if len(rec.turns) != 1 || !strings.Contains(rec.turns[0], "authentication was rejected") || !strings.Contains(rec.turns[0], "Success: False") {
		t.Fatalf("turn = %q", rec.turns)
	}
	if response := callback(t, coordinator, "hook-token", map[string]any{"type": "complete", "session_id": "not-a-session", "result": map[string]any{}}); response.Code != http.StatusConflict {
		t.Fatalf("unknown session = %d", response.Code)
	}
}

func TestSubagentOffChatGetsTheOffBlockAndCannotStartTasks(t *testing.T) {
	service := newFakeService(t)
	rec := &recorder{sent: make(chan struct{}, 1)}
	store := newMemoryStore()
	coordinator, key := newTestCoordinator(t, service, rec, store)
	_ = coordinator.SetEnabled(context.Background(), key, ScopeChat, false)
	if context := coordinator.ChatContext(context.Background(), key); context == nil || !context.Off {
		t.Fatalf("context = %#v", context)
	}
	invocation, _ := identity.NewInvocationID()
	if _, err := coordinator.ExecuteSubtaskEffect(context.Background(), subtaskEffect(key, invocation), effect.Subtask{Instruction: "x"}); !agent.IsCode(err, agent.ErrorPermissionDenied) {
		t.Fatalf("execute with the sub-agent off = %v", err)
	}
}

func TestDetectKindSniffsAndSendsUnplayableMediaAsDocuments(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		_ = os.WriteFile(path, data, 0o600)
		return path
	}
	if kind, mime := DetectKind(write("noext", []byte("\x89PNG\r\n\x1a\nrest")), "noext", ""); kind != "image" || mime != "image/png" {
		t.Fatalf("png = %s %s", kind, mime)
	}
	if kind, _ := DetectKind(write("a.gif", []byte("GIF89a")), "a.gif", ""); kind != "document" {
		t.Fatalf("gif kind = %s", kind)
	}
	if kind, _ := DetectKind(write("a.mp3", []byte("ID3")), "a.mp3", ""); kind != "audio" {
		t.Fatalf("mp3 kind = %s", kind)
	}
	if name := safeFileName("../../etc/passwd", "x"); name != "passwd" {
		t.Fatalf("safe name = %q", name)
	}
}
