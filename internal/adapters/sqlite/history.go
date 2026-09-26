package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/mention"
)

func (store *HistoryStore) ListIfConfigVersion(
	ctx context.Context,
	key agent.Key,
	version agent.ConfigVersion,
	query agent.HistoryQuery,
) (agent.HistoryPage, error) {
	return store.list(ctx, key, version, query, true)
}

// listForTranscript reads the durable transcript without requiring an
// agent_configs row. Passive allowlisted group messages are recorded before
// the bot is invoked, so some chats have history before their first Agent
// configuration exists. Transcript display is read-only and does not need to
// create that configuration as a side effect.
func (store *HistoryStore) listForTranscript(
	ctx context.Context,
	key agent.Key,
	query agent.HistoryQuery,
) (agent.HistoryPage, error) {
	return store.list(ctx, key, 0, query, false)
}

func (store *HistoryStore) list(
	ctx context.Context,
	key agent.Key,
	version agent.ConfigVersion,
	query agent.HistoryQuery,
	guardConfig bool,
) (agent.HistoryPage, error) {
	if err := key.Validate(); err != nil {
		return agent.HistoryPage{}, err
	}
	if (guardConfig && version == 0) || (!guardConfig && version != 0) || query.Limit == 0 || query.Limit > agent.MaxHistoryPageSize {
		return agent.HistoryPage{}, agent.NewError(agent.ErrorInvalidArgument, "list history", errors.New("valid history query and config guard are required"))
	}
	before, err := decodeHistoryCursor(query.Before)
	if err != nil {
		return agent.HistoryPage{}, err
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return agent.HistoryPage{}, storageError("begin history list", err)
	}
	defer tx.Rollback()
	if guardConfig {
		if err := requireConfigVersion(ctx, tx, key, version); err != nil {
			return agent.HistoryPage{}, err
		}
	}
	var resetCutoff int64
	err = tx.QueryRowContext(ctx, `SELECT cutoff_sequence FROM history_resets
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&resetCutoff)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return agent.HistoryPage{}, storageError("load history reset", err)
	}
	through := int64(0)
	if !query.ThroughInvocationID.IsZero() {
		err = tx.QueryRowContext(ctx, `SELECT sequence FROM history_entries
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
            AND role = ? AND sequence > ?`,
			key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), query.ThroughInvocationID.String(),
			uint8(agent.HistoryUser), resetCutoff,
		).Scan(&through)
		if errors.Is(err, sql.ErrNoRows) {
			return agent.HistoryPage{}, agent.NewError(agent.ErrorIntegrityFailure, "bound history context", errors.New("current invocation history is missing or reset"))
		}
		if err != nil {
			return agent.HistoryPage{}, storageError("bound history context", err)
		}
	}
	args := []any{key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), resetCutoff}
	bound := ""
	if before > 0 {
		bound = " AND sequence < ?"
		args = append(args, before)
	}
	if through > 0 {
		bound = " AND sequence <= ?"
		args = append(args, through)
	}
	args = append(args, int64(query.Limit)+1)
	rows, err := tx.QueryContext(ctx, `SELECT sequence, message_id, invocation_id, causation_kind,
	    causation_id, role, participant_id, sender_ref, sender_name, sender_is_admin, sender_is_super_admin, quoted_message_id,
	    quoted_sequence, quoted_role, quoted_sender_ref, quoted_text, quoted_sender_is_admin, quoted_sender_is_super_admin, content_text,
	    content_digest, delivery_status, created_at_ms
      FROM history_entries
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND sequence > ?`+bound+`
      ORDER BY sequence DESC LIMIT ?`, args...)
	if err != nil {
		return agent.HistoryPage{}, storageError("query history", err)
	}
	defer rows.Close()
	type sequencedEntry struct {
		sequence int64
		entry    agent.HistoryEntry
		digest   []byte
	}
	loaded := make([]sequencedEntry, 0, int(query.Limit)+1)
	for rows.Next() {
		entry, sequence, digest, decodeErr := scanHistoryEntry(rows)
		if decodeErr != nil {
			return agent.HistoryPage{}, decodeErr
		}
		loaded = append(loaded, sequencedEntry{sequence: sequence, entry: entry, digest: digest})
	}
	if err := rows.Err(); err != nil {
		return agent.HistoryPage{}, storageError("iterate history", err)
	}
	if err := rows.Close(); err != nil {
		return agent.HistoryPage{}, storageError("close history rows", err)
	}
	messageIDs := make([]identity.MessageID, 0, len(loaded)*2)
	for index := range loaded {
		messageIDs = append(messageIDs, loaded[index].entry.MessageID)
		if quote := loaded[index].entry.Quote; quote != nil && quote.Role == agent.HistoryUser {
			messageIDs = append(messageIDs, quote.MessageID)
		}
	}
	bindings, err := loadMessageMentionContexts(ctx, tx, key, messageIDs)
	if err != nil {
		return agent.HistoryPage{}, err
	}
	for index := range loaded {
		hydrateHistoryMentions(&loaded[index].entry, bindings)
		wantedDigest, digestErr := agent.DigestHistoryEntry(loaded[index].entry)
		if digestErr != nil {
			return agent.HistoryPage{}, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", digestErr)
		}
		if !equalRawDigest(loaded[index].digest, wantedDigest[:]) {
			return agent.HistoryPage{}, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", errors.New("content digest mismatch"))
		}
	}
	if err := tx.Commit(); err != nil {
		return agent.HistoryPage{}, storageError("commit history list", err)
	}
	more := len(loaded) > int(query.Limit)
	if more {
		loaded = loaded[:query.Limit]
	}
	page := agent.HistoryPage{Entries: make([]agent.HistoryEntry, len(loaded))}
	for index := range loaded {
		page.Entries[len(loaded)-1-index] = loaded[index].entry
	}
	if more && len(loaded) > 0 {
		cursor := encodeHistoryCursor(loaded[len(loaded)-1].sequence)
		page.Next = &cursor
	}
	return page, nil
}

func (store *HistoryStore) Append(ctx context.Context, key agent.Key, entry agent.HistoryEntry) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if _, err := agent.DigestHistoryEntry(entry); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin history append", err)
	}
	defer tx.Rollback()
	if err := ensureScope(ctx, tx, key, store.clock.Now().UnixMilli()); err != nil {
		return err
	}
	var resetAtMS, receivedAtMS sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT r.reset_at_ms, e.received_at_ms
      FROM history_resets r
      LEFT JOIN inbound_events e ON e.tenant_id = r.tenant_id AND e.account_id = r.account_id
        AND e.chat_id = r.chat_id AND e.invocation_id = ?
      WHERE r.tenant_id = ? AND r.account_id = ? AND r.chat_id = ?`,
		entry.InvocationID.String(), key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&resetAtMS, &receivedAtMS)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return storageError("guard history append against reset", err)
	}
	if err == nil && receivedAtMS.Valid && receivedAtMS.Int64 <= resetAtMS.Int64 {
		return agent.NewError(agent.ErrorConflict, "append history", errors.New("inbound turn predates the latest history reset"))
	}
	if err := store.Store.appendHistoryEntryTx(ctx, tx, key, entry, store.clock.Now().UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit history append", err)
	}
	return nil
}

// appendHistoryEntryTx writes one immutable transcript entry. The caller owns
// the transaction and decides any reset visibility guard before calling it.
func (store *Store) appendHistoryEntryTx(
	ctx context.Context,
	tx *sql.Tx,
	key agent.Key,
	entry agent.HistoryEntry,
	nowMS int64,
) error {
	bindings, err := loadMessageMentionContexts(ctx, tx, key, historyMentionMessageIDs(entry))
	if err != nil {
		return err
	}
	storedContent, storedQuote := mentionsForHistoryEntry(entry, bindings)
	if len(entry.Mentions) > 0 && !sameMentionIdentities(entry.Mentions, storedContent) {
		return agent.NewError(agent.ErrorConflict, "append history", errors.New("message mention bindings conflict with durable inbound metadata"))
	}
	if entry.Quote != nil && len(entry.Quote.Mentions) > 0 && !sameMentionIdentities(entry.Quote.Mentions, storedQuote) {
		return agent.NewError(agent.ErrorConflict, "append history", errors.New("quoted mention bindings conflict with durable inbound metadata"))
	}
	entry.Mentions = storedContent
	if entry.Quote != nil {
		entry.Quote = cloneAgentQuote(entry.Quote)
		entry.Quote.Mentions = storedQuote
	}
	digest, err := agent.DigestHistoryEntry(entry)
	if err != nil {
		return err
	}
	var participantID, senderRef any
	var quotedMessageID, quotedSequence, quotedRole, quotedSenderRef, quotedText any
	var quotedSenderIsAdmin, quotedSenderIsSuperAdmin int
	senderName := ""
	senderIsAdmin, senderIsSuperAdmin := 0, 0
	if entry.Sender != nil {
		if err := ensureInternalSender(ctx, tx, key, *entry.Sender, nowMS); err != nil {
			return err
		}
		participantID = entry.Sender.ParticipantID.String()
		senderRef = entry.Sender.Ref.String()
		senderName = entry.Sender.DisplayName
		if entry.Sender.IsAdmin {
			senderIsAdmin = 1
		}
		if entry.Sender.IsSuperAdmin {
			senderIsSuperAdmin = 1
		}
	}
	if entry.Quote != nil {
		quotedMessageID = entry.Quote.MessageID.String()
		if entry.Quote.Sequence > 0 {
			quotedSequence = entry.Quote.Sequence
		}
		quotedRole = uint8(entry.Quote.Role)
		quotedText = entry.Quote.Text
		if !entry.Quote.SenderRef.IsZero() {
			quotedSenderRef = entry.Quote.SenderRef.String()
		}
		if entry.Quote.SenderIsAdmin {
			quotedSenderIsAdmin = 1
		}
		if entry.Quote.SenderIsSuperAdmin {
			quotedSenderIsSuperAdmin = 1
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO history_entries(
	    tenant_id, account_id, chat_id, message_id, invocation_id, causation_kind,
	    causation_id, role, participant_id, sender_ref, sender_name, sender_is_admin, sender_is_super_admin,
		quoted_message_id, quoted_sequence, quoted_role, quoted_sender_ref, quoted_text, quoted_sender_is_admin, quoted_sender_is_super_admin, content_text,
	    content_digest, delivery_status, created_at_ms, updated_at_ms
	  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
		entry.MessageID.String(), entry.InvocationID.String(), uint8(entry.Causation.Kind),
		entry.Causation.ID.String(), uint8(entry.Role), participantID, senderRef, senderName, senderIsAdmin, senderIsSuperAdmin,
		quotedMessageID, quotedSequence, quotedRole, quotedSenderRef, quotedText, quotedSenderIsAdmin, quotedSenderIsSuperAdmin,
		flattenText(entry.Content), digest[:], uint8(entry.Delivery), entry.CreatedAt.UTC().UnixMilli(), nowMS,
	)
	if err != nil {
		return storageError("append history", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storageError("inspect history append", err)
	}
	if changed == 0 {
		rows, err := tx.QueryContext(ctx, `SELECT sequence, content_digest FROM history_entries
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
            AND (message_id = ? OR (invocation_id = ? AND role = ?))`,
			key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
			entry.MessageID.String(), entry.InvocationID.String(), uint8(entry.Role),
		)
		if err != nil {
			return storageError("inspect history replay", err)
		}
		matches := 0
		for rows.Next() {
			var sequence int64
			var stored []byte
			if err := rows.Scan(&sequence, &stored); err != nil {
				rows.Close()
				return storageError("decode history replay", err)
			}
			matches++
			if !equalRawDigest(stored, digest[:]) {
				rows.Close()
				return agent.NewError(agent.ErrorConflict, "append history", errors.New("message or invocation identity is bound to different content"))
			}
		}
		if err := rows.Close(); err != nil {
			return storageError("close history replay", err)
		}
		if matches != 1 {
			return agent.NewError(agent.ErrorConflict, "append history", errors.New("history identity collision"))
		}
	}
	return nil
}

func (store *HistoryStore) ResetIfConfigVersion(
	ctx context.Context,
	key agent.Key,
	version agent.ConfigVersion,
	resetAt time.Time,
) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if version == 0 || resetAt.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "reset history", errors.New("config version and reset time are required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin history reset", err)
	}
	defer tx.Rollback()
	if err := requireConfigVersion(ctx, tx, key, version); err != nil {
		return err
	}
	var cutoff int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM history_entries
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&cutoff); err != nil {
		return storageError("find history reset cutoff", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO history_resets(
        tenant_id, account_id, chat_id, cutoff_sequence, config_version, reset_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?)
      ON CONFLICT(tenant_id, account_id, chat_id) DO UPDATE SET
        cutoff_sequence = MAX(history_resets.cutoff_sequence, excluded.cutoff_sequence),
        config_version = excluded.config_version,
        reset_at_ms = MAX(history_resets.reset_at_ms, excluded.reset_at_ms)`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), cutoff, uint64(version), resetAt.UTC().UnixMilli(),
	)
	if err != nil {
		return storageError("write history reset tombstone", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        turn_state = ?, ignored_reason = 'history_reset',
        generation_lease = NULL, generation_lease_until_ms = NULL, retry_after_ms = NULL,
        updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id IS NULL
        AND received_at_ms <= (SELECT reset_at_ms FROM history_resets
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ?) AND (
          batch_ready_at_ms IS NOT NULL OR batch_anchor_invocation_id IS NOT NULL OR
          turn_state IN (?, ?)
        )`,
		ignoredTurnState, resetAt.UTC().UnixMilli(),
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
		uint8(agent.TurnGenerating), uint8(agent.TurnFailedRetryable),
	); err != nil {
		return storageError("cancel pre-reset inbound turns", err)
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit history reset", err)
	}
	return nil
}

func (store *HistoryStore) Trim(ctx context.Context, key agent.Key, policy agent.RetentionPolicy) (agent.TrimResult, error) {
	if err := key.Validate(); err != nil {
		return agent.TrimResult{}, err
	}
	if (policy.KeepLatest == 0 && policy.MaxAge <= 0) || policy.KeepLatest > 1_000_000 || policy.MaxAge < 0 {
		return agent.TrimResult{}, agent.NewError(agent.ErrorInvalidArgument, "trim history", errors.New("retention bounds are invalid"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.TrimResult{}, storageError("begin history trim", err)
	}
	defer tx.Rollback()
	var resetCutoff int64
	err = tx.QueryRowContext(ctx, `SELECT cutoff_sequence FROM history_resets
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&resetCutoff)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return agent.TrimResult{}, storageError("load history trim cutoff", err)
	}
	var removed uint64
	result, err := tx.ExecContext(ctx, `DELETE FROM history_entries
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND sequence <= ?
	        AND delivery_status != ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), resetCutoff,
		uint8(agent.DeliveryPending),
	)
	if err != nil {
		return agent.TrimResult{}, storageError("trim reset history", err)
	}
	if count, countErr := result.RowsAffected(); countErr == nil && count > 0 {
		removed += uint64(count)
	}

	clauses := []string{"tenant_id = ?", "account_id = ?", "chat_id = ?", "sequence > ?", "delivery_status != ?"}
	args := []any{key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), resetCutoff,
		uint8(agent.DeliveryPending)}
	if policy.KeepLatest > 0 {
		var keepFrom int64
		err := tx.QueryRowContext(ctx, `SELECT sequence FROM history_entries
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND sequence > ?
          ORDER BY sequence DESC LIMIT 1 OFFSET ?`,
			key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), resetCutoff, int64(policy.KeepLatest)-1,
		).Scan(&keepFrom)
		if errors.Is(err, sql.ErrNoRows) {
			keepFrom = 0
		} else if err != nil {
			return agent.TrimResult{}, storageError("find retained history window", err)
		}
		if keepFrom > 0 {
			clauses = append(clauses, "sequence < ?")
			args = append(args, keepFrom)
		} else {
			// Fewer than KeepLatest visible entries: no visible entry is removable.
			clauses = append(clauses, "0 = 1")
		}
	}
	if policy.MaxAge > 0 {
		clauses = append(clauses, "created_at_ms < ?")
		args = append(args, store.clock.Now().Add(-policy.MaxAge).UnixMilli())
	}
	result, err = tx.ExecContext(ctx, "DELETE FROM history_entries WHERE "+strings.Join(clauses, " AND "), args...)
	if err != nil {
		return agent.TrimResult{}, storageError("trim visible history", err)
	}
	if count, countErr := result.RowsAffected(); countErr == nil && count > 0 {
		removed += uint64(count)
	}
	if err := tx.Commit(); err != nil {
		return agent.TrimResult{}, storageError("commit history trim", err)
	}
	return agent.TrimResult{Removed: removed}, nil
}

type historyScanner interface {
	Scan(...any) error
}

func scanHistoryEntry(scanner historyScanner) (agent.HistoryEntry, int64, []byte, error) {
	var (
		sequence                                      int64
		messageValue                                  string
		invocationValue                               string
		causationKind                                 uint8
		causationValue                                string
		role                                          uint8
		participant                                   sql.NullString
		senderRefValue                                sql.NullString
		senderName                                    string
		senderIsAdmin, senderIsSuperAdmin             int64
		quotedMessage                                 sql.NullString
		quotedSequence                                sql.NullInt64
		quotedRole                                    sql.NullInt64
		quotedSenderRef                               sql.NullString
		quotedText                                    sql.NullString
		quotedSenderIsAdmin, quotedSenderIsSuperAdmin int64
		content                                       string
		contentDigest                                 []byte
		delivery                                      uint8
		createdAtMS                                   int64
	)
	if err := scanner.Scan(&sequence, &messageValue, &invocationValue, &causationKind, &causationValue,
		&role, &participant, &senderRefValue, &senderName, &senderIsAdmin, &senderIsSuperAdmin,
		&quotedMessage, &quotedSequence, &quotedRole, &quotedSenderRef, &quotedText,
		&quotedSenderIsAdmin, &quotedSenderIsSuperAdmin, &content, &contentDigest, &delivery, &createdAtMS); err != nil {
		return agent.HistoryEntry{}, 0, nil, storageError("scan history entry", err)
	}
	messageID, err := identity.ParseMessageID(messageValue)
	if err != nil {
		return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", err)
	}
	invocationID, err := identity.ParseInvocationID(invocationValue)
	if err != nil {
		return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", err)
	}
	causationID, err := identity.ParseCausationID(causationValue)
	if err != nil {
		return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", err)
	}
	entry := agent.HistoryEntry{
		Sequence: uint64(sequence), MessageID: messageID, InvocationID: invocationID,
		Causation: agent.CausationRef{Kind: agent.CausationKind(causationKind), ID: causationID},
		Role:      agent.HistoryRole(role), Content: []agent.ContentPart{agent.TextPart{Text: content}},
		Delivery: agent.DeliveryStatus(delivery), CreatedAt: time.UnixMilli(createdAtMS).UTC(),
	}
	if participant.Valid != senderRefValue.Valid || (!participant.Valid && (senderIsAdmin != 0 || senderIsSuperAdmin != 0)) {
		return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", errors.New("partial sender identity"))
	}
	if participant.Valid {
		participantID, parseErr := identity.ParseParticipantID(participant.String)
		if parseErr != nil {
			return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", parseErr)
		}
		senderRef, parseErr := identity.ParseSenderRef(senderRefValue.String)
		if parseErr != nil {
			return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", parseErr)
		}
		entry.Sender = &agent.SenderContext{
			ParticipantID: participantID, Ref: senderRef, DisplayName: senderName,
			IsAdmin: senderIsAdmin == 1, IsSuperAdmin: senderIsSuperAdmin == 1,
		}
	}
	if quotedMessage.Valid || quotedRole.Valid || quotedSenderRef.Valid || quotedText.Valid || quotedSenderIsAdmin != 0 || quotedSenderIsSuperAdmin != 0 {
		if !quotedMessage.Valid || !quotedRole.Valid || !quotedText.Valid {
			return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", errors.New("partial quote context"))
		}
		quotedMessageID, parseErr := identity.ParseMessageID(quotedMessage.String)
		if parseErr != nil {
			return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", parseErr)
		}
		quoteSequence := uint64(0)
		if quotedSequence.Valid && quotedSequence.Int64 > 0 {
			quoteSequence = uint64(quotedSequence.Int64)
		}
		entry.Quote = &agent.QuoteContext{
			Sequence: quoteSequence, MessageID: quotedMessageID, Role: agent.HistoryRole(quotedRole.Int64), Text: quotedText.String,
			SenderIsAdmin: quotedSenderIsAdmin == 1, SenderIsSuperAdmin: quotedSenderIsSuperAdmin == 1,
		}
		if quotedSenderRef.Valid {
			ref, parseErr := identity.ParseSenderRef(quotedSenderRef.String)
			if parseErr != nil {
				return agent.HistoryEntry{}, 0, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history entry", parseErr)
			}
			entry.Quote.SenderRef = ref
		}
	}
	return entry, sequence, append([]byte(nil), contentDigest...), nil
}

func loadMessageMentionContexts(
	ctx context.Context,
	query actionQuerier,
	key agent.Key,
	messageIDs []identity.MessageID,
) (map[string][]agent.MentionContext, error) {
	unique := make([]string, 0, len(messageIDs))
	seen := make(map[string]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		if messageID.IsZero() {
			continue
		}
		value := messageID.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	result := make(map[string][]agent.MentionContext, len(unique))
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, 0, len(unique)+3)
	args = append(args, key.TenantID.String(), key.AccountID.String(), key.ChatID.String())
	for _, messageID := range unique {
		args = append(args, messageID)
	}
	rows, err := query.QueryContext(ctx, `SELECT m.message_id, m.token, m.sender_ref, m.is_bot,
        COALESCE(r.display_name, '')
      FROM message_mentions m
      LEFT JOIN sender_refs r ON r.tenant_id = m.tenant_id AND r.account_id = m.account_id
        AND r.chat_id = m.chat_id AND r.sender_ref = m.sender_ref
      WHERE m.tenant_id = ? AND m.account_id = ? AND m.chat_id = ?
        AND m.message_id IN (`+placeholders+`)
      ORDER BY m.message_id, m.ordinal`, args...)
	if err != nil {
		return nil, storageError("load history mention bindings", err)
	}
	defer rows.Close()
	for rows.Next() {
		var messageID, token, displayName string
		var senderRefValue sql.NullString
		var isBot int64
		if err := rows.Scan(&messageID, &token, &senderRefValue, &isBot, &displayName); err != nil {
			return nil, storageError("decode history mention binding", err)
		}
		if !mention.ValidToken(token) || (isBot != 0 && isBot != 1) || (isBot == 1) != !senderRefValue.Valid {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history mention binding", errors.New("mention metadata is invalid"))
		}
		binding := agent.MentionContext{Token: token, DisplayName: displayName, Bot: isBot == 1}
		if senderRefValue.Valid {
			ref, parseErr := identity.ParseSenderRef(senderRefValue.String)
			if parseErr != nil {
				return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode history mention binding", parseErr)
			}
			binding.SenderRef = ref
		}
		result[messageID] = append(result[messageID], binding)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("iterate history mention bindings", err)
	}
	return result, nil
}

func historyMentionMessageIDs(entry agent.HistoryEntry) []identity.MessageID {
	result := []identity.MessageID{entry.MessageID}
	if entry.Quote != nil && entry.Quote.Role == agent.HistoryUser {
		result = append(result, entry.Quote.MessageID)
	}
	return result
}

func mentionsForHistoryEntry(
	entry agent.HistoryEntry,
	bindings map[string][]agent.MentionContext,
) ([]agent.MentionContext, []agent.MentionContext) {
	var content []agent.MentionContext
	if entry.Role == agent.HistoryUser {
		content = append([]agent.MentionContext(nil), bindings[entry.MessageID.String()]...)
	}
	var quoted []agent.MentionContext
	if entry.Quote != nil && entry.Quote.Role == agent.HistoryUser {
		for _, binding := range bindings[entry.Quote.MessageID.String()] {
			if mention.Contains(entry.Quote.Text, binding.Token) {
				quoted = append(quoted, binding)
			}
		}
	}
	return content, quoted
}

func hydrateHistoryMentions(entry *agent.HistoryEntry, bindings map[string][]agent.MentionContext) {
	content, quoted := mentionsForHistoryEntry(*entry, bindings)
	entry.Mentions = content
	if entry.Quote != nil {
		entry.Quote = cloneAgentQuote(entry.Quote)
		entry.Quote.Mentions = quoted
	}
}

func cloneAgentQuote(quote *agent.QuoteContext) *agent.QuoteContext {
	if quote == nil {
		return nil
	}
	copyQuote := *quote
	copyQuote.Mentions = append([]agent.MentionContext(nil), quote.Mentions...)
	return &copyQuote
}

func sameMentionIdentities(left, right []agent.MentionContext) bool {
	if len(left) != len(right) {
		return false
	}
	wanted := make(map[string]agent.MentionContext, len(left))
	for _, binding := range left {
		wanted[binding.Token] = binding
	}
	for _, binding := range right {
		other, exists := wanted[binding.Token]
		if !exists || other.Bot != binding.Bot || other.SenderRef != binding.SenderRef {
			return false
		}
	}
	return true
}

func requireConfigVersion(ctx context.Context, tx *sql.Tx, key agent.Key, wanted agent.ConfigVersion) error {
	var current uint64
	err := tx.QueryRowContext(ctx, `SELECT version FROM agent_configs
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.NewError(agent.ErrorNotFound, "guard history config", errors.New("agent config does not exist"))
	}
	if err != nil {
		return storageError("guard history config", err)
	}
	if agent.ConfigVersion(current) != wanted {
		return agent.NewError(agent.ErrorConflict, "guard history config", errors.New("authorized config version is stale"))
	}
	return nil
}

func encodeHistoryCursor(sequence int64) agent.HistoryCursor {
	return agent.HistoryCursor("h1_" + strconv.FormatInt(sequence, 36))
}

func decodeHistoryCursor(cursor agent.HistoryCursor) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	text := string(cursor)
	if !strings.HasPrefix(text, "h1_") {
		return 0, agent.NewError(agent.ErrorInvalidArgument, "decode history cursor", errors.New("cursor is invalid"))
	}
	sequence, err := strconv.ParseInt(strings.TrimPrefix(text, "h1_"), 36, 64)
	if err != nil || sequence <= 0 {
		return 0, agent.NewError(agent.ErrorInvalidArgument, "decode history cursor", errors.New("cursor is invalid"))
	}
	return sequence, nil
}

func equalRawDigest(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
