package policy_test

import (
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestHumanPrincipalCarriesOnlyVerifiedInboundIdentity(t *testing.T) {
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	participantID, _ := identity.NewParticipantID()
	messageID, _ := identity.NewMessageID()
	invocationID, _ := identity.NewInvocationID()
	causationID, _ := identity.NewCausationID()
	lid, _ := identity.ParseLID("10000000001@lid")
	ref, _ := identity.ParseSenderRef("012345")
	now := time.Now().UTC()
	message := conversation.IncomingMessage{
		ID: messageID, InvocationID: invocationID, CausationID: causationID,
		TenantID: tenantID, AccountID: accountID, ChatID: chatID,
		SenderID: participantID, SenderLID: lid, SenderRef: ref,
		ChatKind: conversation.ChatDirect, Text: "hello", OccurredAt: now, ReceivedAt: now,
	}
	principal, err := policy.HumanPrincipal(message)
	if err != nil || principal.Kind != policy.PrincipalHuman || principal.LID != lid || principal.ParticipantID != participantID || !principal.InvocationID.IsZero() {
		t.Fatalf("human principal = %#v, %v", principal, err)
	}
}
