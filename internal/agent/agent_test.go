package agent_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestAgentInvokePersistsPlanAndSkipsModelOnReplay(t *testing.T) {
	store := openStore(t)
	model := &fakeModel{text: "model reply"}
	dispatcher := &fakeDispatcher{}
	events := &eventRecorder{}
	key := newKey(t)
	current := newAgent(t, key, store, model, dispatcher, events)
	invocation := newInvocation(t, agent.InitialConfigVersion, "hello")

	first, err := current.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatalf("first invoke: %v", err)
	}
	second, err := current.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatalf("replay invoke: %v", err)
	}
	if first.ActionID != second.ActionID || first.ResponseID != second.ResponseID || first.Text != second.Text {
		t.Fatalf("replay created a different plan: first=%#v second=%#v", first, second)
	}
	if model.calls.Load() != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls.Load())
	}
	if dispatcher.calls.Load() != 2 {
		t.Fatalf("dispatcher observations = %d, want 2", dispatcher.calls.Load())
	}

	changed := invocation
	changed.Input = []agent.ContentPart{agent.TextPart{Text: "changed"}}
	if _, err := current.Invoke(context.Background(), changed); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("changed replay error = %v, want conflict", err)
	}
}

func TestAgentCapturesConfigVersionAndRejectsStalePolicy(t *testing.T) {
	store := openStore(t)
	model := &blockingModel{started: make(chan agent.ModelRequest, 1), release: make(chan struct{})}
	dispatcher := &fakeDispatcher{}
	events := &eventRecorder{}
	key := newKey(t)
	current := newAgent(t, key, store, model, dispatcher, events)
	invocation := newInvocation(t, agent.InitialConfigVersion, "first")
	result := make(chan error, 1)
	go func() {
		_, err := current.Invoke(context.Background(), invocation)
		result <- err
	}()
	request := <-model.started
	if request.ConfigVersion != agent.InitialConfigVersion || len(request.Messages) < 2 ||
		request.Messages[0].Content != "base prompt" ||
		request.Messages[len(request.Messages)-1].Provenance != agent.ProvenanceCurrentUser {
		t.Fatalf("captured model request = %#v", request)
	}
	snapshot := current.Config().Snapshot()
	updated, err := current.Config().SetPromptOverride(context.Background(), snapshot.Version, agent.PromptOverride{Mode: agent.PromptAppend, Text: "new override"})
	if err != nil {
		t.Fatalf("update config during invoke: %v", err)
	}
	close(model.release)
	if err := <-result; err != nil {
		t.Fatalf("invoke after concurrent config update: %v", err)
	}
	record, err := store.Turns().Load(context.Background(), key, invocation.ID)
	if err != nil {
		t.Fatalf("load turn: %v", err)
	}
	if record.Plan.ConfigVersion != agent.InitialConfigVersion {
		t.Fatalf("plan config version = %d, want original version", record.Plan.ConfigVersion)
	}
	if updated.Version != 2 || events.count() != 1 {
		t.Fatalf("config update version/events = %d/%d", updated.Version, events.count())
	}

	stale := newInvocation(t, agent.InitialConfigVersion, "stale")
	if _, err := current.Invoke(context.Background(), stale); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("stale policy error = %v, want conflict", err)
	}
	if model.callCount.Load() != 1 {
		t.Fatalf("stale policy reached model; calls=%d", model.callCount.Load())
	}

	returned := current.Config().Snapshot()
	returned.PromptOverride.Text = "mutated by caller"
	if current.Config().Snapshot().PromptOverride.Text != "new override" {
		t.Fatal("Config snapshot did not defensively copy prompt override")
	}
}

func TestHistoryResetCancelsInFlightTurnWithoutWaitingForModel(t *testing.T) {
	store := openStore(t)
	model := &blockingModel{started: make(chan agent.ModelRequest, 1), release: make(chan struct{})}
	current := newAgent(t, newKey(t), store, model, &fakeDispatcher{}, &eventRecorder{})
	invokeDone := make(chan error, 1)
	go func() {
		_, err := current.Invoke(context.Background(), newInvocation(t, agent.InitialConfigVersion, "in flight"))
		invokeDone <- err
	}()
	<-model.started
	resetCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := current.History().Reset(resetCtx, agent.InitialConfigVersion); err != nil {
		t.Fatalf("reset while invoke is active: %v", err)
	}
	close(model.release)
	if err := <-invokeDone; !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("late invoke error = %v, want conflict", err)
	}
	page, err := current.History().List(context.Background(), agent.InitialConfigVersion, agent.HistoryQuery{Limit: 10})
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("history after reset = %#v, err=%v", page, err)
	}
}

func TestAgentInvokeSerializesOneChat(t *testing.T) {
	store := openStore(t)
	model := &trackingModel{delay: 15 * time.Millisecond}
	current := newAgent(t, newKey(t), store, model, &fakeDispatcher{}, &eventRecorder{})
	var wait sync.WaitGroup
	for index := 0; index < 6; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			invocation := newInvocation(t, agent.InitialConfigVersion, "message")
			if _, err := current.Invoke(context.Background(), invocation); err != nil {
				t.Errorf("invoke %d: %v", index, err)
			}
		}(index)
	}
	wait.Wait()
	if model.maximum.Load() != 1 {
		t.Fatalf("maximum concurrent model calls = %d, want 1", model.maximum.Load())
	}
}

func TestDifferentChatAgentsInvokeConcurrently(t *testing.T) {
	store := openStore(t)
	model := &trackingModel{delay: 50 * time.Millisecond}
	first := newAgent(t, newKey(t), store, model, &fakeDispatcher{}, &eventRecorder{})
	second := newAgent(t, newKey(t), store, model, &fakeDispatcher{}, &eventRecorder{})
	var wait sync.WaitGroup
	for _, current := range []*agent.Agent{first, second} {
		wait.Add(1)
		go func(current *agent.Agent) {
			defer wait.Done()
			if _, err := current.Invoke(context.Background(), newInvocation(t, agent.InitialConfigVersion, "message")); err != nil {
				t.Errorf("invoke different chat: %v", err)
			}
		}(current)
	}
	wait.Wait()
	if model.maximum.Load() != 2 {
		t.Fatalf("maximum cross-chat model calls = %d, want 2", model.maximum.Load())
	}
}

func TestConfigRefreshRecoversDroppedNotification(t *testing.T) {
	store := openStore(t)
	key := newKey(t)
	first := newAgent(t, key, store, &fakeModel{text: "reply"}, &fakeDispatcher{}, agent.DiscardConfigEvents{})
	second := newAgent(t, key, store, &fakeModel{text: "reply"}, &fakeDispatcher{}, agent.DiscardConfigEvents{})
	updated, err := first.Config().SetPromptOverride(context.Background(), agent.InitialConfigVersion, agent.PromptOverride{
		Mode: agent.PromptAppend, Text: "durable change",
	})
	if err != nil {
		t.Fatalf("mutate first config: %v", err)
	}
	if second.Config().Snapshot().Version != agent.InitialConfigVersion {
		t.Fatal("second config unexpectedly received a notification")
	}
	refreshed, err := second.Config().Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh second config: %v", err)
	}
	if refreshed.Version != updated.Version || refreshed.PromptOverride == nil || refreshed.PromptOverride.Text != "durable change" {
		t.Fatalf("refreshed config = %#v", refreshed)
	}
}

func TestAgentRejectsInvalidModelOutputBeforeDispatch(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "whitespace", text: "   "},
		{name: "invalid UTF-8", text: string([]byte{0xff})},
		{name: "oversized", text: strings.Repeat("x", agent.MaxResponseBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openStore(t)
			dispatcher := &fakeDispatcher{}
			current := newAgent(t, newKey(t), store, &fakeModel{text: test.text}, dispatcher, &eventRecorder{})
			_, err := current.Invoke(context.Background(), newInvocation(t, agent.InitialConfigVersion, "message"))
			if !agent.IsCode(err, agent.ErrorProviderFailure) {
				t.Fatalf("invalid model result error = %v, want provider_failure", err)
			}
			if dispatcher.calls.Load() != 0 {
				t.Fatalf("invalid model result reached dispatcher %d times", dispatcher.calls.Load())
			}
		})
	}
}

func TestAgentPreservesDispatcherFailureBeforeActionObservation(t *testing.T) {
	store := openStore(t)
	dispatchErr := agent.NewError(agent.ErrorStorageFailure, "load action", context.DeadlineExceeded)
	current := newAgent(t, newKey(t), store, &fakeModel{text: "reply"}, failingDispatcher{err: dispatchErr}, &eventRecorder{})
	result, err := current.Invoke(context.Background(), newInvocation(t, agent.InitialConfigVersion, "message"))
	if !agent.IsCode(err, agent.ErrorStorageFailure) {
		t.Fatalf("dispatcher error = %v, want storage_failure", err)
	}
	if result.ActionID.IsZero() || result.Delivery != agent.DeliveryPending {
		t.Fatalf("planned result was lost: %#v", result)
	}
}

func TestRegistryCoalescesConstructionAndWaiterCancellation(t *testing.T) {
	store := openStore(t)
	key := newKey(t)
	constructionStarted := make(chan struct{})
	releaseConstruction := make(chan struct{})
	var constructions atomic.Int32
	factory := agent.FactoryFunc(func(ctx context.Context, requested agent.Key) (*agent.Agent, error) {
		if constructions.Add(1) == 1 {
			close(constructionStarted)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-releaseConstruction:
		}
		return agent.New(ctx, requested, dependencies(store, &fakeModel{text: "ok"}, &fakeDispatcher{}, &eventRecorder{}))
	})
	registry, err := agent.NewRegistry(context.Background(), factory, agent.RegistryLimits{
		MaxLive: 10, IdleTTL: time.Minute, ConstructionTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := registry.AgentFor(firstCtx, key)
		firstResult <- err
	}()
	<-constructionStarted
	secondResult := make(chan *agent.Agent, 1)
	secondError := make(chan error, 1)
	go func() {
		value, err := registry.AgentFor(context.Background(), key)
		secondResult <- value
		secondError <- err
	}()
	cancelFirst()
	if err := <-firstResult; !agent.IsCode(err, agent.ErrorCancelled) {
		t.Fatalf("cancelled waiter error = %v", err)
	}
	close(releaseConstruction)
	second := <-secondResult
	if err := <-secondError; err != nil {
		t.Fatalf("second waiter: %v", err)
	}
	third, err := registry.AgentFor(context.Background(), key)
	if err != nil {
		t.Fatalf("cached lookup: %v", err)
	}
	if second == nil || second != third || constructions.Load() != 1 {
		t.Fatalf("registry uniqueness failed: second=%p third=%p constructions=%d", second, third, constructions.Load())
	}
}

func TestRegistryEnforcesCapacityAndEvictsIdleAgent(t *testing.T) {
	store := openStore(t)
	var constructions atomic.Int32
	factory := agent.FactoryFunc(func(ctx context.Context, key agent.Key) (*agent.Agent, error) {
		constructions.Add(1)
		return agent.New(ctx, key, dependencies(store, &fakeModel{text: "ok"}, &fakeDispatcher{}, &eventRecorder{}))
	})
	registry, err := agent.NewRegistry(context.Background(), factory, agent.RegistryLimits{
		MaxLive: 1, IdleTTL: 30 * time.Millisecond, ConstructionTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close(context.Background()) })
	if _, err := registry.AgentFor(context.Background(), newKey(t)); err != nil {
		t.Fatalf("construct first agent: %v", err)
	}
	secondKey := newKey(t)
	if _, err := registry.AgentFor(context.Background(), secondKey); !agent.IsCode(err, agent.ErrorResourceExhausted) {
		t.Fatalf("capacity error = %v, want resource_exhausted", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := registry.AgentFor(context.Background(), secondKey); err != nil {
		t.Fatalf("construct after idle eviction: %v", err)
	}
	if constructions.Load() != 2 {
		t.Fatalf("agent constructions = %d, want 2", constructions.Load())
	}
}

func openStore(t *testing.T) *appsqlite.Store {
	t.Helper()
	store, err := appsqlite.Open(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newAgent(
	t *testing.T,
	key agent.Key,
	store *appsqlite.Store,
	model agent.ModelInvoker,
	dispatcher agent.ResponseDispatcher,
	events agent.ConfigEventSink,
) *agent.Agent {
	t.Helper()
	value, err := agent.New(context.Background(), key, dependencies(store, model, dispatcher, events))
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return value
}

func dependencies(store *appsqlite.Store, model agent.ModelInvoker, dispatcher agent.ResponseDispatcher, events agent.ConfigEventSink) agent.Dependencies {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	policyID, _ := identity.ParsePolicyID("part1-chat-gate.v1")
	contextBuilder, _ := agent.NewDeterministicContextBuilder(agent.DefaultMaxContextBytes)
	return agent.Dependencies{
		Defaults: agent.ConfigValues{
			Model:      agent.ModelConfig{ProviderID: providerID, Model: "test-model", MaxOutputTokens: 256},
			Prompt:     "base prompt",
			Permission: agent.PermissionConfig{PolicyID: policyID, Revision: 1},
		},
		ConfigStore:   store.Configs(),
		HistoryStore:  store.History(),
		Turns:         store.Turns(),
		Context:       contextBuilder,
		HistoryWindow: agent.DefaultHistoryWindow,
		Model:         model,
		Responses:     dispatcher,
		Events:        events,
		Clock:         agent.SystemClock{},
	}
}

func newKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}

func newInvocation(t *testing.T, version agent.ConfigVersion, text string) agent.Invocation {
	t.Helper()
	invocationID, _ := identity.NewInvocationID()
	causationID, _ := identity.NewCausationID()
	participantID, _ := identity.NewParticipantID()
	senderRef, _ := identity.NewSenderRef()
	capabilities, _ := agent.NewCapabilitySet()
	return agent.Invocation{
		ID:            invocationID,
		Causation:     agent.CausationRef{Kind: agent.CausationMessage, ID: causationID},
		Cause:         agent.CauseInboundMessage,
		Sender:        &agent.SenderContext{ParticipantID: participantID, Ref: senderRef, DisplayName: "Tester"},
		Input:         []agent.ContentPart{agent.TextPart{Text: text}},
		Capabilities:  capabilities,
		PolicyVersion: version,
		RequestedAt:   time.Now().UTC(),
	}
}

type fakeModel struct {
	text  string
	calls atomic.Int32
}

func (model *fakeModel) Generate(context.Context, agent.ModelRequest) (agent.ModelResult, error) {
	model.calls.Add(1)
	return agent.ModelResult{Text: model.text}, nil
}

type blockingModel struct {
	started   chan agent.ModelRequest
	release   chan struct{}
	callCount atomic.Int32
}

func (model *blockingModel) Generate(ctx context.Context, request agent.ModelRequest) (agent.ModelResult, error) {
	model.callCount.Add(1)
	model.started <- request
	select {
	case <-ctx.Done():
		return agent.ModelResult{}, ctx.Err()
	case <-model.release:
		return agent.ModelResult{Text: "captured reply"}, nil
	}
}

type trackingModel struct {
	delay   time.Duration
	active  atomic.Int32
	maximum atomic.Int32
}

func (model *trackingModel) Generate(context.Context, agent.ModelRequest) (agent.ModelResult, error) {
	active := model.active.Add(1)
	for {
		maximum := model.maximum.Load()
		if active <= maximum || model.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	time.Sleep(model.delay)
	model.active.Add(-1)
	return agent.ModelResult{Text: "reply"}, nil
}

type fakeDispatcher struct{ calls atomic.Int32 }

func (dispatcher *fakeDispatcher) Dispatch(_ context.Context, ref agent.DispatchRef) (agent.DeliveryResult, error) {
	dispatcher.calls.Add(1)
	now := time.Now().UTC()
	return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliverySucceeded, CompletedAt: &now}, nil
}

type failingDispatcher struct{ err error }

func (dispatcher failingDispatcher) Dispatch(context.Context, agent.DispatchRef) (agent.DeliveryResult, error) {
	return agent.DeliveryResult{}, dispatcher.err
}

type eventRecorder struct {
	mu     sync.Mutex
	events []agent.ConfigChanged
}

func (recorder *eventRecorder) TryPublish(event agent.ConfigChanged) bool {
	recorder.mu.Lock()
	recorder.events = append(recorder.events, event)
	recorder.mu.Unlock()
	return true
}

func (recorder *eventRecorder) count() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return len(recorder.events)
}
