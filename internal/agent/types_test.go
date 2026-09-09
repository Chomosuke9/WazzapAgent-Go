package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestUnquotedInvocationDigestRemainsPart1Compatible(t *testing.T) {
	tenantID, _ := identity.ParseTenantID("018f0000-0000-7000-8000-000000000001")
	accountID, _ := identity.ParseAccountID("018f0000-0000-7000-8000-000000000002")
	chatID, _ := identity.ParseChatID("018f0000-0000-7000-8000-000000000003")
	invocationID, _ := identity.ParseInvocationID("018f0000-0000-7000-8000-000000000004")
	causationID, _ := identity.ParseCausationID("018f0000-0000-7000-8000-000000000005")
	participantID, _ := identity.ParseParticipantID("018f0000-0000-7000-8000-000000000006")
	senderRef, _ := identity.ParseSenderRef("u_01234567")
	key := Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
	invocation := Invocation{
		ID: invocationID, Causation: CausationRef{Kind: CausationMessage, ID: causationID},
		Cause:  CauseInboundMessage,
		Sender: &SenderContext{ParticipantID: participantID, Ref: senderRef, DisplayName: "Alice"},
		Input:  []ContentPart{TextPart{Text: "hello"}}, PolicyVersion: 1,
		RequestedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	want := legacyPart1InvocationDigest(key, invocation)
	got, err := DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest invocation: %v", err)
	}
	if got != want {
		t.Fatalf("unquoted digest changed across the Part 1 to Part 2 upgrade: got %x want %x", got, want)
	}
	quotedMessageID, _ := identity.ParseMessageID("018f0000-0000-7000-8000-000000000007")
	invocation.Quote = &QuoteContext{MessageID: quotedMessageID, Role: HistoryAssistant, Text: "prior reply"}
	quoted, err := DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest quoted invocation: %v", err)
	}
	if quoted == want {
		t.Fatal("quote-aware invocation reused the Part 1 digest")
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

// This intentionally mirrors the frozen Part 1 encoding. It must not grow
// quote fields or otherwise follow future digest implementations.
func legacyPart1InvocationDigest(key Key, invocation Invocation) InvocationDigest {
	var canonical bytes.Buffer
	canonical.WriteString("wazzapagent.invocation.v1")
	writeField(&canonical, key.TenantID.String())
	writeField(&canonical, key.AccountID.String())
	writeField(&canonical, key.ChatID.String())
	canonical.WriteByte(byte(invocation.Cause))
	canonical.WriteByte(byte(invocation.Causation.Kind))
	writeField(&canonical, invocation.Causation.ID.String())
	canonical.WriteByte(1)
	writeField(&canonical, invocation.Sender.ParticipantID.String())
	writeField(&canonical, invocation.Sender.Ref.String())
	writeField(&canonical, invocation.Sender.DisplayName)
	_ = binary.Write(&canonical, binary.BigEndian, uint32(len(invocation.Input)))
	for _, part := range invocation.Input {
		canonical.WriteByte(1)
		writeField(&canonical, part.(TextPart).Text)
	}
	_ = binary.Write(&canonical, binary.BigEndian, uint32(0))
	return sha256.Sum256(canonical.Bytes())
}
