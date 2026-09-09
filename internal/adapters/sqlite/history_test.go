package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestHistoryAppendPaginationResetAndVersionGuard(t *testing.T) {
	clock := &testClock{now: time.Unix(1_700_000_000, 0).UTC()}
	store := openTestStoreWithClock(t, clock)
	key := testKey(t)
	snapshot, err := store.Configs().LoadOrCreate(context.Background(), key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	participantID, _ := identity.NewParticipantID()
	senderRef, _ := identity.NewSenderRef()
	entries := make([]agent.HistoryEntry, 0, 5)
	for index := 0; index < 5; index++ {
		messageID, _ := identity.NewMessageID()
		invocationID, _ := identity.NewInvocationID()
		causationID, _ := identity.NewCausationID()
		entries = append(entries, agent.HistoryEntry{
			MessageID: messageID, InvocationID: invocationID,
			Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: causationID},
			Role:      agent.HistoryUser,
			Sender:    &agent.SenderContext{ParticipantID: participantID, Ref: senderRef, DisplayName: "Tester"},
			Content:   []agent.ContentPart{agent.TextPart{Text: string(rune('a' + index))}},
			CreatedAt: clock.now.Add(time.Duration(index) * time.Second),
		})
		if err := store.History().Append(context.Background(), key, entries[index]); err != nil {
			t.Fatalf("append entry %d: %v", index, err)
		}
	}
	if err := store.History().Append(context.Background(), key, entries[0]); err != nil {
		t.Fatalf("idempotent append: %v", err)
	}
	conflict := entries[0]
	conflict.Content = []agent.ContentPart{agent.TextPart{Text: "different"}}
	if err := store.History().Append(context.Background(), key, conflict); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("conflicting append error = %v, want conflict", err)
	}
	first, err := store.History().ListIfConfigVersion(context.Background(), key, snapshot.Version, agent.HistoryQuery{Limit: 2})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first.Entries) != 2 || first.Next == nil ||
		first.Entries[0].MessageID != entries[3].MessageID || first.Entries[1].MessageID != entries[4].MessageID {
		t.Fatalf("first page = %#v", first)
	}
	first.Entries[0].Content[0] = agent.TextPart{Text: "mutated"}
	second, err := store.History().ListIfConfigVersion(context.Background(), key, snapshot.Version,
		agent.HistoryQuery{Before: *first.Next, Limit: 2})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second.Entries) != 2 || second.Entries[0].MessageID != entries[1].MessageID ||
		second.Entries[1].MessageID != entries[2].MessageID {
		t.Fatalf("second page = %#v", second)
	}
	values := snapshot.Values()
	values.Prompt = "changed"
	updated, err := store.Configs().CompareAndSwap(context.Background(), key, snapshot.Version, values)
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	if _, err := store.History().ListIfConfigVersion(context.Background(), key, snapshot.Version, agent.HistoryQuery{Limit: 2}); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("stale list error = %v, want conflict", err)
	}
	if err := store.History().ResetIfConfigVersion(context.Background(), key, snapshot.Version, clock.now); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("stale reset error = %v, want conflict", err)
	}
	if err := store.History().ResetIfConfigVersion(context.Background(), key, updated.Version, clock.now); err != nil {
		t.Fatalf("reset history: %v", err)
	}
	empty, err := store.History().ListIfConfigVersion(context.Background(), key, updated.Version, agent.HistoryQuery{Limit: 10})
	if err != nil || len(empty.Entries) != 0 {
		t.Fatalf("history after reset = %#v, err=%v", empty, err)
	}
}

func TestHistoryTrimPreservesPendingButBoundsTerminalOutcomes(t *testing.T) {
	clock := &testClock{now: time.Unix(1_700_000_000, 0).UTC()}
	store := openTestStoreWithClock(t, clock)
	key := testKey(t)
	if _, err := store.Configs().LoadOrCreate(context.Background(), key, testDefaults(t)); err != nil {
		t.Fatalf("create config: %v", err)
	}
	for _, delivery := range []agent.DeliveryStatus{agent.DeliverySucceeded, agent.DeliveryPending, agent.DeliveryUnknownOutcome} {
		messageID, _ := identity.NewMessageID()
		invocationID, _ := identity.NewInvocationID()
		causeID, _ := identity.NewCausationID()
		entry := agent.HistoryEntry{
			MessageID: messageID, InvocationID: invocationID,
			Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: causeID},
			Role:      agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: "old"}},
			Delivery: delivery, CreatedAt: clock.now.Add(-48 * time.Hour),
		}
		if err := store.History().Append(context.Background(), key, entry); err != nil {
			t.Fatalf("append delivery %d: %v", delivery, err)
		}
	}
	result, err := store.History().Trim(context.Background(), key, agent.RetentionPolicy{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatalf("trim history: %v", err)
	}
	if result.Removed != 2 {
		t.Fatalf("removed = %d, want 2", result.Removed)
	}
}

func TestReplyToPreResetAssistantKeepsTriggerWithoutResurrectingText(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(1_700_000_000, 0).UTC()}
	store := openTestStoreWithClock(t, clock)
	source := testCandidate(t, "quote-before-reset", "120363000000000099@g.us")
	source.ChatKind = conversation.ChatGroup
	source.MentionsBot = true
	source.OccurredAt = clock.now.Add(-time.Second)
	source.ReceivedAt = clock.now
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, source)
	if err != nil {
		t.Fatalf("claim source: %v", err)
	}
	key := agent.Key{TenantID: source.TenantID, AccountID: source.AccountID, ChatID: claimed.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	capabilities, _ := agent.NewCapabilitySet()
	invocation := agent.Invocation{
		ID:        claimed.Message.InvocationID,
		Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: claimed.Message.CausationID},
		Cause:     agent.CauseInboundMessage,
		Sender: &agent.SenderContext{
			ParticipantID: claimed.Message.SenderID, Ref: claimed.Message.SenderRef, DisplayName: claimed.Message.SenderName,
		},
		Input: []agent.ContentPart{agent.TextPart{Text: claimed.Message.Text}}, Capabilities: capabilities,
		PolicyVersion: snapshot.Version, RequestedAt: claimed.Message.OccurredAt,
	}
	digest, err := agent.DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest source: %v", err)
	}
	turn, err := store.Turns().Claim(ctx, agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now})
	if err != nil {
		t.Fatalf("claim source turn: %v", err)
	}
	plan, err := store.Turns().CommitPlan(ctx, agent.CommitPlanRequest{
		Key: key, InvocationID: invocation.ID, Lease: turn.Lease,
		ConfigVersion: snapshot.Version, ResponseText: "old private context",
	})
	if err != nil {
		t.Fatalf("commit source plan: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE outbound_actions SET provider_receipt = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?`,
		"provider-old-response", key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), plan.ActionID.String(),
	); err != nil {
		t.Fatalf("record provider receipt fixture: %v", err)
	}
	clock.now = clock.now.Add(time.Second)
	if err := store.History().ResetIfConfigVersion(ctx, key, snapshot.Version, clock.now); err != nil {
		t.Fatalf("reset history: %v", err)
	}
	reply := source
	reply.ProviderMessageID = "quote-after-reset"
	reply.ProviderQuotedMessageID = "provider-old-response"
	reply.Text = "new question"
	reply.MentionsBot = false
	reply.OccurredAt = clock.now
	reply.ReceivedAt = clock.now
	resolved, err := store.Inbound().ClaimAndResolveSender(ctx, reply)
	if err != nil {
		t.Fatalf("claim reply: %v", err)
	}
	if !resolved.Message.RepliedToBot || resolved.Message.Quote == nil ||
		resolved.Message.Quote.Role != conversation.QuoteAssistant {
		t.Fatalf("reply trigger was lost after reset: %#v", resolved.Message)
	}
	if resolved.Message.Quote.Text == "old private context" {
		t.Fatal("pre-reset assistant content was resurrected through quote fallback")
	}
}

func TestHistoryListRejectsCorruptedContent(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	key := testKey(t)
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	participantID, _ := identity.NewParticipantID()
	senderRef, _ := identity.NewSenderRef()
	messageID, _ := identity.NewMessageID()
	invocationID, _ := identity.NewInvocationID()
	causeID, _ := identity.NewCausationID()
	entry := agent.HistoryEntry{
		MessageID: messageID, InvocationID: invocationID,
		Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: causeID},
		Role:      agent.HistoryUser,
		Sender:    &agent.SenderContext{ParticipantID: participantID, Ref: senderRef},
		Content:   []agent.ContentPart{agent.TextPart{Text: "original"}}, CreatedAt: time.Now().UTC(),
	}
	if err := store.History().Append(ctx, key, entry); err != nil {
		t.Fatalf("append history: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE history_entries SET content_text = 'tampered'
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND message_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), messageID.String(),
	); err != nil {
		t.Fatalf("tamper history fixture: %v", err)
	}
	_, err = store.History().ListIfConfigVersion(ctx, key, snapshot.Version, agent.HistoryQuery{Limit: 10})
	if !agent.IsCode(err, agent.ErrorIntegrityFailure) {
		t.Fatalf("corrupted history error = %v, want integrity_failure", err)
	}
}

func TestHistoryAppendRejectsSenderRefBoundToAnotherParticipant(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	key := testKey(t)
	if _, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t)); err != nil {
		t.Fatalf("create config: %v", err)
	}
	firstParticipant, _ := identity.NewParticipantID()
	secondParticipant, _ := identity.NewParticipantID()
	sharedRef, _ := identity.NewSenderRef()
	entry := func(participant identity.ParticipantID, text string) agent.HistoryEntry {
		messageID, _ := identity.NewMessageID()
		invocationID, _ := identity.NewInvocationID()
		causeID, _ := identity.NewCausationID()
		return agent.HistoryEntry{
			MessageID: messageID, InvocationID: invocationID,
			Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: causeID},
			Role:      agent.HistoryUser,
			Sender:    &agent.SenderContext{ParticipantID: participant, Ref: sharedRef},
			Content:   []agent.ContentPart{agent.TextPart{Text: text}}, CreatedAt: time.Now().UTC(),
		}
	}
	if err := store.History().Append(ctx, key, entry(firstParticipant, "first")); err != nil {
		t.Fatalf("append first sender: %v", err)
	}
	if err := store.History().Append(ctx, key, entry(secondParticipant, "second")); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("sender-ref identity conflict = %v, want conflict", err)
	}
}
