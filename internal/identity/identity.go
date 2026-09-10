// Package identity contains validated semantic identifiers shared by the core.
// Delivery addresses and credentials deliberately do not belong here. WhatsApp
// LID is the canonical participant identity and is represented explicitly.
package identity

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

type TenantID struct{ value string }
type AccountID struct{ value string }
type ChatID struct{ value string }
type ParticipantID struct{ value string }
type MessageID struct{ value string }
type ActionID struct{ value string }
type EffectID struct{ value string }
type InvocationID struct{ value string }
type CausationID struct{ value string }
type SenderRef struct{ value string }

// LID is WhatsApp's canonical participant identity. Phone JIDs are aliases only.
type LID struct{ value string }
type ProviderID struct{ value string }
type PolicyID struct{ value string }

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,62}$`)
var senderRefPattern = regexp.MustCompile(`^[0-9a-z]{6}$`)
var lidPattern = regexp.MustCompile(`^[0-9]{1,32}@(hosted\.)?lid$`)

const (
	senderRefAlphabet      = "0123456789abcdefghijklmnopqrstuvwxyz"
	senderRefSpace         = uint64(36 * 36 * 36 * 36 * 36 * 36)
	senderRefSamplingLimit = (uint64(1) << 32) - (uint64(1)<<32)%senderRefSpace
)

func ParseTenantID(value string) (TenantID, error) {
	value, err := parseUUID("tenant ID", value)
	return TenantID{value: value}, err
}

func ParseAccountID(value string) (AccountID, error) {
	value, err := parseUUID("account ID", value)
	return AccountID{value: value}, err
}

func ParseChatID(value string) (ChatID, error) {
	value, err := parseUUID("chat ID", value)
	return ChatID{value: value}, err
}

func ParseParticipantID(value string) (ParticipantID, error) {
	value, err := parseUUID("participant ID", value)
	return ParticipantID{value: value}, err
}

func ParseMessageID(value string) (MessageID, error) {
	value, err := parseUUID("message ID", value)
	return MessageID{value: value}, err
}

func ParseActionID(value string) (ActionID, error) {
	value, err := parseUUID("action ID", value)
	return ActionID{value: value}, err
}

func ParseEffectID(value string) (EffectID, error) {
	value, err := parseUUID("effect ID", value)
	return EffectID{value: value}, err
}

func ParseInvocationID(value string) (InvocationID, error) {
	value, err := parseUUID("invocation ID", value)
	return InvocationID{value: value}, err
}

func ParseCausationID(value string) (CausationID, error) {
	value, err := parseUUID("causation ID", value)
	return CausationID{value: value}, err
}

func ParseSenderRef(value string) (SenderRef, error) {
	if !senderRefPattern.MatchString(value) {
		return SenderRef{}, errors.New("sender ref must use 6 lowercase base36 characters")
	}
	return SenderRef{value: value}, nil
}

func ParseLID(value string) (LID, error) {
	if !lidPattern.MatchString(value) {
		return LID{}, errors.New("LID must be a numeric WhatsApp @lid or @hosted.lid address")
	}
	return LID{value: value}, nil
}

func ParseProviderID(value string) (ProviderID, error) {
	value, err := parseSlug("provider ID", value)
	return ProviderID{value: value}, err
}

func ParsePolicyID(value string) (PolicyID, error) {
	value, err := parseSlug("policy ID", value)
	return PolicyID{value: value}, err
}

func NewTenantID() (TenantID, error)   { value, err := newUUIDv7(); return TenantID{value}, err }
func NewAccountID() (AccountID, error) { value, err := newUUIDv7(); return AccountID{value}, err }
func NewChatID() (ChatID, error)       { value, err := newUUIDv7(); return ChatID{value}, err }
func NewParticipantID() (ParticipantID, error) {
	value, err := newUUIDv7()
	return ParticipantID{value}, err
}
func NewMessageID() (MessageID, error) { value, err := newUUIDv7(); return MessageID{value}, err }
func NewActionID() (ActionID, error)   { value, err := newUUIDv7(); return ActionID{value}, err }
func NewEffectID() (EffectID, error)   { value, err := newUUIDv7(); return EffectID{value}, err }
func NewInvocationID() (InvocationID, error) {
	value, err := newUUIDv7()
	return InvocationID{value}, err
}
func NewCausationID() (CausationID, error) { value, err := newUUIDv7(); return CausationID{value}, err }

func NewSenderRef() (SenderRef, error) {
	// Generate a uniform six-character lowercase base36 token. Rejection
	// sampling avoids the modulo bias that would otherwise be introduced by
	// mapping the full uint32 range onto 36^6 possible references.
	var buffer [4]byte
	for {
		if _, err := rand.Read(buffer[:]); err != nil {
			return SenderRef{}, fmt.Errorf("generate sender ref: %w", err)
		}
		number := uint64(binary.BigEndian.Uint32(buffer[:]))
		if number >= senderRefSamplingLimit {
			continue
		}
		number %= senderRefSpace
		var value [6]byte
		for index := len(value) - 1; index >= 0; index-- {
			value[index] = senderRefAlphabet[number%36]
			number /= 36
		}
		return ParseSenderRef(string(value[:]))
	}
}

func (id TenantID) String() string      { return id.value }
func (id AccountID) String() string     { return id.value }
func (id ChatID) String() string        { return id.value }
func (id ParticipantID) String() string { return id.value }
func (id MessageID) String() string     { return id.value }
func (id ActionID) String() string      { return id.value }
func (id EffectID) String() string      { return id.value }
func (id InvocationID) String() string  { return id.value }
func (id CausationID) String() string   { return id.value }
func (id SenderRef) String() string     { return id.value }
func (id LID) String() string           { return id.value }
func (id ProviderID) String() string    { return id.value }
func (id PolicyID) String() string      { return id.value }

func (id TenantID) IsZero() bool      { return id.value == "" }
func (id AccountID) IsZero() bool     { return id.value == "" }
func (id ChatID) IsZero() bool        { return id.value == "" }
func (id ParticipantID) IsZero() bool { return id.value == "" }
func (id MessageID) IsZero() bool     { return id.value == "" }
func (id ActionID) IsZero() bool      { return id.value == "" }
func (id EffectID) IsZero() bool      { return id.value == "" }
func (id InvocationID) IsZero() bool  { return id.value == "" }
func (id CausationID) IsZero() bool   { return id.value == "" }
func (id SenderRef) IsZero() bool     { return id.value == "" }
func (id LID) IsZero() bool           { return id.value == "" }
func (id ProviderID) IsZero() bool    { return id.value == "" }
func (id PolicyID) IsZero() bool      { return id.value == "" }

func newUUIDv7() (string, error) {
	value, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate UUIDv7: %w", err)
	}
	return value.String(), nil
}

func parseUUID(name, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return "", fmt.Errorf("%s must be a canonical lowercase UUID: %q", name, value)
	}
	return value, nil
}

func parseSlug(name, value string) (string, error) {
	if !slugPattern.MatchString(value) {
		return "", fmt.Errorf("%s must be a lowercase slug", name)
	}
	return value, nil
}
