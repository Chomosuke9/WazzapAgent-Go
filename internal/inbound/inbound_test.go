package inbound_test

import (
	"context"
	"encoding/json"
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

func TestRapidMessagesAreDurablyDebouncedIntoOneBoundedBatch(t *testing.T) {
	fixture := newFixtureWithBatching(t, 30*time.Millisecond, 8)
	chat := "15550000012@s.whatsapp.net"
	first := fixture.candidate("batch-1", chat, conversation.ChatDirect, "first")
	second := fixture.candidate("batch-2", chat, conversation.ChatDirect, "second")
	second.OccurredAt = first.OccurredAt.Add(time.Millisecond)
	second.ReceivedAt = first.ReceivedAt.Add(time.Millisecond)
	var wait sync.WaitGroup
	errors := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		errors <- fixture.handler.Handle(context.Background(), first)
	}()
	time.Sleep(5 * time.Millisecond)
	go func() {
		defer wait.Done()
		errors <- fixture.handler.Handle(context.Background(), second)
	}()
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("handle batch: %v", err)
		}
	}
	if fixture.model.calls.Load() != 1 || fixture.sender.count() != 1 {
		t.Fatalf("batched model/sender calls = %d/%d, want 1/1", fixture.model.calls.Load(), fixture.sender.count())
	}
	if err := fixture.handler.Handle(context.Background(), first); err != nil {
		t.Fatalf("replay non-anchor batch member: %v", err)
	}
	if fixture.model.calls.Load() != 1 || fixture.sender.count() != 1 {
		t.Fatal("replayed non-anchor batch member produced another effect")
	}
	claimed, err := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), second)
	if err != nil {
		t.Fatalf("reload batch anchor: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	current, err := fixture.registry.AgentFor(context.Background(), key)
	if err != nil {
		t.Fatalf("load batch agent: %v", err)
	}
	snapshot, _ := current.Config().Refresh(context.Background())
	page, err := current.History().List(context.Background(), snapshot.Version, agent.HistoryQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list batch history: %v", err)
	}
	if len(page.Entries) != 3 || page.Entries[0].Role != agent.HistoryUser ||
		page.Entries[1].Role != agent.HistoryUser || page.Entries[2].Role != agent.HistoryAssistant {
		t.Fatalf("batched history = %#v", page.Entries)
	}
}

func TestMessageBurstCapSplitsOversizedBurstWithoutLosingRemainder(t *testing.T) {
	fixture := newFixtureWithBatching(t, 30*time.Millisecond, 2)
	chat := "15550000014@s.whatsapp.net"
	var wait sync.WaitGroup
	errors := make(chan error, 3)
	for index := 0; index < 3; index++ {
		candidate := fixture.candidate(fmt.Sprintf("burst-%d", index), chat, conversation.ChatDirect, fmt.Sprintf("message-%d", index))
		candidate.OccurredAt = candidate.OccurredAt.Add(time.Duration(index) * time.Millisecond)
		candidate.ReceivedAt = candidate.ReceivedAt.Add(time.Duration(index) * time.Millisecond)
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- fixture.handler.Handle(context.Background(), candidate)
		}()
		time.Sleep(2 * time.Millisecond)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("handle bounded burst: %v", err)
		}
	}
	if fixture.model.calls.Load() != 2 || fixture.sender.count() != 2 {
		t.Fatalf("bounded burst model/sender calls = %d/%d, want 2/2", fixture.model.calls.Load(), fixture.sender.count())
	}
}

func TestGroupReplyToBotTriggersAndCarriesCanonicalQuote(t *testing.T) {
	fixture := newFixture(t)
	chat := "120363000000000012@g.us"
	first := fixture.candidate("quote-source", chat, conversation.ChatGroup, "first")
	first.MentionsBot = true
	if err := fixture.handler.Handle(context.Background(), first); err != nil {
		t.Fatalf("handle quoted source: %v", err)
	}
	receipt := "provider-" + fixture.sender.last().ActionID.String()
	reply := fixture.candidate("quote-reply", chat, conversation.ChatGroup, "reply without mention")
	reply.ProviderQuotedMessageID = receipt
	if err := fixture.handler.Handle(context.Background(), reply); err != nil {
		t.Fatalf("handle reply to bot: %v", err)
	}
	if fixture.model.calls.Load() != 2 || fixture.sender.count() != 2 {
		t.Fatalf("reply trigger calls = %d/%d", fixture.model.calls.Load(), fixture.sender.count())
	}
	claimed, err := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), reply)
	if err != nil {
		t.Fatalf("reload reply: %v", err)
	}
	if !claimed.Message.RepliedToBot || claimed.Message.Quote == nil ||
		claimed.Message.Quote.Role != conversation.QuoteAssistant || claimed.Message.Quote.ID.IsZero() {
		t.Fatalf("canonical reply quote = %#v", claimed.Message)
	}
}

func TestHistoryContextSurvivesStoreAndAgentRecreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart-history.db")
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	firstRuntime := newFixtureAtPath(t, path, tenantID, accountID, 0, 1)
	chat := "15550000016@s.whatsapp.net"
	first := firstRuntime.candidate("restart-context-1", chat, conversation.ChatDirect, "remember blue")
	if err := firstRuntime.handler.Handle(context.Background(), first); err != nil {
		t.Fatalf("handle first message: %v", err)
	}
	if err := firstRuntime.registry.Close(context.Background()); err != nil {
		t.Fatalf("close first registry: %v", err)
	}
	if err := firstRuntime.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	secondRuntime := newFixtureAtPath(t, path, tenantID, accountID, 0, 1)
	followup := secondRuntime.candidate("restart-context-2", chat, conversation.ChatDirect, "what color?")
	if err := secondRuntime.handler.Handle(context.Background(), followup); err != nil {
		t.Fatalf("handle follow-up after restart: %v", err)
	}
	request := secondRuntime.model.lastRequest()
	if len(request.Messages) != 4 ||
		!strings.Contains(request.Messages[1].Content, "remember blue") ||
		!strings.Contains(request.Messages[2].Content, "reply: remember blue") ||
		!strings.Contains(request.Messages[3].Content, "what color?") {
		t.Fatalf("recreated model context = %#v", request.Messages)
	}
}

func TestHelpInfoAndOwnerOnlyReset(t *testing.T) {
	fixture := newFixture(t)
	chat := "15550000013@s.whatsapp.net"
	if err := fixture.handler.Handle(context.Background(), fixture.candidate("control-history", chat, conversation.ChatDirect, "remember")); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	help := fixture.candidate("control-help", chat, conversation.ChatDirect, "/help")
	if err := fixture.handler.Handle(context.Background(), help); err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(fixture.sender.last().Text, "/reset") {
		t.Fatalf("help response = %q", fixture.sender.last().Text)
	}
	info := fixture.candidate("control-info", chat, conversation.ChatDirect, "/info")
	if err := fixture.handler.Handle(context.Background(), info); err != nil {
		t.Fatalf("info: %v", err)
	}
	if !strings.Contains(fixture.sender.last().Text, "History: aktif") {
		t.Fatalf("info response = %q", fixture.sender.last().Text)
	}
	denied := fixture.candidate("control-reset-denied", chat, conversation.ChatDirect, "/reset")
	if err := fixture.handler.Handle(context.Background(), denied); err != nil {
		t.Fatalf("denied reset: %v", err)
	}
	if !strings.Contains(fixture.sender.last().Text, "hanya dapat digunakan oleh owner") {
		t.Fatalf("reset denial = %q", fixture.sender.last().Text)
	}
	reset := fixture.candidate("control-reset", chat, conversation.ChatDirect, "/reset")
	reset.Owner = true
	if err := fixture.handler.Handle(context.Background(), reset); err != nil {
		t.Fatalf("owner reset: %v", err)
	}
	claimed, _ := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), reset)
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	current, _ := fixture.registry.AgentFor(context.Background(), key)
	snapshot, _ := current.Config().Refresh(context.Background())
	page, err := current.History().List(context.Background(), snapshot.Version, agent.HistoryQuery{Limit: 10})
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("history after owner reset = %#v, err=%v", page, err)
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
	return newFixtureWithBatching(t, 0, 1)
}

func newFixtureWithBatching(t *testing.T, debounce time.Duration, burstCap uint32) *fixture {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	return newFixtureAtPath(t, filepath.Join(t.TempDir(), "app.db"), tenantID, accountID, debounce, burstCap)
}

func newFixtureAtPath(
	t *testing.T,
	path string,
	tenantID identity.TenantID,
	accountID identity.AccountID,
	debounce time.Duration,
	burstCap uint32,
) *fixture {
	t.Helper()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	policyID, _ := identity.ParsePolicyID("part1-chat-gate.v1")
	store, err := appsqlite.Open(context.Background(), path)
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
	contextBuilder, _ := agent.NewDeterministicContextBuilder(agent.DefaultMaxContextBytes)
	factory := agent.FactoryFunc(func(ctx context.Context, key agent.Key) (*agent.Agent, error) {
		return agent.New(ctx, key, agent.Dependencies{
			Defaults: defaults, ConfigStore: store.Configs(), HistoryStore: store.History(), Turns: store.Turns(),
			Context: contextBuilder, HistoryWindow: agent.DefaultHistoryWindow, Model: model,
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
	handler, err := inbound.NewHandlerWithBatching(
		store.Inbound(), registry, gate, responder, inbound.DiscardObserver{},
		inbound.BatchOptions{Debounce: debounce, BurstCap: burstCap, Clock: agent.SystemClock{}},
	)
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

type echoModel struct {
	calls atomic.Int32
	mu    sync.Mutex
	seen  []agent.ModelRequest
}

func (model *echoModel) Generate(_ context.Context, request agent.ModelRequest) (agent.ModelResult, error) {
	model.calls.Add(1)
	request.Messages = append([]agent.ModelMessage(nil), request.Messages...)
	model.mu.Lock()
	model.seen = append(model.seen, request)
	model.mu.Unlock()
	if len(request.Messages) == 0 {
		return agent.ModelResult{}, fmt.Errorf("missing model messages")
	}
	content := request.Messages[len(request.Messages)-1].Content
	_, encoded, ok := strings.Cut(content, "\n")
	if !ok {
		return agent.ModelResult{}, fmt.Errorf("invalid user envelope")
	}
	var envelope struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		return agent.ModelResult{}, err
	}
	return agent.ModelResult{Text: "reply: " + envelope.Text}, nil
}

func (model *echoModel) lastRequest() agent.ModelRequest {
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.seen) == 0 {
		return agent.ModelRequest{}
	}
	return model.seen[len(model.seen)-1]
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
