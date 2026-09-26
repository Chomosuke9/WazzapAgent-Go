package policy_test

import (
	"slices"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestInboundGateFailsClosedOnEmptyAllowlist(t *testing.T) {
	for _, allowlist := range [][]string{nil, {}, {""}} {
		if _, err := policy.NewInboundGate("15550000001@s.whatsapp.net", allowlist); err == nil {
			t.Fatalf("empty allowlist %q was accepted", allowlist)
		}
	}
}

func TestInboundGateAllowlistIsDeduplicatedAndSorted(t *testing.T) {
	gate, err := policy.NewInboundGate("", []string{"b@lid", "a@lid", "b@lid"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gate.Allowlist(); !slices.Equal(got, []string{"a@lid", "b@lid"}) {
		t.Fatalf("allowlist = %q", got)
	}
}

func TestInboundGateOwnerMatchesAnyKnownAddress(t *testing.T) {
	gate, err := policy.NewInboundGate("15550000011@s.whatsapp.net", []string{policy.ChatAllowlistAll})
	if err != nil {
		t.Fatal(err)
	}
	if !gate.IsOwner("10000000001@lid", "15550000011@s.whatsapp.net") {
		t.Fatal("owner alias was not matched")
	}
	if gate.IsOwner("10000000001@lid", "") {
		t.Fatal("non-owner was matched")
	}
	unowned, _ := policy.NewInboundGate("", []string{policy.ChatAllowlistAll})
	if unowned.IsOwner("") {
		t.Fatal("empty address matched an unset owner")
	}
}

func TestInboundGateAlternativesDoNotAdmitGroups(t *testing.T) {
	phone := "15550000011@s.whatsapp.net"
	gate, err := policy.NewInboundGate("", []string{phone})
	if err != nil {
		t.Fatal(err)
	}
	if !gate.ChatAllowlisted(conversation.ChatDirect, "10000000001@lid", phone) {
		t.Fatal("direct chat phone alias was not honored")
	}
	if gate.ChatAllowlisted(conversation.ChatGroup, "120363000000000001@g.us", phone) {
		t.Fatal("group was admitted through a sender alias")
	}
}

func TestAllowlistWildcardsMatchExpectedChatKinds(t *testing.T) {
	direct := "10000000001@lid"
	group := "120363000000000001@g.us"
	status := "status@broadcast"
	tests := []struct {
		name    string
		pattern string
		kind    conversation.ChatKind
		address string
		want    bool
	}{
		{name: "all direct", pattern: policy.ChatAllowlistAll, kind: conversation.ChatDirect, address: direct, want: true},
		{name: "all group", pattern: policy.ChatAllowlistAll, kind: conversation.ChatGroup, address: group, want: true},
		{name: "all excludes status", pattern: policy.ChatAllowlistAll, kind: conversation.ChatStatus, address: status, want: false},
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
