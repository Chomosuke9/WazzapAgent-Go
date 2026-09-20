package agent

import (
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestInvocationDigestUsesOneCurrentCanonicalEncoding(t *testing.T) {
	tenantID, _ := identity.ParseTenantID("018f0000-0000-7000-8000-000000000001")
	accountID, _ := identity.ParseAccountID("018f0000-0000-7000-8000-000000000002")
	chatID, _ := identity.ParseChatID("018f0000-0000-7000-8000-000000000003")
	invocationID, _ := identity.ParseInvocationID("018f0000-0000-7000-8000-000000000004")
	causationID, _ := identity.ParseCausationID("018f0000-0000-7000-8000-000000000005")
	participantID, _ := identity.ParseParticipantID("018f0000-0000-7000-8000-000000000006")
	senderRef, _ := identity.ParseSenderRef("012345")
	key := Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
	invocation := Invocation{
		ID: invocationID, Causation: CausationRef{Kind: CausationMessage, ID: causationID},
		Cause:  CauseInboundMessage,
		Sender: &SenderContext{ParticipantID: participantID, Ref: senderRef, DisplayName: "Alice"},
		Input:  []ContentPart{TextPart{Text: "hello"}}, PolicyVersion: 1,
		RequestedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	first, err := DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest invocation: %v", err)
	}
	second, err := DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("repeat digest invocation: %v", err)
	}
	if first != second {
		t.Fatal("current invocation digest is not deterministic")
	}
	quotedMessageID, _ := identity.ParseMessageID("018f0000-0000-7000-8000-000000000007")
	invocation.Quote = &QuoteContext{MessageID: quotedMessageID, Role: HistoryAssistant, Text: "prior reply"}
	quoted, err := DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest quoted invocation: %v", err)
	}
	if quoted == first {
		t.Fatal("quote-aware invocation reused the unquoted digest")
	}
}

func TestDeliveryForTurnStatePreservesTerminalReplayStatus(t *testing.T) {
	tests := map[TurnState]DeliveryStatus{
		TurnResponsePlanned: DeliveryPending,
		TurnDeliveryPending: DeliveryPending,
		TurnSucceeded:       DeliverySucceeded,
		TurnFailedTerminal:  DeliveryFailedTerminal,
		TurnUnknownOutcome:  DeliveryUnknownOutcome,
	}
	for state, want := range tests {
		if got := deliveryForTurnState(state); got != want {
			t.Errorf("deliveryForTurnState(%d) = %d, want %d", state, got, want)
		}
	}
}

func TestCommandIntentAcceptsRegisteredCommandShape(t *testing.T) {
	target, _ := identity.NewMessageID()
	for _, command := range []string{"/group close", "/help", "/future any arguments"} {
		intent := EffectIntent{Kind: EffectRunCommand, Command: command}
		if err := intent.Validate(); err != nil {
			t.Errorf("%q rejected: %v", command, err)
		}
	}
	for _, command := range []string{"group close", " /help", ""} {
		intent := EffectIntent{Kind: EffectRunCommand, Command: command}
		if err := intent.Validate(); err == nil {
			t.Errorf("%q accepted without valid form/target", command)
		}
	}
	if err := (EffectIntent{Kind: EffectRunCommand, Command: "/group delete", TargetMessageID: target}).Validate(); err != nil {
		t.Fatalf("targeted delete rejected: %v", err)
	}
}
