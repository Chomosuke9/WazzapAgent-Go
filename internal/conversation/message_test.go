package conversation_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

func TestIncomingCandidateEnforcesPartOneTextBoundary(t *testing.T) {
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	lid, _ := identity.ParseUserID("10000000001")
	now := time.Now().UTC()
	candidate := conversation.IncomingCandidate{
		TenantID: tenantID, AccountID: accountID, ProviderMessageID: "provider-id",
		ProviderChatAddress: "15550000001", SenderUserID: lid,
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
