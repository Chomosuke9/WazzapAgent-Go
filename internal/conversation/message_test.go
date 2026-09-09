package conversation_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestIncomingCandidateEnforcesPartOneTextBoundary(t *testing.T) {
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	lid, _ := identity.ParseLID("10000000001@lid")
	now := time.Now().UTC()
	candidate := conversation.IncomingCandidate{
		TenantID: tenantID, AccountID: accountID, ProviderMessageID: "provider-id",
		ProviderChatAddress: "15550000001@s.whatsapp.net", SenderLID: lid,
		ChatKind: conversation.ChatDirect, Text: strings.Repeat("x", conversation.MaxTextBytes),
		OccurredAt: now, ReceivedAt: now,
	}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("candidate at text limit: %v", err)
	}
	candidate.Text += "x"
	if err := candidate.Validate(); err == nil {
		t.Fatal("candidate above text limit was accepted")
	}
	candidate.Text = string([]byte{0xff})
	if err := candidate.Validate(); err == nil {
		t.Fatal("candidate with invalid UTF-8 was accepted")
	}
}
