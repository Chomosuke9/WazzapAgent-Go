package policy_test

import (
	"slices"
	"testing"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
)

func TestInboundGateFailsClosedOnEmptyAllowlist(t *testing.T) {
	for _, allowlist := range [][]string{nil, {}, {""}} {
		if _, err := policy.NewInboundGate("15550000001", allowlist); err == nil {
			t.Fatalf("empty allowlist %q was accepted", allowlist)
		}
	}
}

func TestInboundGateAllowlistIsDeduplicatedAndSorted(t *testing.T) {
	gate, err := policy.NewInboundGate("", []string{"222", "111", "222"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gate.Allowlist(); !slices.Equal(got, []string{"111", "222"}) {
		t.Fatalf("allowlist = %q", got)
	}
}

func TestInboundGateOwnerMatchesAnyKnownAddress(t *testing.T) {
	gate, err := policy.NewInboundGate("15550000011", []string{policy.ChatAllowlistAll})
	if err != nil {
		t.Fatal(err)
	}
	if !gate.IsOwner("10000000001", "15550000011") {
		t.Fatal("owner alias was not matched")
	}
	if gate.IsOwner("10000000001", "") {
		t.Fatal("non-owner was matched")
	}
	unowned, _ := policy.NewInboundGate("", []string{policy.ChatAllowlistAll})
	if unowned.IsOwner("") {
		t.Fatal("empty address matched an unset owner")
	}
}

func TestInboundGateScopesAdmitTheChatsTheyContain(t *testing.T) {
	server, parent, user := "300000000000000001", "300000000000000002", "300000000000000003"
	gate, err := policy.NewInboundGate("", []string{server, parent, user})
	if err != nil {
		t.Fatal(err)
	}
	if !gate.ChatAllowlisted(conversation.ChatGroup, "300000000000000010", server, "") {
		t.Fatal("a channel of an allowlisted server was not admitted")
	}
	if !gate.ChatAllowlisted(conversation.ChatGroup, "300000000000000011", "300000000000000099", parent) {
		t.Fatal("a thread of an allowlisted channel was not admitted")
	}
	if !gate.ChatAllowlisted(conversation.ChatDirect, "300000000000000012", user) {
		t.Fatal("a direct chat with an allowlisted user was not admitted")
	}
	if gate.ChatAllowlisted(conversation.ChatGroup, "300000000000000013", "300000000000000098", "") {
		t.Fatal("a channel outside every scope was admitted")
	}
}

func TestAllowlistWildcardsMatchExpectedChatKinds(t *testing.T) {
	direct := "10000000001"
	group := "120363000000000001"
	tests := []struct {
		name    string
		pattern string
		kind    conversation.ChatKind
		address string
		want    bool
	}{
		{name: "all direct", pattern: policy.ChatAllowlistAll, kind: conversation.ChatDirect, address: direct, want: true},
		{name: "all group", pattern: policy.ChatAllowlistAll, kind: conversation.ChatGroup, address: group, want: true},
		{name: "direct wildcard direct", pattern: policy.ChatAllowlistDirect, kind: conversation.ChatDirect, address: direct, want: true},
		{name: "direct wildcard group", pattern: policy.ChatAllowlistDirect, kind: conversation.ChatGroup, address: group, want: false},
		{name: "group wildcard group", pattern: policy.ChatAllowlistGroup, kind: conversation.ChatGroup, address: group, want: true},
		{name: "group wildcard direct", pattern: policy.ChatAllowlistGroup, kind: conversation.ChatDirect, address: direct, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate, err := policy.NewInboundGate("", []string{test.pattern})
			if err != nil {
				t.Fatal(err)
			}
			if got := gate.ChatAllowlisted(test.kind, test.address); got != test.want {
				t.Fatalf("ChatAllowlisted(%q, %v) = %v, want %v", test.pattern, test.kind, got, test.want)
			}
		})
	}
}
