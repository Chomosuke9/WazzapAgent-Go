package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const maxGenerationAttempts = 3

func (store *TurnStore) Claim(ctx context.Context, request agent.ClaimTurnRequest) (agent.TurnClaim, error) {
	if err := request.Key.Validate(); err != nil {
		return agent.TurnClaim{}, err
	}
	if request.Invocation.ID.IsZero() || request.Now.IsZero() {
		return agent.TurnClaim{}, agent.NewError(agent.ErrorInvalidArgument, "claim turn", fmt.Errorf("invocation ID and current time are required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.TurnClaim{}, storageError("begin turn claim", err)
	}
	defer tx.Rollback()
	row, err := loadTurnRow(ctx, tx, request.Key, request.Invocation.ID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := ensureScope(ctx, tx, request.Key, request.Now.UnixMilli()); err != nil {
			return agent.TurnClaim{}, err
		}
		row, err = insertInvocation(ctx, tx, request, store.generationTTL)
		if err != nil {
			return agent.TurnClaim{}, err
		}
		if err := tx.Commit(); err != nil {
			return agent.TurnClaim{}, storageError("commit turn claim", err)
		}
		messageID, parseErr := identity.ParseMessageID(row.messageID)
		if parseErr != nil {
			return agent.TurnClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "decode turn message ID", parseErr)
		}
		return agent.TurnClaim{State: agent.TurnGenerating, Lease: agent.TurnLease(row.generationLease.String), MessageID: messageID}, nil
	}
	if err != nil {
		return agent.TurnClaim{}, storageError("load turn claim", err)
	}
	if !row.digest.Valid {
		if !matchesPreclaimedInbound(row, request) {
			return agent.TurnClaim{}, agent.NewError(agent.ErrorConflict, "claim received turn", fmt.Errorf("invocation does not match durable inbound message"))
		}
		lease, err := randomLease("gen")
		if err != nil {
			return agent.TurnClaim{}, agent.NewError(agent.ErrorInternal, "claim turn", err)
		}
		arguments := []any{
			request.Digest[:], uint64(request.Invocation.PolicyVersion), uint8(agent.TurnGenerating), lease,
			request.Now.Add(store.generationTTL).UnixMilli(), request.Now.UnixMilli(),
		}
		arguments = append(arguments, keyArgs(request.Key, request.Invocation.ID)...)
		result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
            invocation_digest = ?, config_version = ?, turn_state = ?, generation_lease = ?,
            generation_lease_until_ms = ?, retry_after_ms = NULL, generation_attempts = generation_attempts + 1,
            updated_at_ms = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ? AND invocation_digest IS NULL`, arguments...)
		if err != nil {
			return agent.TurnClaim{}, storageError("claim received turn", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return agent.TurnClaim{}, agent.NewError(agent.ErrorConflict, "claim received turn", fmt.Errorf("turn changed concurrently"))
		}
		if err := tx.Commit(); err != nil {
			return agent.TurnClaim{}, storageError("commit received turn claim", err)
		}
		messageID, parseErr := identity.ParseMessageID(row.messageID)
		if parseErr != nil {
			return agent.TurnClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "decode received message ID", parseErr)
		}
		return agent.TurnClaim{State: agent.TurnGenerating, Lease: agent.TurnLease(lease), MessageID: messageID}, nil
	}
	if len(row.digest.Bytes) != sha256.Size || !equalDigest(row.digest.Bytes, request.Digest) {
		return agent.TurnClaim{}, agent.NewError(agent.ErrorConflict, "claim turn", fmt.Errorf("invocation ID is bound to different input"))
	}
	if row.actionID.Valid {
		plan, err := row.plan(request.Key, request.Invocation.ID)
		if err != nil {
			return agent.TurnClaim{}, err
		}
		if err := tx.Commit(); err != nil {
			return agent.TurnClaim{}, storageError("commit replay lookup", err)
		}
		messageID, parseErr := identity.ParseMessageID(row.messageID)
		if parseErr != nil {
			return agent.TurnClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "decode replay message ID", parseErr)
		}
		return agent.TurnClaim{State: agent.TurnState(row.state), MessageID: messageID, Plan: &plan}, nil
	}

	nowMS := request.Now.UnixMilli()
	switch agent.TurnState(row.state) {
	case agent.TurnGenerating:
		if row.generationLease.Valid && row.generationLeaseUntil.Valid && row.generationLeaseUntil.Int64 > nowMS {
			return agent.TurnClaim{}, agent.NewError(agent.ErrorInProgress, "claim turn", fmt.Errorf("generation lease is active"))
		}
	case agent.TurnFailedRetryable:
		if row.retryAfter.Valid && row.retryAfter.Int64 > nowMS {
			return agent.TurnClaim{}, agent.NewError(agent.ErrorInProgress, "claim turn", fmt.Errorf("generation retry is not due"))
		}
	case agent.TurnFailedTerminal:
		return agent.TurnClaim{}, agent.NewError(agent.ErrorProviderFailure, "claim turn", fmt.Errorf("generation failed terminally"))
	case agent.TurnUnknownOutcome:
		return agent.TurnClaim{}, agent.NewError(agent.ErrorUnknownOutcome, "claim turn", fmt.Errorf("turn outcome is unknown"))
	default:
		return agent.TurnClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "claim turn", fmt.Errorf("turn has invalid pre-plan state"))
	}
	if row.generationAttempts < 0 {
		return agent.TurnClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "claim turn", fmt.Errorf("generation attempt count is invalid"))
	}
	if row.generationAttempts >= maxGenerationAttempts {
		result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
            turn_state = ?, generation_lease = NULL, generation_lease_until_ms = NULL,
            retry_after_ms = NULL, last_error_code = ?, updated_at_ms = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
            AND turn_state = ? AND action_id IS NULL`,
			uint8(agent.TurnFailedTerminal), string(agent.ErrorProviderFailure), nowMS,
			request.Key.TenantID.String(), request.Key.AccountID.String(), request.Key.ChatID.String(), request.Invocation.ID.String(), row.state,
		)
		if err := requireOne(result, err, "exhaust generation attempts"); err != nil {
			return agent.TurnClaim{}, err
		}
		if err := tx.Commit(); err != nil {
			return agent.TurnClaim{}, storageError("commit exhausted generation attempts", err)
		}
		return agent.TurnClaim{}, agent.NewError(agent.ErrorProviderFailure, "claim turn", fmt.Errorf("generation retry limit reached"))
	}

	lease, err := randomLease("gen")
	if err != nil {
		return agent.TurnClaim{}, agent.NewError(agent.ErrorInternal, "claim turn", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        turn_state = ?, generation_lease = ?, generation_lease_until_ms = ?, retry_after_ms = NULL,
        generation_attempts = generation_attempts + 1, config_version = ?, last_error_code = NULL, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
        AND invocation_digest = ? AND action_id IS NULL`,
		uint8(agent.TurnGenerating), lease, request.Now.Add(store.generationTTL).UnixMilli(),
		uint64(request.Invocation.PolicyVersion), nowMS,
		request.Key.TenantID.String(), request.Key.AccountID.String(), request.Key.ChatID.String(), request.Invocation.ID.String(), request.Digest[:],
	)
	if err != nil {
		return agent.TurnClaim{}, storageError("renew turn claim", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agent.TurnClaim{}, agent.NewError(agent.ErrorConflict, "renew turn claim", fmt.Errorf("turn changed concurrently"))
	}
	if err := tx.Commit(); err != nil {
		return agent.TurnClaim{}, storageError("commit renewed turn claim", err)
	}
	messageID, parseErr := identity.ParseMessageID(row.messageID)
	if parseErr != nil {
		return agent.TurnClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "decode renewed message ID", parseErr)
	}
	return agent.TurnClaim{State: agent.TurnGenerating, Lease: agent.TurnLease(lease), MessageID: messageID}, nil
}

func (store *TurnStore) CommitPlan(ctx context.Context, request agent.CommitPlanRequest) (agent.StoredPlan, error) {
	if err := request.Key.Validate(); err != nil {
		return agent.StoredPlan{}, err
	}
	if request.InvocationID.IsZero() || request.Lease == "" || request.ConfigVersion == 0 || strings.TrimSpace(request.ResponseText) == "" {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorInvalidArgument, "commit response plan", fmt.Errorf("complete response plan is required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.StoredPlan{}, storageError("begin response plan", err)
	}
	defer tx.Rollback()
	row, err := loadTurnRow(ctx, tx, request.Key, request.InvocationID)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorNotFound, "commit response plan", fmt.Errorf("turn does not exist"))
	}
	if err != nil {
		return agent.StoredPlan{}, storageError("load response plan", err)
	}
	if row.actionID.Valid {
		plan, planErr := row.plan(request.Key, request.InvocationID)
		if planErr != nil {
			return agent.StoredPlan{}, planErr
		}
		if plan.ConfigVersion != request.ConfigVersion || plan.Text != request.ResponseText {
			return agent.StoredPlan{}, agent.NewError(agent.ErrorConflict, "commit response plan", fmt.Errorf("different plan already exists"))
		}
		if err := tx.Commit(); err != nil {
			return agent.StoredPlan{}, storageError("commit response replay", err)
		}
		return plan, nil
	}
	nowMS := store.clock.Now().UTC().UnixMilli()
	createdAt := time.UnixMilli(nowMS).UTC()
	if agent.TurnState(row.state) != agent.TurnGenerating ||
		!row.generationLease.Valid || row.generationLease.String != string(request.Lease) ||
		!row.generationLeaseUntil.Valid || row.generationLeaseUntil.Int64 <= nowMS {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorConflict, "commit response plan", fmt.Errorf("generation lease is not owned"))
	}
	if row.configVersion.Valid && agent.ConfigVersion(row.configVersion.Int64) != request.ConfigVersion {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorConflict, "commit response plan", fmt.Errorf("config version changed"))
	}
	responseID, err := identity.NewMessageID()
	if err != nil {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorInternal, "create response ID", err)
	}
	actionID, err := identity.NewActionID()
	if err != nil {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorInternal, "create action ID", err)
	}
	payloadDigest := digestAction(request.Key, actionID, request.ResponseText)
	causationID, err := identity.ParseCausationID(row.causationID)
	if err != nil {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorIntegrityFailure, "decode response causation", err)
	}
	historyEntry := agent.HistoryEntry{
		MessageID: responseID, InvocationID: request.InvocationID,
		Causation: agent.CausationRef{Kind: agent.CausationKind(row.causationKind), ID: causationID},
		Role:      agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: request.ResponseText}},
		Delivery: agent.DeliveryPending, CreatedAt: createdAt,
	}
	historyDigest, err := agent.DigestHistoryEntry(historyEntry)
	if err != nil {
		return agent.StoredPlan{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbound_actions(
        tenant_id, account_id, chat_id, action_id, invocation_id, response_id,
        payload_digest, text, state, created_at_ms, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.Key.TenantID.String(), request.Key.AccountID.String(), request.Key.ChatID.String(),
		actionID.String(), request.InvocationID.String(), responseID.String(), payloadDigest[:], request.ResponseText,
		uint8(action.StatePending), nowMS, nowMS,
	)
	if err != nil {
		return agent.StoredPlan{}, storageError("insert outbound action", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO action_receipts(
        tenant_id, account_id, chat_id, action_id, status, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?)`,
		request.Key.TenantID.String(), request.Key.AccountID.String(), request.Key.ChatID.String(),
		actionID.String(), uint8(agent.DeliveryPending), nowMS,
	)
	if err != nil {
		return agent.StoredPlan{}, storageError("insert action receipt", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO history_entries(
        tenant_id, account_id, chat_id, message_id, invocation_id, causation_kind,
        causation_id, role, sender_name, content_text, content_digest,
        delivery_status, created_at_ms, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?)`,
		request.Key.TenantID.String(), request.Key.AccountID.String(), request.Key.ChatID.String(),
		responseID.String(), request.InvocationID.String(), uint8(row.causationKind), row.causationID,
		uint8(agent.HistoryAssistant), request.ResponseText, historyDigest[:], uint8(agent.DeliveryPending),
		createdAt.UnixMilli(), nowMS,
	)
	if err != nil {
		return agent.StoredPlan{}, storageError("insert pending assistant history", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        config_version = ?, turn_state = ?, response_id = ?, action_id = ?, response_text = ?,
        delivery_status = ?, generation_lease = NULL, generation_lease_until_ms = NULL, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
        AND turn_state = ? AND generation_lease = ? AND generation_lease_until_ms > ? AND action_id IS NULL`,
		uint64(request.ConfigVersion), uint8(agent.TurnResponsePlanned), responseID.String(), actionID.String(), request.ResponseText,
		uint8(agent.DeliveryPending), nowMS,
		request.Key.TenantID.String(), request.Key.AccountID.String(), request.Key.ChatID.String(), request.InvocationID.String(),
		uint8(agent.TurnGenerating), string(request.Lease), nowMS,
	)
	if err != nil {
		return agent.StoredPlan{}, storageError("publish response plan", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorConflict, "publish response plan", fmt.Errorf("generation lease changed"))
	}
	if err := tx.Commit(); err != nil {
		return agent.StoredPlan{}, storageError("commit response plan", err)
	}
	return agent.StoredPlan{
		InvocationID:  request.InvocationID,
		ConfigVersion: request.ConfigVersion,
		ResponseID:    responseID,
		ActionID:      actionID,
		Text:          request.ResponseText,
		CreatedAt:     createdAt,
		Dispatch:      agent.DispatchRef{Key: request.Key, ActionID: actionID},
	}, nil
}

func (store *TurnStore) FailGeneration(ctx context.Context, request agent.FailGenerationRequest) error {
	if err := request.Key.Validate(); err != nil {
		return err
	}
	if request.InvocationID.IsZero() || request.Lease == "" || request.Code == "" {
		return agent.NewError(agent.ErrorInvalidArgument, "fail generation", fmt.Errorf("complete failure data is required"))
	}
	state := agent.TurnFailedTerminal
	if request.Retryable {
		state = agent.TurnFailedRetryable
		if request.RetryAfter.IsZero() {
			return agent.NewError(agent.ErrorInvalidArgument, "fail generation", fmt.Errorf("retry time is required"))
		}
	}
	nowMS := store.clock.Now().UnixMilli()
	result, err := store.db.ExecContext(ctx, `UPDATE inbound_events SET
        turn_state = ?, generation_lease = NULL, generation_lease_until_ms = NULL,
        retry_after_ms = ?, last_error_code = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
        AND turn_state = ? AND generation_lease = ? AND generation_lease_until_ms > ? AND action_id IS NULL`,
		uint8(state), nullableMillis(request.RetryAfter), string(request.Code), nowMS,
		request.Key.TenantID.String(), request.Key.AccountID.String(), request.Key.ChatID.String(), request.InvocationID.String(),
		uint8(agent.TurnGenerating), string(request.Lease), nowMS,
	)
	if err != nil {
		return storageError("record generation failure", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agent.NewError(agent.ErrorConflict, "record generation failure", fmt.Errorf("generation lease changed"))
	}
	return nil
}

func (store *TurnStore) Load(ctx context.Context, key agent.Key, invocationID identity.InvocationID) (agent.TurnRecord, error) {
	if err := key.Validate(); err != nil {
		return agent.TurnRecord{}, err
	}
	if invocationID.IsZero() {
		return agent.TurnRecord{}, agent.NewError(agent.ErrorInvalidArgument, "load turn", fmt.Errorf("invocation ID is required"))
	}
	row, err := loadTurnRow(ctx, store.db, key, invocationID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !row.digest.Valid) {
		return agent.TurnRecord{}, agent.NewError(agent.ErrorNotFound, "load turn", fmt.Errorf("claimed turn does not exist"))
	}
	if err != nil {
		return agent.TurnRecord{}, storageError("load turn", err)
	}
	if len(row.digest.Bytes) != sha256.Size {
		return agent.TurnRecord{}, agent.NewError(agent.ErrorIntegrityFailure, "decode turn", fmt.Errorf("invalid invocation digest"))
	}
	var digest agent.InvocationDigest
	copy(digest[:], row.digest.Bytes)
	record := agent.TurnRecord{
		Key:          key,
		InvocationID: invocationID,
		Digest:       digest,
		State:        agent.TurnState(row.state),
		Delivery:     agent.DeliveryStatus(row.deliveryStatus),
		UpdatedAt:    time.UnixMilli(row.updatedAt).UTC(),
	}
	messageID, parseErr := identity.ParseMessageID(row.messageID)
	if parseErr != nil {
		return agent.TurnRecord{}, agent.NewError(agent.ErrorIntegrityFailure, "decode turn message ID", parseErr)
	}
	record.MessageID = messageID
	if row.actionID.Valid {
		plan, err := row.plan(key, invocationID)
		if err != nil {
			return agent.TurnRecord{}, err
		}
		record.Plan = &plan
	}
	return record, nil
}

type turnRow struct {
	state                int64
	digest               nullableBytes
	messageID            string
	providerMessageID    sql.NullString
	invocationCause      int64
	causationKind        int64
	causationID          string
	participantID        sql.NullString
	senderRef            sql.NullString
	senderName           string
	inputText            string
	occurredAt           int64
	generationLease      sql.NullString
	generationLeaseUntil sql.NullInt64
	retryAfter           sql.NullInt64
	generationAttempts   int64
	configVersion        sql.NullInt64
	responseID           sql.NullString
	actionID             sql.NullString
	responseText         sql.NullString
	deliveryStatus       int64
	updatedAt            int64
	responseCreatedAt    sql.NullInt64
}

// nullableBytes distinguishes a SQL NULL digest from an empty/corrupt digest.
type nullableBytes struct {
	Bytes []byte
	Valid bool
}

func (value *nullableBytes) Scan(source any) error {
	if source == nil {
		value.Bytes = nil
		value.Valid = false
		return nil
	}
	bytesValue, ok := source.([]byte)
	if !ok {
		return fmt.Errorf("digest has unexpected database type")
	}
	value.Bytes = append(value.Bytes[:0], bytesValue...)
	value.Valid = true
	return nil
}

type turnQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadTurnRow(ctx context.Context, query turnQuerier, key agent.Key, invocationID identity.InvocationID) (turnRow, error) {
	var row turnRow
	var digest nullableBytes
	err := query.QueryRowContext(ctx, `SELECT i.turn_state, i.invocation_digest, i.message_id, i.provider_message_id,
        i.invocation_cause, i.causation_kind, i.causation_id, i.participant_id, i.sender_ref, i.sender_name, i.input_text, i.occurred_at_ms, i.generation_lease,
        i.generation_lease_until_ms, i.retry_after_ms, i.generation_attempts, i.config_version, i.response_id, i.action_id,
        i.response_text, i.delivery_status, i.updated_at_ms, a.created_at_ms
      FROM inbound_events i
      LEFT JOIN outbound_actions a ON a.tenant_id = i.tenant_id AND a.account_id = i.account_id
        AND a.chat_id = i.chat_id AND a.action_id = i.action_id
      WHERE i.tenant_id = ? AND i.account_id = ? AND i.chat_id = ? AND i.invocation_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), invocationID.String(),
	).Scan(&row.state, &digest, &row.messageID, &row.providerMessageID, &row.invocationCause, &row.causationKind,
		&row.causationID, &row.participantID, &row.senderRef,
		&row.senderName, &row.inputText, &row.occurredAt, &row.generationLease, &row.generationLeaseUntil, &row.retryAfter, &row.generationAttempts,
		&row.configVersion, &row.responseID, &row.actionID, &row.responseText, &row.deliveryStatus, &row.updatedAt, &row.responseCreatedAt)
	row.digest = digest
	return row, err
}

func matchesPreclaimedInbound(row turnRow, request agent.ClaimTurnRequest) bool {
	invocation := request.Invocation
	if !row.providerMessageID.Valid || invocation.Cause != agent.CauseInboundMessage ||
		int64(invocation.Cause) != row.invocationCause || int64(invocation.Causation.Kind) != row.causationKind ||
		invocation.Causation.Kind != agent.CausationMessage || row.causationID != invocation.Causation.ID.String() ||
		invocation.Sender == nil || !row.participantID.Valid || !row.senderRef.Valid ||
		row.participantID.String != invocation.Sender.ParticipantID.String() ||
		row.senderRef.String != invocation.Sender.Ref.String() || row.senderName != invocation.Sender.DisplayName ||
		row.inputText != flattenText(invocation.Input) || row.occurredAt != invocation.RequestedAt.UnixMilli() ||
		len(invocation.Capabilities.Values()) != 0 {
		return false
	}
	return true
}

func insertInvocation(ctx context.Context, tx *sql.Tx, request agent.ClaimTurnRequest, ttl time.Duration) (turnRow, error) {
	messageID, err := identity.NewMessageID()
	if err != nil {
		return turnRow{}, agent.NewError(agent.ErrorInternal, "create turn message ID", err)
	}
	lease, err := randomLease("gen")
	if err != nil {
		return turnRow{}, agent.NewError(agent.ErrorInternal, "create generation lease", err)
	}
	inputText := flattenText(request.Invocation.Input)
	var participant, senderRef any
	senderName := ""
	if request.Invocation.Sender != nil {
		if err := ensureInternalSender(ctx, tx, request.Key, *request.Invocation.Sender, request.Now.UnixMilli()); err != nil {
			return turnRow{}, err
		}
		participant = request.Invocation.Sender.ParticipantID.String()
		senderRef = request.Invocation.Sender.Ref.String()
		senderName = request.Invocation.Sender.DisplayName
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO inbound_events(
        tenant_id, account_id, chat_id, invocation_id, message_id, invocation_cause, causation_kind, causation_id,
        participant_id, sender_ref, sender_name, input_text, occurred_at_ms, received_at_ms,
        invocation_digest, config_version, turn_state, generation_lease,
        generation_lease_until_ms, generation_attempts, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		request.Key.TenantID.String(), request.Key.AccountID.String(), request.Key.ChatID.String(),
		request.Invocation.ID.String(), messageID.String(), uint8(request.Invocation.Cause), uint8(request.Invocation.Causation.Kind), request.Invocation.Causation.ID.String(),
		participant, senderRef, senderName, inputText, request.Invocation.RequestedAt.UnixMilli(), request.Now.UnixMilli(),
		request.Digest[:], uint64(request.Invocation.PolicyVersion), uint8(agent.TurnGenerating), lease,
		request.Now.Add(ttl).UnixMilli(), request.Now.UnixMilli(),
	)
	if err != nil {
		return turnRow{}, storageError("insert turn claim", err)
	}
	return turnRow{messageID: messageID.String(), invocationCause: int64(request.Invocation.Cause),
		causationKind: int64(request.Invocation.Causation.Kind), causationID: request.Invocation.Causation.ID.String(),
		generationLease: sql.NullString{String: lease, Valid: true}}, nil
}

func ensureInternalSender(ctx context.Context, tx *sql.Tx, key agent.Key, sender agent.SenderContext, nowMS int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO participants(
        tenant_id, account_id, id, provider_address, created_at_ms
      ) VALUES (?, ?, ?, ?, ?)`,
		key.TenantID.String(), key.AccountID.String(), sender.ParticipantID.String(),
		"internal:"+sender.ParticipantID.String(), nowMS,
	); err != nil {
		return storageError("ensure invocation participant", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO sender_refs(
        tenant_id, account_id, chat_id, participant_id, sender_ref, created_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?)`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
		sender.ParticipantID.String(), sender.Ref.String(), nowMS,
	); err != nil {
		return storageError("ensure invocation sender ref", err)
	}
	var storedRef string
	err := tx.QueryRowContext(ctx, `SELECT sender_ref FROM sender_refs
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND participant_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), sender.ParticipantID.String(),
	).Scan(&storedRef)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && storedRef != sender.Ref.String()) {
		return agent.NewError(agent.ErrorConflict, "ensure invocation sender ref", fmt.Errorf("participant and sender ref are bound to different identities"))
	}
	if err != nil {
		return storageError("verify invocation sender ref", err)
	}
	return nil
}

func (row turnRow) plan(key agent.Key, invocationID identity.InvocationID) (agent.StoredPlan, error) {
	if !row.configVersion.Valid || !row.responseID.Valid || !row.actionID.Valid || !row.responseText.Valid || !row.responseCreatedAt.Valid {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorIntegrityFailure, "decode response plan", fmt.Errorf("partial response plan"))
	}
	responseID, err := identity.ParseMessageID(row.responseID.String)
	if err != nil {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorIntegrityFailure, "decode response plan", err)
	}
	actionID, err := identity.ParseActionID(row.actionID.String)
	if err != nil {
		return agent.StoredPlan{}, agent.NewError(agent.ErrorIntegrityFailure, "decode response plan", err)
	}
	return agent.StoredPlan{
		InvocationID:  invocationID,
		ConfigVersion: agent.ConfigVersion(row.configVersion.Int64),
		ResponseID:    responseID,
		ActionID:      actionID,
		Text:          row.responseText.String,
		CreatedAt:     time.UnixMilli(row.responseCreatedAt.Int64).UTC(),
		Dispatch:      agent.DispatchRef{Key: key, ActionID: actionID},
	}, nil
}

func flattenText(parts []agent.ContentPart) string {
	var builder strings.Builder
	for index, part := range parts {
		if index > 0 {
			builder.WriteString("\n")
		}
		if text, ok := part.(agent.TextPart); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

func randomLease(prefix string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buffer), nil
}

func keyArgs(key agent.Key, invocationID identity.InvocationID) []any {
	return []any{key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), invocationID.String()}
}

func equalDigest(stored []byte, wanted agent.InvocationDigest) bool {
	if len(stored) != len(wanted) {
		return false
	}
	for index := range stored {
		if stored[index] != wanted[index] {
			return false
		}
	}
	return true
}

func digestAction(key agent.Key, actionID identity.ActionID, text string) [32]byte {
	return sha256.Sum256([]byte(key.TenantID.String() + "\x00" + key.AccountID.String() + "\x00" + key.ChatID.String() + "\x00" + actionID.String() + "\x00" + text))
}

func nullableMillis(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UnixMilli()
}
