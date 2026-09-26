package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	MaxHistoryPageSize = 256
	MaxHistoryBytes    = MaxInputBytes
)

type HistoryRole uint8

const (
	HistoryUser HistoryRole = iota + 1
	HistoryAssistant
	HistorySystem
)

type HistoryEntry struct {
	// Sequence is the durable ordering number assigned by the history store.
	// It is output-only: callers never choose it when appending an entry.
	Sequence     uint64
	MessageID    identity.MessageID
	InvocationID identity.InvocationID
	Causation    CausationRef
	Role         HistoryRole
	Sender       *SenderContext
	Quote        *QuoteContext
	Content      []ContentPart
	Mentions     []MentionContext
	Delivery     DeliveryStatus
	CreatedAt    time.Time
}

type HistoryCursor string

type HistoryQuery struct {
	Before              HistoryCursor
	ThroughInvocationID identity.InvocationID
	Limit               uint32
}

type HistoryPage struct {
	Entries []HistoryEntry
	Next    *HistoryCursor
}

type RetentionPolicy struct {
	KeepLatest uint32
	MaxAge     time.Duration
}

type TrimResult struct {
	Removed uint64
}

type HistoryStore interface {
	ListIfConfigVersion(context.Context, Key, ConfigVersion, HistoryQuery) (HistoryPage, error)
	Append(context.Context, Key, HistoryEntry) error
	ResetIfConfigVersion(context.Context, Key, ConfigVersion, time.Time) error
	Trim(context.Context, Key, RetentionPolicy) (TrimResult, error)
}

// History is a chat-scoped child object owned by Agent. Authorization remains
// at the application boundary; version guards only prevent stale decisions.
type History struct {
	key   Key
	store HistoryStore
	gate  *operationGate
	clock Clock
}

func newHistory(key Key, store HistoryStore, gate *operationGate, clock Clock) (*History, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if store == nil || gate == nil || clock == nil {
		return nil, NewError(ErrorInvalidArgument, "create history", fmt.Errorf("store, operation gate, and clock are required"))
	}
	return &History{key: key, store: store, gate: gate, clock: clock}, nil
}

func (history *History) List(ctx context.Context, version ConfigVersion, query HistoryQuery) (HistoryPage, error) {
	if version == 0 {
		return HistoryPage{}, NewError(ErrorInvalidArgument, "list history", fmt.Errorf("authorized config version is required"))
	}
	if err := validateHistoryQuery(query); err != nil {
		return HistoryPage{}, NewError(ErrorInvalidArgument, "list history", err)
	}
	page, err := history.store.ListIfConfigVersion(ctx, history.key, version, query)
	if err != nil {
		return HistoryPage{}, err
	}
	return cloneHistoryPage(page), nil
}

func (history *History) Append(ctx context.Context, entry HistoryEntry) error {
	if entry.Sequence != 0 {
		return NewError(ErrorInvalidArgument, "append history", fmt.Errorf("history sequence is store-assigned"))
	}
	if err := validateHistoryEntry(entry); err != nil {
		return NewError(ErrorInvalidArgument, "append history", err)
	}
	if err := history.gate.acquire(ctx); err != nil {
		return err
	}
	defer history.gate.release()
	return history.store.Append(ctx, history.key, cloneHistoryEntry(entry))
}

func (history *History) Reset(ctx context.Context, version ConfigVersion) error {
	if version == 0 {
		return NewError(ErrorInvalidArgument, "reset history", fmt.Errorf("authorized config version is required"))
	}
	// Do not wait for a model call. The durable reset tombstone cancels its
	// pre-reset lease, so a late result cannot commit or repopulate history.
	return history.store.ResetIfConfigVersion(ctx, history.key, version, history.clock.Now())
}

func (history *History) Trim(ctx context.Context, policy RetentionPolicy) (TrimResult, error) {
	if err := validateRetentionPolicy(policy); err != nil {
		return TrimResult{}, NewError(ErrorInvalidArgument, "trim history", err)
	}
	if err := history.gate.acquire(ctx); err != nil {
		return TrimResult{}, err
	}
	defer history.gate.release()
	return history.store.Trim(ctx, history.key, policy)
}

func (history *History) appendWithinGate(ctx context.Context, entry HistoryEntry) error {
	if entry.Sequence != 0 {
		return NewError(ErrorInvalidArgument, "append history", fmt.Errorf("history sequence is store-assigned"))
	}
	if err := validateHistoryEntry(entry); err != nil {
		return NewError(ErrorInvalidArgument, "append history", err)
	}
	return history.store.Append(ctx, history.key, cloneHistoryEntry(entry))
}

func validateHistoryQuery(query HistoryQuery) error {
	if query.Limit == 0 || query.Limit > MaxHistoryPageSize {
		return NewError(ErrorInvalidArgument, "validate history query", fmt.Errorf("limit must be between 1 and %d", MaxHistoryPageSize))
	}
	if query.Before != "" {
		if _, err := parseHistoryCursor(query.Before); err != nil {
			return NewError(ErrorInvalidArgument, "validate history query", err)
		}
	}
	if query.Before != "" && !query.ThroughInvocationID.IsZero() {
		return NewError(ErrorInvalidArgument, "validate history query", fmt.Errorf("before cursor and invocation boundary cannot be combined"))
	}
	return nil
}

func validateRetentionPolicy(policy RetentionPolicy) error {
	if policy.KeepLatest == 0 && policy.MaxAge <= 0 {
		return NewError(ErrorInvalidArgument, "validate history retention", fmt.Errorf("keep-latest or max-age is required"))
	}
	if policy.KeepLatest > 1_000_000 || policy.MaxAge < 0 {
		return NewError(ErrorInvalidArgument, "validate history retention", fmt.Errorf("retention bounds are invalid"))
	}
	return nil
}

func validateHistoryEntry(entry HistoryEntry) error {
	if entry.MessageID.IsZero() || entry.InvocationID.IsZero() || entry.Causation.ID.IsZero() || entry.CreatedAt.IsZero() {
		return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("message, invocation, causation, and creation time are required"))
	}
	if entry.Causation.Kind < CausationMessage || entry.Causation.Kind > CausationSubagent {
		return Errorf(ErrorInvalidArgument, "validate history entry", "causation kind is invalid")
	}
	if entry.Role < HistoryUser || entry.Role > HistorySystem {
		return Errorf(ErrorInvalidArgument, "validate history entry", "history role is invalid")
	}
	if entry.Role == HistoryUser {
		if entry.Sender == nil || entry.Sender.ParticipantID.IsZero() || entry.Sender.Ref.IsZero() {
			return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("user history requires trusted sender context"))
		}
	} else if entry.Sender != nil {
		return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("assistant and system history must not carry sender context"))
	}
	if entry.Sender != nil && (!utf8.ValidString(entry.Sender.DisplayName) || len(entry.Sender.DisplayName) > MaxDisplayNameBytes) {
		return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("sender display name is invalid"))
	}
	if entry.Sender != nil && entry.Sender.IsSuperAdmin && !entry.Sender.IsAdmin {
		return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("superadmin sender must also be an admin"))
	}
	if err := validateQuoteContext(entry.Quote); err != nil {
		return NewError(ErrorInvalidArgument, "validate history entry", err)
	}
	if entry.Role == HistorySystem && entry.Quote != nil {
		return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("system history must not carry quote context"))
	}
	if entry.Role != HistoryUser && len(entry.Mentions) != 0 {
		return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("only user history may carry raw mention bindings"))
	}
	if len(entry.Content) != 1 {
		return NewError(ErrorUnsupported, "validate history entry", fmt.Errorf("Part 2 history requires exactly one text part"))
	}
	total := 0
	for _, part := range entry.Content {
		text, ok := part.(TextPart)
		if !ok {
			return NewError(ErrorUnsupported, "validate history entry", fmt.Errorf("Part 2 history accepts text only"))
		}
		if text.Text == "" || !utf8.ValidString(text.Text) {
			return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("history text must be non-empty valid UTF-8"))
		}
		total += len(text.Text)
		if total > MaxHistoryBytes {
			return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("history content exceeds %d bytes", MaxHistoryBytes))
		}
	}
	if err := validateMentionContexts(flattenContent(entry.Content), entry.Mentions); err != nil {
		return NewError(ErrorInvalidArgument, "validate history entry", err)
	}
	switch entry.Role {
	case HistoryUser:
		if entry.Delivery != DeliveryNotStarted {
			return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("user history cannot have outbound delivery state"))
		}
	case HistoryAssistant:
		if entry.Delivery != DeliveryPending && entry.Delivery != DeliverySucceeded &&
			entry.Delivery != DeliveryFailedTerminal && entry.Delivery != DeliveryUnknownOutcome {
			return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("assistant delivery state is invalid"))
		}
	case HistorySystem:
		if entry.Delivery != DeliveryNotStarted {
			return NewError(ErrorInvalidArgument, "validate history entry", fmt.Errorf("system history cannot have outbound delivery state"))
		}
	}
	return nil
}

// DigestHistoryEntry returns the canonical immutable identity/content digest
// used by durable stores to distinguish idempotent replay from a collision.
func DigestHistoryEntry(entry HistoryEntry) ([32]byte, error) {
	if err := validateHistoryEntry(entry); err != nil {
		return [32]byte{}, NewError(ErrorIntegrityFailure, "digest history entry", err)
	}
	var canonical bytes.Buffer
	canonical.WriteString("wazzapagent.history.v1")
	writeField(&canonical, entry.MessageID.String())
	writeField(&canonical, entry.InvocationID.String())
	canonical.WriteByte(byte(entry.Causation.Kind))
	writeField(&canonical, entry.Causation.ID.String())
	canonical.WriteByte(byte(entry.Role))
	if entry.Sender == nil {
		canonical.WriteByte(0)
	} else {
		canonical.WriteByte(1)
		writeField(&canonical, entry.Sender.ParticipantID.String())
		writeField(&canonical, entry.Sender.Ref.String())
		writeField(&canonical, entry.Sender.DisplayName)
		writeGroupRoleDigest(&canonical, entry.Sender.IsAdmin, entry.Sender.IsSuperAdmin)
	}
	if entry.Quote == nil {
		canonical.WriteByte(0)
	} else {
		canonical.WriteByte(1)
		writeField(&canonical, entry.Quote.MessageID.String())
		canonical.WriteByte(byte(entry.Quote.Role))
		writeField(&canonical, entry.Quote.SenderRef.String())
		writeField(&canonical, entry.Quote.Text)
		writeGroupRoleDigest(&canonical, entry.Quote.SenderIsAdmin, entry.Quote.SenderIsSuperAdmin)
	}
	_ = binary.Write(&canonical, binary.BigEndian, entry.CreatedAt.UTC().UnixMilli())
	_ = binary.Write(&canonical, binary.BigEndian, uint32(len(entry.Content)))
	for _, part := range entry.Content {
		canonical.WriteByte(1)
		writeField(&canonical, part.(TextPart).Text)
	}
	writeMentionDigestExtension(&canonical, entry.Mentions, quoteMentions(entry.Quote))
	return sha256.Sum256(canonical.Bytes()), nil
}

func formatHistoryCursor(sequence int64) HistoryCursor {
	return HistoryCursor("h1_" + strconv.FormatInt(sequence, 36))
}

func parseHistoryCursor(cursor HistoryCursor) (int64, error) {
	text := string(cursor)
	if !strings.HasPrefix(text, "h1_") || len(text) <= 3 {
		return 0, Errorf(ErrorInvalidArgument, "parse history cursor", "cursor is invalid")
	}
	sequence, err := strconv.ParseInt(strings.TrimPrefix(text, "h1_"), 36, 64)
	if err != nil || sequence <= 0 {
		return 0, Errorf(ErrorInvalidArgument, "parse history cursor", "cursor is invalid")
	}
	return sequence, nil
}

func cloneHistoryEntry(entry HistoryEntry) HistoryEntry {
	entry.Sender = cloneSender(entry.Sender)
	entry.Quote = cloneQuote(entry.Quote)
	entry.Content = cloneContent(entry.Content)
	entry.Mentions = cloneMentions(entry.Mentions)
	return entry
}

func validateQuoteContext(quote *QuoteContext) error {
	if quote == nil {
		return nil
	}
	if quote.MessageID.IsZero() || (quote.Role != HistoryUser && quote.Role != HistoryAssistant) ||
		quote.Text == "" || !utf8.ValidString(quote.Text) || len(quote.Text) > MaxHistoryBytes {
		return Errorf(ErrorInvalidArgument, "validate quote context", "quote identity, role, and bounded text are required")
	}
	if quote.Role == HistoryUser && quote.SenderRef.IsZero() {
		return Errorf(ErrorInvalidArgument, "validate quote context", "quoted user requires sender ref")
	}
	if quote.Role == HistoryAssistant && !quote.SenderRef.IsZero() {
		return Errorf(ErrorInvalidArgument, "validate quote context", "quoted assistant must not carry sender ref")
	}
	if quote.Role == HistoryAssistant && len(quote.Mentions) != 0 {
		return Errorf(ErrorInvalidArgument, "validate quote context", "quoted assistant must not carry raw mention bindings")
	}
	if quote.SenderIsSuperAdmin && !quote.SenderIsAdmin {
		return Errorf(ErrorInvalidArgument, "validate quote context", "quoted superadmin must also be an admin")
	}
	if quote.Role == HistoryAssistant && (quote.SenderIsAdmin || quote.SenderIsSuperAdmin) {
		return Errorf(ErrorInvalidArgument, "validate quote context", "quoted assistant cannot have a group admin role")
	}
	if err := validateMentionContexts(quote.Text, quote.Mentions); err != nil {
		return err
	}
	return nil
}

func cloneHistoryPage(page HistoryPage) HistoryPage {
	copyPage := HistoryPage{Entries: make([]HistoryEntry, len(page.Entries))}
	for index, entry := range page.Entries {
		copyPage.Entries[index] = cloneHistoryEntry(entry)
	}
	if page.Next != nil {
		cursor := *page.Next
		copyPage.Next = &cursor
	}
	return copyPage
}
