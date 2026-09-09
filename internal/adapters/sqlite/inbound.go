package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

const ignoredTurnState = -1

func (store *InboundStore) ClaimAndResolveSender(
	ctx context.Context,
	candidate conversation.IncomingCandidate,
) (inbound.ClaimedMessage, error) {
	if err := candidate.Validate(); err != nil {
		return inbound.ClaimedMessage{}, agent.NewError(agent.ErrorInvalidArgument, "claim incoming message", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return inbound.ClaimedMessage{}, storageError("begin incoming claim", err)
	}
	defer tx.Rollback()
	nowMS := candidate.ReceivedAt.UTC().UnixMilli()
	if err := ensureAccount(ctx, tx, candidate.TenantID, candidate.AccountID, nowMS); err != nil {
		return inbound.ClaimedMessage{}, err
	}
	chatID, err := resolveChat(ctx, tx, candidate, nowMS)
	if err != nil {
		return inbound.ClaimedMessage{}, err
	}
	participantID, err := resolveParticipant(ctx, tx, candidate, nowMS)
	if err != nil {
		return inbound.ClaimedMessage{}, err
	}
	senderRef, err := resolveSenderRef(ctx, tx, candidate.TenantID, candidate.AccountID, chatID, participantID, store.senderRefs, nowMS)
	if err != nil {
		return inbound.ClaimedMessage{}, err
	}
	message, state, err := loadInboundByProvider(ctx, tx, candidate.TenantID, candidate.AccountID, chatID, candidate.ProviderMessageID)
	if err == nil {
		message.Owner = candidate.Owner
		message.Allowlisted = candidate.Allowlisted
		if err := tx.Commit(); err != nil {
			return inbound.ClaimedMessage{}, storageError("commit duplicate incoming claim", err)
		}
		return inbound.ClaimedMessage{Message: message, Duplicate: true, Handled: isHandledState(state)}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return inbound.ClaimedMessage{}, storageError("look up incoming dedup key", err)
	}
	quote, err := resolveQuotedMessage(ctx, tx, candidate.TenantID, candidate.AccountID, chatID, candidate.ProviderQuotedMessageID)
	if err != nil {
		return inbound.ClaimedMessage{}, err
	}
	messageID, err := identity.NewMessageID()
	if err != nil {
		return inbound.ClaimedMessage{}, agent.NewError(agent.ErrorInternal, "create incoming message ID", err)
	}
	invocationID, err := identity.NewInvocationID()
	if err != nil {
		return inbound.ClaimedMessage{}, agent.NewError(agent.ErrorInternal, "create incoming invocation ID", err)
	}
	causationID, err := identity.NewCausationID()
	if err != nil {
		return inbound.ClaimedMessage{}, agent.NewError(agent.ErrorInternal, "create incoming causation ID", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO inbound_events(
        tenant_id, account_id, chat_id, invocation_id, message_id, causation_id,
        provider_message_id, participant_id, sender_ref, sender_name, input_text,
        quoted_message_id, quoted_role, quoted_sender_ref, quoted_text, replied_to_bot,
        chat_kind, mentions_bot, from_me, owner, allowlisted, occurred_at_ms,
        received_at_ms, turn_state, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		candidate.TenantID.String(), candidate.AccountID.String(), chatID.String(), invocationID.String(), messageID.String(), causationID.String(),
		candidate.ProviderMessageID, participantID.String(), senderRef.String(), candidate.SenderName, candidate.Text,
		quotedID(quote), quotedRole(quote), quotedSenderRef(quote), quotedText(quote), boolInt(quote != nil && quote.Role == conversation.QuoteAssistant),
		uint8(candidate.ChatKind), boolInt(candidate.MentionsBot), boolInt(candidate.FromMe), boolInt(candidate.Owner), boolInt(candidate.Allowlisted),
		candidate.OccurredAt.UTC().UnixMilli(), nowMS, nowMS,
	)
	if err != nil {
		return inbound.ClaimedMessage{}, storageError("insert incoming event", err)
	}
	if err := tx.Commit(); err != nil {
		return inbound.ClaimedMessage{}, storageError("commit incoming claim", err)
	}
	return inbound.ClaimedMessage{Message: conversation.IncomingMessage{
		ID:           messageID,
		InvocationID: invocationID,
		CausationID:  causationID,
		TenantID:     candidate.TenantID,
		AccountID:    candidate.AccountID,
		ChatID:       chatID,
		SenderID:     participantID,
		SenderRef:    senderRef,
		SenderName:   candidate.SenderName,
		ChatKind:     candidate.ChatKind,
		Text:         candidate.Text,
		Quote:        quote,
		RepliedToBot: quote != nil && quote.Role == conversation.QuoteAssistant,
		MentionsBot:  candidate.MentionsBot,
		FromMe:       candidate.FromMe,
		Owner:        candidate.Owner,
		Allowlisted:  candidate.Allowlisted,
		OccurredAt:   candidate.OccurredAt.UTC(),
		ReceivedAt:   candidate.ReceivedAt.UTC(),
	}}, nil
}

func (store *InboundStore) MarkIgnored(ctx context.Context, message conversation.IncomingMessage, reason inbound.IgnoreReason) error {
	if !reason.Valid() {
		return agent.NewError(agent.ErrorInvalidArgument, "mark incoming message ignored", fmt.Errorf("ignore reason is required"))
	}
	result, err := store.db.ExecContext(ctx, `UPDATE inbound_events SET turn_state = ?, ignored_reason = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
        AND turn_state = 0 AND invocation_digest IS NULL AND action_id IS NULL`,
		ignoredTurnState, string(reason), store.clock.Now().UnixMilli(), message.TenantID.String(), message.AccountID.String(),
		message.ChatID.String(), message.InvocationID.String(),
	)
	if err != nil {
		return storageError("mark incoming message ignored", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storageError("inspect ignored incoming message", err)
	}
	if changed == 0 {
		// Repeated ignore is idempotent; a planned turn must never be overwritten.
		var state int64
		if err := store.db.QueryRowContext(ctx, `SELECT turn_state FROM inbound_events
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
			message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
		).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return agent.NewError(agent.ErrorNotFound, "mark incoming message ignored", fmt.Errorf("message does not exist"))
			}
			return storageError("inspect ignored incoming state", err)
		}
		if state != ignoredTurnState {
			return agent.NewError(agent.ErrorConflict, "mark incoming message ignored", fmt.Errorf("message already entered processing"))
		}
	}
	return nil
}

func (store *InboundStore) IsChatAllowlisted(ctx context.Context, key agent.Key) (bool, error) {
	if err := key.Validate(); err != nil {
		return false, err
	}
	var allowed int
	err := store.db.QueryRowContext(ctx, `SELECT allowlisted FROM chats
      WHERE tenant_id = ? AND account_id = ? AND id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, agent.NewError(agent.ErrorNotFound, "read chat policy", fmt.Errorf("chat does not exist"))
	}
	if err != nil {
		return false, storageError("read chat policy", err)
	}
	return allowed == 1, nil
}

func (store *InboundStore) ReconcileAccountPolicy(
	ctx context.Context,
	tenantID identity.TenantID,
	accountID identity.AccountID,
	ownerAddress string,
	allowlist []string,
) error {
	if tenantID.IsZero() || accountID.IsZero() || strings.TrimSpace(ownerAddress) == "" || len(ownerAddress) > 512 || len(allowlist) == 0 || len(allowlist) > 1024 {
		return agent.NewError(agent.ErrorInvalidArgument, "reconcile account policy", fmt.Errorf("valid identity, owner, and bounded allowlist are required"))
	}
	for _, address := range allowlist {
		if strings.TrimSpace(address) == "" || len(address) > 512 {
			return agent.NewError(agent.ErrorInvalidArgument, "reconcile account policy", fmt.Errorf("allowlist address is invalid"))
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin account policy reconciliation", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET allowlisted = 0
      WHERE tenant_id = ? AND account_id = ?`, tenantID.String(), accountID.String()); err != nil {
		return storageError("clear durable chat allowlist", err)
	}
	for _, address := range allowlist {
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET allowlisted = 1
          WHERE tenant_id = ? AND account_id = ? AND provider_address = ?`,
			tenantID.String(), accountID.String(), address,
		); err != nil {
			return storageError("apply durable chat allowlist", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE participants SET owner = CASE WHEN provider_address = ? THEN 1 ELSE 0 END
      WHERE tenant_id = ? AND account_id = ?`, ownerAddress, tenantID.String(), accountID.String()); err != nil {
		return storageError("apply durable owner policy", err)
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit account policy reconciliation", err)
	}
	return nil
}

func (store *InboundStore) ResolveChatAddress(ctx context.Context, key agent.Key) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	var address sql.NullString
	err := store.db.QueryRowContext(ctx, `SELECT provider_address FROM chats
      WHERE tenant_id = ? AND account_id = ? AND id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&address)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !address.Valid) {
		return "", agent.NewError(agent.ErrorNotFound, "resolve chat target", fmt.Errorf("provider target does not exist"))
	}
	if err != nil {
		return "", storageError("resolve chat target", err)
	}
	return address.String, nil
}

func (store *InboundStore) ListRecoverableInbound(
	ctx context.Context,
	tenantID identity.TenantID,
	now time.Time,
	staleBefore time.Time,
	limit uint32,
) ([]conversation.IncomingMessage, error) {
	if tenantID.IsZero() || now.IsZero() || staleBefore.IsZero() || !staleBefore.Before(now) || limit == 0 || limit > 10_000 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list recoverable inbound", fmt.Errorf("valid tenant, times, and limit are required"))
	}
	rows, err := store.db.QueryContext(ctx, `SELECT
        e.message_id, e.invocation_id, e.causation_id, e.account_id, e.chat_id,
        e.participant_id, e.sender_ref, e.sender_name, e.input_text,
        e.quoted_message_id, e.quoted_role, e.quoted_sender_ref, e.quoted_text, e.replied_to_bot, e.chat_kind,
        e.mentions_bot, e.from_me, p.owner, c.allowlisted, e.occurred_at_ms, e.received_at_ms
      FROM inbound_events e
      JOIN chats c ON c.tenant_id = e.tenant_id AND c.account_id = e.account_id AND c.id = e.chat_id
      JOIN participants p ON p.tenant_id = e.tenant_id AND p.account_id = e.account_id AND p.id = e.participant_id
      WHERE e.tenant_id = ? AND e.action_id IS NULL AND (
        (e.turn_state = 0 AND e.updated_at_ms <= ?) OR
        (e.turn_state = ? AND (e.generation_lease_until_ms IS NULL OR e.generation_lease_until_ms <= ?)) OR
        (e.turn_state = ? AND (e.retry_after_ms IS NULL OR e.retry_after_ms <= ?))
      )
      ORDER BY e.updated_at_ms, e.invocation_id
      LIMIT ?`,
		tenantID.String(), staleBefore.UnixMilli(),
		uint8(agent.TurnGenerating), now.UnixMilli(),
		uint8(agent.TurnFailedRetryable), now.UnixMilli(), limit,
	)
	if err != nil {
		return nil, storageError("list recoverable inbound", err)
	}
	defer rows.Close()
	messages := make([]conversation.IncomingMessage, 0)
	for rows.Next() {
		var (
			messageValue, invocationValue, causationValue, accountValue, chatValue string
			participantValue, senderRefValue, senderName, text                     string
			quotedMessage, quotedSender, quotedTextValue                           sql.NullString
			quotedRoleValue                                                        sql.NullInt64
			repliedToBot, chatKind, mentionsBot, fromMe, owner, allowlisted        int64
			occurredAt, receivedAt                                                 int64
		)
		if err := rows.Scan(&messageValue, &invocationValue, &causationValue, &accountValue, &chatValue,
			&participantValue, &senderRefValue, &senderName, &text,
			&quotedMessage, &quotedRoleValue, &quotedSender, &quotedTextValue, &repliedToBot, &chatKind, &mentionsBot,
			&fromMe, &owner, &allowlisted, &occurredAt, &receivedAt); err != nil {
			return nil, storageError("scan recoverable inbound", err)
		}
		messageID, err := identity.ParseMessageID(messageValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable inbound", err)
		}
		invocationID, err := identity.ParseInvocationID(invocationValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable inbound", err)
		}
		causationID, err := identity.ParseCausationID(causationValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable inbound", err)
		}
		accountID, err := identity.ParseAccountID(accountValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable inbound", err)
		}
		chatID, err := identity.ParseChatID(chatValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable inbound", err)
		}
		participantID, err := identity.ParseParticipantID(participantValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable inbound", err)
		}
		senderRef, err := identity.ParseSenderRef(senderRefValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable inbound", err)
		}
		quote, err := decodeQuotedMessage(quotedMessage, quotedRoleValue, quotedSender, quotedTextValue)
		if err != nil {
			return nil, err
		}
		messages = append(messages, conversation.IncomingMessage{
			ID: messageID, InvocationID: invocationID, CausationID: causationID,
			TenantID: tenantID, AccountID: accountID, ChatID: chatID,
			SenderID: participantID, SenderRef: senderRef, SenderName: senderName,
			ChatKind: conversation.ChatKind(chatKind), Text: text,
			Quote: quote, RepliedToBot: repliedToBot == 1,
			MentionsBot: mentionsBot == 1, FromMe: fromMe == 1, Owner: owner == 1, Allowlisted: allowlisted == 1,
			OccurredAt: time.UnixMilli(occurredAt).UTC(), ReceivedAt: time.UnixMilli(receivedAt).UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("iterate recoverable inbound", err)
	}
	return messages, nil
}

func ensureAccount(ctx context.Context, tx *sql.Tx, tenantID identity.TenantID, accountID identity.AccountID, nowMS int64) error {
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO tenants(id, created_at_ms) VALUES (?, ?)", tenantID.String(), nowMS); err != nil {
		return storageError("ensure inbound tenant", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO accounts(tenant_id, id, created_at_ms) VALUES (?, ?, ?)", tenantID.String(), accountID.String(), nowMS); err != nil {
		return storageError("ensure inbound account", err)
	}
	return nil
}

func resolveChat(ctx context.Context, tx *sql.Tx, candidate conversation.IncomingCandidate, nowMS int64) (identity.ChatID, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT id FROM chats
      WHERE tenant_id = ? AND account_id = ? AND provider_address = ?`,
		candidate.TenantID.String(), candidate.AccountID.String(), candidate.ProviderChatAddress,
	).Scan(&value)
	if err == nil {
		chatID, parseErr := identity.ParseChatID(value)
		if parseErr != nil {
			return identity.ChatID{}, agent.NewError(agent.ErrorIntegrityFailure, "decode chat mapping", parseErr)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET kind = ?, allowlisted = ?
          WHERE tenant_id = ? AND account_id = ? AND id = ?`,
			uint8(candidate.ChatKind), boolInt(candidate.Allowlisted), candidate.TenantID.String(), candidate.AccountID.String(), chatID.String(),
		); err != nil {
			return identity.ChatID{}, storageError("refresh chat mapping", err)
		}
		return chatID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return identity.ChatID{}, storageError("resolve chat mapping", err)
	}
	chatID, err := identity.NewChatID()
	if err != nil {
		return identity.ChatID{}, agent.NewError(agent.ErrorInternal, "create chat ID", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chats(
        tenant_id, account_id, id, provider_address, kind, allowlisted, created_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		candidate.TenantID.String(), candidate.AccountID.String(), chatID.String(), candidate.ProviderChatAddress,
		uint8(candidate.ChatKind), boolInt(candidate.Allowlisted), nowMS,
	); err != nil {
		return identity.ChatID{}, storageError("create chat mapping", err)
	}
	return chatID, nil
}

func resolveParticipant(ctx context.Context, tx *sql.Tx, candidate conversation.IncomingCandidate, nowMS int64) (identity.ParticipantID, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT id FROM participants
      WHERE tenant_id = ? AND account_id = ? AND provider_address = ?`,
		candidate.TenantID.String(), candidate.AccountID.String(), candidate.ProviderSenderAddress,
	).Scan(&value)
	if err == nil {
		participantID, parseErr := identity.ParseParticipantID(value)
		if parseErr != nil {
			return identity.ParticipantID{}, agent.NewError(agent.ErrorIntegrityFailure, "decode participant mapping", parseErr)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE participants SET owner = ?
          WHERE tenant_id = ? AND account_id = ? AND id = ?`,
			boolInt(candidate.Owner), candidate.TenantID.String(), candidate.AccountID.String(), participantID.String(),
		); err != nil {
			return identity.ParticipantID{}, storageError("refresh participant mapping", err)
		}
		return participantID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return identity.ParticipantID{}, storageError("resolve participant mapping", err)
	}
	participantID, err := identity.NewParticipantID()
	if err != nil {
		return identity.ParticipantID{}, agent.NewError(agent.ErrorInternal, "create participant ID", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO participants(
        tenant_id, account_id, id, provider_address, owner, created_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?)`,
		candidate.TenantID.String(), candidate.AccountID.String(), participantID.String(), candidate.ProviderSenderAddress,
		boolInt(candidate.Owner), nowMS,
	); err != nil {
		return identity.ParticipantID{}, storageError("create participant mapping", err)
	}
	return participantID, nil
}

func resolveSenderRef(
	ctx context.Context,
	tx *sql.Tx,
	tenantID identity.TenantID,
	accountID identity.AccountID,
	chatID identity.ChatID,
	participantID identity.ParticipantID,
	factory func() (identity.SenderRef, error),
	nowMS int64,
) (identity.SenderRef, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT sender_ref FROM sender_refs
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND participant_id = ?`,
		tenantID.String(), accountID.String(), chatID.String(), participantID.String(),
	).Scan(&value)
	if err == nil {
		parsed, parseErr := identity.ParseSenderRef(value)
		if parseErr != nil {
			return identity.SenderRef{}, agent.NewError(agent.ErrorIntegrityFailure, "decode sender ref", parseErr)
		}
		return parsed, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return identity.SenderRef{}, storageError("resolve sender ref", err)
	}
	for attempt := 0; attempt < 8; attempt++ {
		ref, err := factory()
		if err != nil {
			return identity.SenderRef{}, agent.NewError(agent.ErrorInternal, "create sender ref", err)
		}
		if ref.IsZero() {
			return identity.SenderRef{}, agent.NewError(agent.ErrorIntegrityFailure, "create sender ref", fmt.Errorf("factory returned an empty reference"))
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sender_refs(
          tenant_id, account_id, chat_id, participant_id, sender_ref, created_at_ms
        ) VALUES (?, ?, ?, ?, ?, ?)`,
			tenantID.String(), accountID.String(), chatID.String(), participantID.String(), ref.String(), nowMS,
		)
		if err == nil {
			return ref, nil
		}
		if !isUniqueConstraint(err) {
			return identity.SenderRef{}, storageError("persist sender ref", err)
		}
	}
	return identity.SenderRef{}, agent.NewError(agent.ErrorResourceExhausted, "create sender ref", fmt.Errorf("collision retry limit reached"))
}

func loadInboundByProvider(
	ctx context.Context,
	query actionQuerier,
	tenantID identity.TenantID,
	accountID identity.AccountID,
	chatID identity.ChatID,
	providerMessageID string,
) (conversation.IncomingMessage, int64, error) {
	var (
		messageValue, invocationValue, causationValue, participantValue, senderRefValue string
		senderName, text                                                                string
		quotedMessage, quotedSender, quotedTextValue                                    sql.NullString
		quotedRoleValue                                                                 sql.NullInt64
		chatKind, mentionsBot, fromMe, owner, allowlisted, occurredAt, receivedAt       int64
		repliedToBot                                                                    int64
		state                                                                           int64
	)
	err := query.QueryRowContext(ctx, `SELECT message_id, invocation_id, causation_id,
        participant_id, sender_ref, sender_name, input_text,
        quoted_message_id, quoted_role, quoted_sender_ref, quoted_text, replied_to_bot,
        chat_kind, mentions_bot,
        from_me, owner, allowlisted, occurred_at_ms, received_at_ms, turn_state
      FROM inbound_events WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND provider_message_id = ?`,
		tenantID.String(), accountID.String(), chatID.String(), providerMessageID,
	).Scan(&messageValue, &invocationValue, &causationValue, &participantValue, &senderRefValue,
		&senderName, &text, &quotedMessage, &quotedRoleValue, &quotedSender, &quotedTextValue, &repliedToBot,
		&chatKind, &mentionsBot, &fromMe, &owner, &allowlisted, &occurredAt, &receivedAt, &state)
	if err != nil {
		return conversation.IncomingMessage{}, 0, err
	}
	messageID, err := identity.ParseMessageID(messageValue)
	if err != nil {
		return conversation.IncomingMessage{}, 0, agent.NewError(agent.ErrorIntegrityFailure, "decode inbound message", err)
	}
	invocationID, err := identity.ParseInvocationID(invocationValue)
	if err != nil {
		return conversation.IncomingMessage{}, 0, agent.NewError(agent.ErrorIntegrityFailure, "decode inbound invocation", err)
	}
	causationID, err := identity.ParseCausationID(causationValue)
	if err != nil {
		return conversation.IncomingMessage{}, 0, agent.NewError(agent.ErrorIntegrityFailure, "decode inbound causation", err)
	}
	participantID, err := identity.ParseParticipantID(participantValue)
	if err != nil {
		return conversation.IncomingMessage{}, 0, agent.NewError(agent.ErrorIntegrityFailure, "decode inbound participant", err)
	}
	senderRef, err := identity.ParseSenderRef(senderRefValue)
	if err != nil {
		return conversation.IncomingMessage{}, 0, agent.NewError(agent.ErrorIntegrityFailure, "decode inbound sender ref", err)
	}
	quote, err := decodeQuotedMessage(quotedMessage, quotedRoleValue, quotedSender, quotedTextValue)
	if err != nil {
		return conversation.IncomingMessage{}, 0, err
	}
	return conversation.IncomingMessage{
		ID:           messageID,
		InvocationID: invocationID,
		CausationID:  causationID,
		TenantID:     tenantID,
		AccountID:    accountID,
		ChatID:       chatID,
		SenderID:     participantID,
		SenderRef:    senderRef,
		SenderName:   senderName,
		ChatKind:     conversation.ChatKind(chatKind),
		Text:         text,
		Quote:        quote,
		RepliedToBot: repliedToBot == 1,
		MentionsBot:  mentionsBot == 1,
		FromMe:       fromMe == 1,
		Owner:        owner == 1,
		Allowlisted:  allowlisted == 1,
		OccurredAt:   time.UnixMilli(occurredAt).UTC(),
		ReceivedAt:   time.UnixMilli(receivedAt).UTC(),
	}, state, nil
}

func resolveQuotedMessage(
	ctx context.Context,
	query actionQuerier,
	tenantID identity.TenantID,
	accountID identity.AccountID,
	chatID identity.ChatID,
	providerMessageID string,
) (*conversation.QuotedMessage, error) {
	if providerMessageID == "" {
		return nil, nil
	}
	var responseValue, text string
	err := query.QueryRowContext(ctx, `SELECT a.response_id,
        CASE
          WHEN r.reset_at_ms IS NOT NULL AND e.received_at_ms <= r.reset_at_ms
            THEN '[konten balasan sebelum reset tidak disertakan]'
          ELSE COALESCE(NULLIF(h.content_text, ''), NULLIF(a.text, ''), '[konten balasan lama tidak lagi disimpan]')
        END
      FROM outbound_actions a
      JOIN inbound_events e ON e.tenant_id = a.tenant_id AND e.account_id = a.account_id
        AND e.chat_id = a.chat_id AND e.invocation_id = a.invocation_id
      LEFT JOIN history_resets r ON r.tenant_id = a.tenant_id AND r.account_id = a.account_id AND r.chat_id = a.chat_id
      LEFT JOIN history_entries h ON h.tenant_id = a.tenant_id AND h.account_id = a.account_id
        AND h.chat_id = a.chat_id AND h.message_id = a.response_id
      WHERE a.tenant_id = ? AND a.account_id = ? AND a.chat_id = ? AND a.provider_receipt = ?
      ORDER BY a.created_at_ms DESC LIMIT 1`,
		tenantID.String(), accountID.String(), chatID.String(), providerMessageID,
	).Scan(&responseValue, &text)
	if err == nil {
		messageID, parseErr := identity.ParseMessageID(responseValue)
		if parseErr != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "resolve quoted assistant", parseErr)
		}
		if text == "" {
			return nil, nil
		}
		return &conversation.QuotedMessage{ID: messageID, Role: conversation.QuoteAssistant, Text: text}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, storageError("resolve quoted assistant", err)
	}
	var messageValue, senderRefValue string
	err = query.QueryRowContext(ctx, `SELECT e.message_id, e.sender_ref,
        CASE
          WHEN r.reset_at_ms IS NOT NULL AND e.received_at_ms <= r.reset_at_ms
            THEN '[konten pesan sebelum reset tidak disertakan]'
          ELSE COALESCE(NULLIF(h.content_text, ''), NULLIF(e.input_text, ''), '[konten pesan lama tidak lagi disimpan]')
        END
      FROM inbound_events e
      LEFT JOIN history_resets r ON r.tenant_id = e.tenant_id AND r.account_id = e.account_id AND r.chat_id = e.chat_id
      LEFT JOIN history_entries h ON h.tenant_id = e.tenant_id AND h.account_id = e.account_id
        AND h.chat_id = e.chat_id AND h.message_id = e.message_id
      WHERE e.tenant_id = ? AND e.account_id = ? AND e.chat_id = ? AND e.provider_message_id = ?
        AND e.sender_ref IS NOT NULL
      ORDER BY e.received_at_ms DESC LIMIT 1`,
		tenantID.String(), accountID.String(), chatID.String(), providerMessageID,
	).Scan(&messageValue, &senderRefValue, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError("resolve quoted user", err)
	}
	messageID, err := identity.ParseMessageID(messageValue)
	if err != nil {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "resolve quoted user", err)
	}
	senderRef, err := identity.ParseSenderRef(senderRefValue)
	if err != nil {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "resolve quoted user", err)
	}
	if text == "" {
		return nil, nil
	}
	return &conversation.QuotedMessage{ID: messageID, Role: conversation.QuoteUser, SenderRef: senderRef, Text: text}, nil
}

func decodeQuotedMessage(
	message sql.NullString,
	role sql.NullInt64,
	sender sql.NullString,
	text sql.NullString,
) (*conversation.QuotedMessage, error) {
	if !message.Valid && !role.Valid && !sender.Valid && !text.Valid {
		return nil, nil
	}
	if !message.Valid || !role.Valid || !text.Valid {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode quoted message", fmt.Errorf("partial quote metadata"))
	}
	messageID, err := identity.ParseMessageID(message.String)
	if err != nil {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode quoted message", err)
	}
	quote := &conversation.QuotedMessage{ID: messageID, Role: conversation.QuoteRole(role.Int64), Text: text.String}
	if sender.Valid {
		ref, err := identity.ParseSenderRef(sender.String)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode quoted message", err)
		}
		quote.SenderRef = ref
	}
	if (quote.Role == conversation.QuoteUser) != sender.Valid ||
		(quote.Role != conversation.QuoteUser && quote.Role != conversation.QuoteAssistant) {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode quoted message", fmt.Errorf("quote role and sender do not match"))
	}
	return quote, nil
}

func quotedID(quote *conversation.QuotedMessage) any {
	if quote == nil {
		return nil
	}
	return quote.ID.String()
}

func quotedRole(quote *conversation.QuotedMessage) any {
	if quote == nil {
		return nil
	}
	return uint8(quote.Role)
}

func quotedSenderRef(quote *conversation.QuotedMessage) any {
	if quote == nil || quote.SenderRef.IsZero() {
		return nil
	}
	return quote.SenderRef.String()
}

func quotedText(quote *conversation.QuotedMessage) any {
	if quote == nil {
		return nil
	}
	return quote.Text
}

func isHandledState(state int64) bool {
	return state == ignoredTurnState || state == batchedTurnState || state == int64(agent.TurnSucceeded) ||
		state == int64(agent.TurnFailedTerminal) || state == int64(agent.TurnUnknownOutcome)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func isUniqueConstraint(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") || strings.Contains(text, "constraint failed")
}
