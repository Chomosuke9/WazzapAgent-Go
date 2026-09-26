package policy

import (
	"errors"
	"sort"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

const (
	ChatAllowlistAll    = "*"
	ChatAllowlistDirect = "*@lid"
	ChatAllowlistGroup  = "*@g.us"
)

func IsChatAllowlistWildcard(value string) bool {
	switch value {
	case ChatAllowlistAll, ChatAllowlistDirect, ChatAllowlistGroup:
		return true
	default:
		return false
	}
}

func ChatAllowlistWildcardMatches(value string, kind conversation.ChatKind) bool {
	switch value {
	case ChatAllowlistAll:
		return kind == conversation.ChatDirect || kind == conversation.ChatGroup
	case ChatAllowlistDirect:
		return kind == conversation.ChatDirect
	case ChatAllowlistGroup:
		return kind == conversation.ChatGroup
	default:
		return false
	}
}

// InboundGate derives the trust flags an inbound message carries before it
// reaches the conversation layer: whether its chat is allowlisted and whether
// its sender is the configured owner. Addresses are already provider-normalized
// strings; parsing native identities stays in the provider adapter.
type InboundGate struct {
	owner     string
	allowlist map[string]struct{}
}

// NewInboundGate fails closed: an empty allowlist would silently admit nothing
// and is treated as a configuration error.
func NewInboundGate(owner string, allowlist []string) (InboundGate, error) {
	entries := make(map[string]struct{}, len(allowlist))
	for _, address := range allowlist {
		if address != "" {
			entries[address] = struct{}{}
		}
	}
	if len(entries) == 0 {
		return InboundGate{}, errors.New("allowlist must fail closed")
	}
	return InboundGate{owner: owner, allowlist: entries}, nil
}

// Allowlist returns the deduplicated entries in a stable order.
func (gate InboundGate) Allowlist() []string {
	entries := make([]string, 0, len(gate.allowlist))
	for address := range gate.allowlist {
		entries = append(entries, address)
	}
	sort.Strings(entries)
	return entries
}

// IsOwner reports whether any of the sender's known addresses is the owner.
func (gate InboundGate) IsOwner(addresses ...string) bool {
	if gate.owner == "" {
		return false
	}
	for _, address := range addresses {
		if address == gate.owner {
			return true
		}
	}
	return false
}

// ChatAllowlisted matches the chat address, then kind wildcards. Alternative
// addresses (a direct chat's phone alias) are honored only outside groups so a
// group sender's alias can never admit the whole group.
func (gate InboundGate) ChatAllowlisted(kind conversation.ChatKind, address string, alternatives ...string) bool {
	if _, exists := gate.allowlist[address]; exists {
		return true
	}
	for _, wildcard := range []string{ChatAllowlistAll, ChatAllowlistDirect, ChatAllowlistGroup} {
		if _, exists := gate.allowlist[wildcard]; exists && ChatAllowlistWildcardMatches(wildcard, kind) {
			return true
		}
	}
	if kind == conversation.ChatGroup {
		return false
	}
	for _, alternative := range alternatives {
		if alternative == "" {
			continue
		}
		if _, exists := gate.allowlist[alternative]; exists {
			return true
		}
	}
	return false
}
