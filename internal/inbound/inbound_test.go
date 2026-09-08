package inbound_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestFakeEndToEndEligibleDMIsSentExactlyOnce(t *testing.T) {
	fixture := newFixture(t)
	candidate := fixture.candidate("dm-1", "15550000002@s.whatsapp.net", conversation.ChatDirect, "hello")
	if err := fixture.handler.Handle(context.Background(), candidate); err != nil {
		t.Fatalf("handle DM: %v", err)
	}
	if err := fixture.handler.Handle(context.Background(), candidate); err != nil {
		t.Fatalf("handle duplicate DM: %v", err)
	}
	if fixture.model.calls.Load() != 1 || fixture.sender.count() != 1 {
		t.Fatalf("model/sender calls = %d/%d, want 1/1", fixture.model.calls.Load(), fixture.sender.count())
	}
	if fixture.sender.last().Text != "reply: hello" {
		t.Fatalf("sent text = %q", fixture.sender.last().Text)
	}
	claimed, err := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("load durable message: %v", err)
	}
	if err := fixture.handler.Resume(context.Background(), claimed.Message); err != nil {
		t.Fatalf("explicit plan replay: %v", err)
	}
	if fixture.model.calls.Load() != 1 || fixture.sender.count() != 1 {
		t.Fatalf("explicit replay model/sender calls = %d/%d, want 1/1", fixture.model.calls.Load(), fixture.sender.count())
	}
}

func TestFakeEndToEndGroupRequiresMention(t *testing.T) {
	fixture := newFixture(t)
	chat := "120363000000000001@g.us"
	ignored := fixture.candidate("group-1", chat, conversation.ChatGroup, "without mention")
	if err := fixture.handler.Handle(context.Background(), ignored); err != nil {
		t.Fatalf("handle non-mention: %v", err)
	}
	if fixture.model.calls.Load() != 0 || fixture.sender.count() != 0 {
		t.Fatal("non-mentioned group message reached model or sender")
	}
	mentioned := fixture.candidate("group-2", chat, conversation.ChatGroup, "with mention")
	mentioned.MentionsBot = true
	if err := fixture.handler.Handle(context.Background(), mentioned); err != nil {
		t.Fatalf("handle mention: %v", err)
	}
	if fixture.model.calls.Load() != 1 || fixture.sender.count() != 1 {
		t.Fatalf("mentioned group model/sender calls = %d/%d", fixture.model.calls.Load(), fixture.sender.count())
	}
}

func TestPromptCommandsAreOwnerOnlyPersistedAndBypassModel(t *testing.T) {
	fixture := newFixture(t)
	chat := "15550000003@s.whatsapp.net"
	set := fixture.candidate("prompt-1", chat, conversation.ChatDirect, "/prompt set speak concisely")
	set.Owner = true
	if err := fixture.handler.Handle(context.Background(), set); err != nil {
		t.Fatalf("set prompt: %v", err)
	}
	if fixture.model.calls.Load() != 0 || fixture.sender.count() != 1 {
		t.Fatalf("prompt set model/sender calls = %d/%d", fixture.model.calls.Load(), fixture.sender.count())
	}
	claimed, err := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), set)
	if err != nil {
		t.Fatalf("reload prompt message: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	snapshot, err := fixture.store.Configs().Load(context.Background(), key)
	if err != nil {
		t.Fatalf("load prompt config: %v", err)
	}
	if snapshot.Version != 2 || snapshot.PromptOverride == nil || snapshot.PromptOverride.Text != "speak concisely" || snapshot.Prompt != "base prompt" {
		t.Fatalf("persisted prompt config = %#v", snapshot)
	}

	view := fixture.candidate("prompt-2", chat, conversation.ChatDirect, "/prompt view")
	view.Owner = true
	if err := fixture.handler.Handle(context.Background(), view); err != nil {
		t.Fatalf("view prompt: %v", err)
	}
	if got := fixture.sender.last().Text; got != "Prompt override saat ini:\nspeak concisely" {
		t.Fatalf("view response = %q", got)
	}

	denied := fixture.candidate("prompt-3", chat, conversation.ChatDirect, "/prompt set unauthorized")
	denied.Owner = false
	if err := fixture.handler.Handle(context.Background(), denied); err != nil {
		t.Fatalf("deny non-owner prompt: %v", err)
	}
	afterDenied, _ := fixture.store.Configs().Load(context.Background(), key)
	if afterDenied.Version != snapshot.Version || afterDenied.PromptOverride.Text != "speak concisely" {
		t.Fatalf("non-owner changed config: %#v", afterDenied)
	}
	if got := fixture.sender.last().Text; got != "Perintah /prompt hanya dapat digunakan oleh owner yang dikonfigurasi." {
		t.Fatalf("denial response = %q", got)
	}

	clear := fixture.candidate("prompt-4", chat, conversation.ChatDirect, "/prompt clear")
	clear.Owner = true
	if err := fixture.handler.Handle(context.Background(), clear); err != nil {
		t.Fatalf("clear prompt: %v", err)
	}
	cleared, _ := fixture.store.Configs().Load(context.Background(), key)
	if cleared.PromptOverride != nil || cleared.Prompt != "base prompt" || cleared.Version != 3 {
		t.Fatalf("clear changed wrong config fields: %#v", cleared)
	}
	if fixture.model.calls.Load() != 0 {
		t.Fatalf("prompt commands called model %d times", fixture.model.calls.Load())
	}
}

func TestUnknownSendOutcomeIsNotAutomaticallySentAgain(t *testing.T) {
	fixture := newFixture(t)
	fixture.sender.err = agent.NewError(agent.ErrorTimeout, "fake send", context.DeadlineExceeded)
	candidate := fixture.candidate("unknown-1", "15550000004@s.whatsapp.net", conversation.ChatDirect, "hello")
	err := fixture.handler.Handle(context.Background(), candidate)
	if !agent.IsCode(err, agent.ErrorUnknownOutcome) {
		t.Fatalf("first send error = %v, want unknown_outcome", err)
	}
	if err := fixture.handler.Handle(context.Background(), candidate); err != nil {
		// The completed unknown inbound is intentionally ignored on provider
		// duplicate rather than risking a second effect.
		t.Fatalf("duplicate unknown outcome: %v", err)
	}
	if fixture.model.calls.Load() != 1 || fixture.sender.count() != 1 {
		t.Fatalf("unknown replay model/sender calls = %d/%d, want 1/1", fixture.model.calls.Load(), fixture.sender.count())
	}
	claimed, claimErr := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if claimErr != nil {
		t.Fatalf("load unknown message: %v", claimErr)
	}
	if resumeErr := fixture.handler.Resume(context.Background(), claimed.Message); !agent.IsCode(resumeErr, agent.ErrorUnknownOutcome) {
		t.Fatalf("explicit unknown replay error = %v, want unknown_outcome", resumeErr)
	}
	if fixture.sender.count() != 1 {
		t.Fatalf("explicit unknown replay resent %d times", fixture.sender.count())
	}
}

func TestPromptMutationRecoversCrashAfterConfigCommitWithoutApplyingTwice(t *testing.T) {
	fixture := newFixture(t)
	candidate := fixture.candidate("prompt-crash", "15550000005@s.whatsapp.net", conversation.ChatDirect, "/prompt set crash-safe")
	candidate.Owner = true
	claimed, err := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim command: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	current, err := fixture.registry.AgentFor(context.Background(), key)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	snapshot, err := current.Config().Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh config: %v", err)
	}
	command, _ := inbound.ParsePromptCommand(candidate.Text)
	journal, err := fixture.store.Inbound().BeginPromptMutation(context.Background(), claimed.Message, command, snapshot.Version)
	if err != nil {
		t.Fatalf("begin command journal: %v", err)
	}
	if _, err := current.Config().SetPromptOverride(context.Background(), journal.ExpectedVersion, agent.PromptOverride{Mode: agent.PromptAppend, Text: command.Text}); err != nil {
		t.Fatalf("commit config before simulated crash: %v", err)
	}
	// Simulate a crash before MarkPromptMutationApplied and response planning by
	// invoking the duplicate durable inbox record through the normal handler.
	if err := fixture.handler.Handle(context.Background(), candidate); err != nil {
		t.Fatalf("recover duplicate command: %v", err)
	}
	recovered, err := fixture.store.Configs().Load(context.Background(), key)
	if err != nil {
		t.Fatalf("load recovered config: %v", err)
	}
	if recovered.Version != 2 || recovered.PromptOverride == nil || recovered.PromptOverride.Text != "crash-safe" {
		t.Fatalf("command was applied twice or lost: %#v", recovered)
	}
	if fixture.sender.count() != 1 || fixture.model.calls.Load() != 0 {
		t.Fatalf("recovery sender/model calls = %d/%d", fixture.sender.count(), fixture.model.calls.Load())
	}
}

func TestDurablyClaimedInboundCanResumeWithoutProviderReplay(t *testing.T) {
	fixture := newFixture(t)
	candidate := fixture.candidate("inbound-crash", "15550000006@s.whatsapp.net", conversation.ChatDirect, "resume me")
	candidate.ReceivedAt = time.Now().UTC().Add(-time.Minute)
	candidate.OccurredAt = candidate.ReceivedAt.Add(-time.Second)
	claimed, err := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim before crash: %v", err)
	}
	now := time.Now().UTC()
	messages, err := fixture.store.Inbound().ListRecoverableInbound(context.Background(), fixture.tenantID, now, now.Add(-5*time.Second), 10)
	if err != nil {
		t.Fatalf("list recoverable inbound: %v", err)
	}
	if len(messages) != 1 || messages[0].InvocationID != claimed.Message.InvocationID {
		t.Fatalf("recoverable messages = %#v", messages)
	}
	if err := fixture.handler.Resume(context.Background(), messages[0]); err != nil {
		t.Fatalf("resume durable inbound: %v", err)
	}
	if fixture.model.calls.Load() != 1 || fixture.sender.count() != 1 {
		t.Fatalf("resumed model/sender calls = %d/%d", fixture.model.calls.Load(), fixture.sender.count())
	}
}

func TestDispatcherRechecksCurrentAllowlistBeforeEverySend(t *testing.T) {
	fixture := newFixture(t)
	chat := "15550000008@s.whatsapp.net"
	original := fixture.candidate("policy-send-1", chat, conversation.ChatDirect, "plan while disconnected")
	claimed, err := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), original)
	if err != nil {
		t.Fatalf("claim original: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	current, err := fixture.registry.AgentFor(context.Background(), key)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	snapshot, err := current.Config().Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh config: %v", err)
	}
	capabilities, _ := agent.NewCapabilitySet()
	invocation := agent.Invocation{
		ID: claimed.Message.InvocationID, Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: claimed.Message.CausationID},
		Cause:  agent.CauseInboundMessage,
		Sender: &agent.SenderContext{ParticipantID: claimed.Message.SenderID, Ref: claimed.Message.SenderRef, DisplayName: claimed.Message.SenderName},
		Input:  []agent.ContentPart{agent.TextPart{Text: claimed.Message.Text}}, Capabilities: capabilities,
		PolicyVersion: snapshot.Version, RequestedAt: claimed.Message.OccurredAt,
	}
	digest, _ := agent.DigestInvocation(key, invocation)
	turnClaim, err := fixture.store.Turns().Claim(context.Background(), agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("claim planned turn: %v", err)
	}
	plan, err := fixture.store.Turns().CommitPlan(context.Background(), agent.CommitPlanRequest{
		Key: key, InvocationID: invocation.ID, Lease: turnClaim.Lease, ConfigVersion: snapshot.Version, ResponseText: "pending response",
	})
	if err != nil {
		t.Fatalf("commit pending plan: %v", err)
	}
	removed := fixture.candidate("policy-send-2", chat, conversation.ChatDirect, "no longer allowed")
	removed.Allowlisted = false
	if err := fixture.handler.Handle(context.Background(), removed); err != nil {
		t.Fatalf("record removed allowlist state: %v", err)
	}
	_, err = fixture.dispatcher.Dispatch(context.Background(), plan.Dispatch)
	if !agent.IsCode(err, agent.ErrorPermissionDenied) {
		t.Fatalf("send after allowlist removal error = %v, want permission_denied", err)
	}
	if fixture.sender.count() != 0 {
		t.Fatalf("policy-denied action reached native sender %d times", fixture.sender.count())
	}
}

func TestPromptCommandGrammarIsClosed(t *testing.T) {
	tests := []struct {
		text       string
		recognized bool
		kind       inbound.PromptCommandKind
	}{
		{text: "/prompt", recognized: true, kind: inbound.PromptView},
		{text: "/prompt view", recognized: true, kind: inbound.PromptView},
		{text: "/prompt clear", recognized: true, kind: inbound.PromptClear},
		{text: "/prompt set hello", recognized: true, kind: inbound.PromptSet},
		{text: "/prompt set " + strings.Repeat("x", agent.MaxPromptBytes+1), recognized: true, kind: inbound.PromptInvalid},
		{text: "/prompt set", recognized: true, kind: inbound.PromptInvalid},
		{text: "/prompt delete", recognized: true, kind: inbound.PromptInvalid},
		{text: " /prompt view", recognized: false},
		{text: "/Prompt view", recognized: false},
		{text: "ordinary", recognized: false},
	}
	for _, test := range tests {
		command, recognized := inbound.ParsePromptCommand(test.text)
		if recognized != test.recognized || (recognized && command.Kind != test.kind) {
			t.Errorf("ParsePromptCommand(%q) = %#v/%v", test.text, command, recognized)
		}
	}
}

type fixture struct {
	tenantID   identity.TenantID
	accountID  identity.AccountID
	store      *appsqlite.Store
	registry   *agent.Registry
	handler    *inbound.Handler
	model      *echoModel
	sender     *recordingSender
	dispatcher *action.Dispatcher
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	policyID, _ := identity.ParsePolicyID("part1-chat-gate.v1")
	store, err := appsqlite.Open(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	model := &echoModel{}
	sender := &recordingSender{}
	sender.ready.Store(true)
	gate, err := policy.NewFixedGate(policyID, 1, store.Configs(), store.Inbound(), true)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	dispatcher, err := action.NewDispatcher(store.Actions(), gate, sender, agent.SystemClock{}, action.DiscardObserver{})
	if err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	defaults := agent.ConfigValues{
		Model:      agent.ModelConfig{ProviderID: providerID, Model: "fake-model", MaxOutputTokens: 256},
		Prompt:     "base prompt",
		Permission: agent.PermissionConfig{PolicyID: policyID, Revision: 1},
	}
	factory := agent.FactoryFunc(func(ctx context.Context, key agent.Key) (*agent.Agent, error) {
		return agent.New(ctx, key, agent.Dependencies{
			Defaults: defaults, ConfigStore: store.Configs(), Turns: store.Turns(), Model: model,
			Responses: dispatcher, Events: agent.DiscardConfigEvents{}, Clock: agent.SystemClock{},
		})
	})
	registry, err := agent.NewRegistry(context.Background(), factory, agent.RegistryLimits{MaxLive: 100, IdleTTL: time.Minute, ConstructionTimeout: time.Second})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	responder, err := action.NewCommandResponder(store.Actions(), dispatcher)
	if err != nil {
		t.Fatalf("create command responder: %v", err)
	}
	handler, err := inbound.NewHandler(store.Inbound(), registry, gate, responder, inbound.DiscardObserver{})
	if err != nil {
		t.Fatalf("create inbound handler: %v", err)
	}
	fixture := &fixture{
		tenantID: tenantID, accountID: accountID, store: store, registry: registry,
		handler: handler, model: model, sender: sender, dispatcher: dispatcher,
	}
	t.Cleanup(func() {
		_ = registry.Close(context.Background())
		_ = store.Close()
	})
	return fixture
}

func (fixture *fixture) candidate(id, chat string, kind conversation.ChatKind, text string) conversation.IncomingCandidate {
	now := time.Now().UTC()
	return conversation.IncomingCandidate{
		TenantID: fixture.tenantID, AccountID: fixture.accountID,
		ProviderMessageID: id, ProviderChatAddress: chat, ProviderSenderAddress: "15550000001@s.whatsapp.net",
		SenderName: "Tester", ChatKind: kind, Text: text, Allowlisted: true,
		OccurredAt: now.Add(-time.Second), ReceivedAt: now,
	}
}

type echoModel struct{ calls atomic.Int32 }

func (model *echoModel) Generate(_ context.Context, request agent.ModelRequest) (agent.ModelResult, error) {
	model.calls.Add(1)
	text, ok := request.Input[0].(agent.TextPart)
	if !ok {
		return agent.ModelResult{}, fmt.Errorf("not text")
	}
	return agent.ModelResult{Text: "reply: " + text.Text}, nil
}

type recordingSender struct {
	mu       sync.Mutex
	requests []action.SendTextRequest
	err      error
	ready    atomic.Bool
}

func (sender *recordingSender) Ready() bool { return sender.ready.Load() }

func (sender *recordingSender) SendText(_ context.Context, request action.SendTextRequest) (action.SendTextResult, error) {
	sender.mu.Lock()
	sender.requests = append(sender.requests, request)
	err := sender.err
	sender.mu.Unlock()
	if err != nil {
		return action.SendTextResult{}, err
	}
	return action.SendTextResult{ProviderReceipt: "provider-" + request.ActionID.String()}, nil
}

func (sender *recordingSender) count() int {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return len(sender.requests)
}

func (sender *recordingSender) last() action.SendTextRequest {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.requests[len(sender.requests)-1]
}
