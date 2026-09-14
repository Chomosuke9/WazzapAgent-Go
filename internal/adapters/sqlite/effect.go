package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func (store *EffectStore) Plan(ctx context.Context, request effect.PlanRequest, now time.Time) (effect.Stored, error) {
	if err := request.Validate(); err != nil || now.IsZero() {
		return effect.Stored{}, agent.NewError(agent.ErrorInvalidArgument, "plan typed effect", fmt.Errorf("valid request and current time are required"))
	}
	digest, err := effect.DigestPlan(request)
	if err != nil {
		return effect.Stored{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return effect.Stored{}, storageError("begin typed effect plan", err)
	}
	defer tx.Rollback()
	if err := requireEffectChat(ctx, tx, request.Ref.Key); err != nil {
		return effect.Stored{}, err
	}
	if target, targeted := effectTarget(request.Effect); targeted {
		if err := requireEffectTarget(ctx, tx, request.Ref.Key, target); err != nil {
			return effect.Stored{}, err
		}
	}
	principalParticipant, principalLID, principalInvocation := storedPrincipal(request.Principal)
	target, emoji, presence, commandText := storedEffect(request.Effect)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO typed_effects(
        tenant_id, account_id, chat_id, effect_id, invocation_id,
        principal_kind, principal_participant_id, principal_lid, principal_invocation_id,
		effect_kind, target_message_id, emoji, presence_state, command_text, payload_digest, state,
        created_at_ms, updated_at_ms
	  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.Ref.Key.TenantID.String(), request.Ref.Key.AccountID.String(), request.Ref.Key.ChatID.String(), request.Ref.EffectID.String(), request.InvocationID.String(),
		uint8(request.Principal.Kind), principalParticipant, principalLID, principalInvocation,
		uint8(request.Effect.Kind()), target, emoji, presence, commandText, digest[:], uint8(effect.StatePending), now.UTC().UnixMilli(), now.UTC().UnixMilli(),
	)
	if err != nil {
		return effect.Stored{}, storageError("insert typed effect", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return effect.Stored{}, storageError("inspect typed effect plan", err)
	}
	stored, leaseUntil, err := loadEffect(ctx, tx, request.Ref)
	if err != nil {
		return effect.Stored{}, err
	}
	if changed == 0 {
		storedDigest, digestErr := effect.DigestPlan(stored.Request)
		if digestErr != nil || !bytes.Equal(digest[:], storedDigest[:]) {
			return effect.Stored{}, agent.NewError(agent.ErrorConflict, "plan typed effect", errors.New("effect ID is already bound to another payload"))
		}
	}
	if err := tx.Commit(); err != nil {
		return effect.Stored{}, storageError("commit typed effect plan", err)
	}
	if leaseUntil.Valid {
		// Plan is allowed to observe a raced in-flight effect. Do not disclose
		// its private lease to a planning caller.
		stored.Lease = ""
	}
	return stored, nil
}

func (store *EffectStore) Claim(ctx context.Context, ref effect.Ref, now time.Time) (effect.Stored, error) {
	if err := ref.Validate(); err != nil || now.IsZero() {
		return effect.Stored{}, agent.NewError(agent.ErrorInvalidArgument, "claim typed effect", fmt.Errorf("valid reference and current time are required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return effect.Stored{}, storageError("begin typed effect claim", err)
	}
	defer tx.Rollback()
	stored, leaseUntil, err := loadEffect(ctx, tx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return effect.Stored{}, agent.NewError(agent.ErrorNotFound, "claim typed effect", errors.New("effect does not exist"))
	}
	if err != nil {
		return effect.Stored{}, err
	}
	ready, err := modelEffectResponseDelivered(ctx, tx, ref)
	if err != nil {
		return effect.Stored{}, err
	}
	if !ready && (stored.State == effect.StatePending || stored.State == effect.StateClaimed || stored.State == effect.StateExecuting) {
		if err := tx.Commit(); err != nil {
			return effect.Stored{}, storageError("commit blocked model effect claim", err)
		}
		stored.State = effect.StatePending
		stored.Lease = ""
		return stored, nil
	}
	nowMS := now.UTC().UnixMilli()
	switch stored.State {
	case effect.StateSucceeded, effect.StateFailedTerminal, effect.StateUnknownOutcome, effect.StateSkipped:
		if err := tx.Commit(); err != nil {
			return effect.Stored{}, storageError("commit typed effect observation", err)
		}
		return stored, nil
	case effect.StateExecuting:
		if leaseUntil.Valid && leaseUntil.Int64 > nowMS {
			if err := tx.Commit(); err != nil {
				return effect.Stored{}, storageError("commit executing effect observation", err)
			}
			return stored, nil
		}
		state := effect.StateUnknownOutcome
		if !stored.Request.Effect.Durable() {
			state = effect.StateSkipped
		}
		if err := finishEffectTx(ctx, tx, stored, state, agent.ErrorUnknownOutcome, "", nowMS); err != nil {
			return effect.Stored{}, err
		}
		stored.State = state
		completedAt := now.UTC()
		stored.CompletedAt = &completedAt
		if err := tx.Commit(); err != nil {
			return effect.Stored{}, storageError("commit expired typed effect", err)
		}
		return stored, nil
	case effect.StateClaimed:
		if leaseUntil.Valid && leaseUntil.Int64 > nowMS {
			stored.State = effect.StatePending
			stored.Lease = ""
			if err := tx.Commit(); err != nil {
				return effect.Stored{}, storageError("commit claimed effect observation", err)
			}
			return stored, nil
		}
	case effect.StatePending:
		// Claim below.
	default:
		return effect.Stored{}, agent.NewError(agent.ErrorIntegrityFailure, "claim typed effect", errors.New("effect state is invalid"))
	}
	lease, err := randomLease("eff")
	if err != nil {
		return effect.Stored{}, agent.NewError(agent.ErrorInternal, "create typed effect lease", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE typed_effects SET
        state = ?, effect_lease = ?, effect_lease_until_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?
        AND state = ?`,
		uint8(effect.StateClaimed), lease, now.Add(store.actionTTL).UTC().UnixMilli(), nowMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(), uint8(stored.State),
	)
	if err := requireOne(result, err, "claim typed effect"); err != nil {
		return effect.Stored{}, err
	}
	if err := tx.Commit(); err != nil {
		return effect.Stored{}, storageError("commit typed effect claim", err)
	}
	stored.State = effect.StateClaimed
	stored.Lease = effect.Lease(lease)
	return stored, nil
}

// ListRecoverableEffects returns pending work and expired leases only. Active
// executing effects are intentionally excluded: their outcome is ambiguous
// until their lease expires, at which point Claim records unknown/skipped.
func (store *EffectStore) ListRecoverableEffects(ctx context.Context, tenantID identity.TenantID, now time.Time, limit uint32) ([]effect.Ref, error) {
	if tenantID.IsZero() || now.IsZero() || limit == 0 || limit > 10_000 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list recoverable effects", errors.New("tenant, time, and bounded limit are required"))
	}
	rows, err := store.db.QueryContext(ctx, `SELECT account_id, chat_id, effect_id FROM typed_effects
      WHERE tenant_id = ? AND (
        state = ? OR
        (state IN (?, ?) AND effect_lease_until_ms IS NOT NULL AND effect_lease_until_ms <= ?)
      )
		AND (
			model_call_id IS NULL OR EXISTS (
				SELECT 1 FROM outbound_actions
				WHERE outbound_actions.tenant_id = typed_effects.tenant_id
				  AND outbound_actions.account_id = typed_effects.account_id
				  AND outbound_actions.chat_id = typed_effects.chat_id
				  AND outbound_actions.invocation_id = typed_effects.invocation_id
				  AND outbound_actions.state = ?
			)
		)
      ORDER BY updated_at_ms, effect_id
      LIMIT ?`,
		tenantID.String(), uint8(effect.StatePending), uint8(effect.StateClaimed), uint8(effect.StateExecuting), now.UTC().UnixMilli(), uint8(action.StateSucceeded), limit,
	)
	if err != nil {
		return nil, storageError("list recoverable effects", err)
	}
	defer rows.Close()
	refs := make([]effect.Ref, 0)
	for rows.Next() {
		var accountValue, chatValue, effectValue string
		if err := rows.Scan(&accountValue, &chatValue, &effectValue); err != nil {
			return nil, storageError("scan recoverable effect", err)
		}
		accountID, err := identity.ParseAccountID(accountValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable effect", err)
		}
		chatID, err := identity.ParseChatID(chatValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable effect", err)
		}
		effectID, err := identity.ParseEffectID(effectValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable effect", err)
		}
		refs = append(refs, effect.Ref{Key: agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}, EffectID: effectID})
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("iterate recoverable effects", err)
	}
	return refs, nil
}

// modelEffectResponseDelivered prevents the effect-recovery worker from
// performing a model-requested side effect before the same turn's text reply
// has a durable success receipt. Non-model effects have no such dependency.
func modelEffectResponseDelivered(ctx context.Context, query effectQuerier, ref effect.Ref) (bool, error) {
	var modelCall sql.NullString
	err := query.QueryRowContext(ctx, `SELECT model_call_id FROM typed_effects
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?`,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(),
	).Scan(&modelCall)
	if err != nil {
		return false, storageError("read model effect dependency", err)
	}
	if !modelCall.Valid {
		return true, nil
	}
	var completed int
	err = query.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbound_actions
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
        AND invocation_id = (SELECT invocation_id FROM typed_effects
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?)
        AND state = ?`,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(),
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(), uint8(action.StateSucceeded),
	).Scan(&completed)
	if err != nil {
		return false, storageError("read model effect response dependency", err)
	}
	return completed == 1, nil
}

func (store *EffectStore) MarkExecuting(ctx context.Context, ref effect.Ref, lease effect.Lease, now time.Time) error {
	if err := ref.Validate(); err != nil || lease == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "start typed effect", fmt.Errorf("reference, lease, and time are required"))
	}
	result, err := store.db.ExecContext(ctx, `UPDATE typed_effects SET state = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?
        AND state = ? AND effect_lease = ? AND effect_lease_until_ms > ?`,
		uint8(effect.StateExecuting), now.UTC().UnixMilli(), ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(),
		uint8(effect.StateClaimed), string(lease), now.UTC().UnixMilli(),
	)
	return requireOne(result, err, "start typed effect")
}

func (store *EffectStore) Requeue(ctx context.Context, ref effect.Ref, lease effect.Lease, now time.Time) error {
	if err := ref.Validate(); err != nil || lease == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "requeue typed effect", errors.New("reference, lease, and time are required"))
	}
	result, err := store.db.ExecContext(ctx, `UPDATE typed_effects SET
        state = ?, effect_lease = NULL, effect_lease_until_ms = NULL, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?
        AND state = ? AND effect_lease = ? AND effect_lease_until_ms > ?`,
		uint8(effect.StatePending), now.UTC().UnixMilli(), ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(),
		uint8(effect.StateClaimed), string(lease), now.UTC().UnixMilli(),
	)
	return requireOne(result, err, "requeue typed effect")
}

func (store *EffectStore) Complete(ctx context.Context, ref effect.Ref, lease effect.Lease, receipt string, now time.Time) error {
	return store.finish(ctx, ref, lease, effect.StateSucceeded, "", receipt, now)
}

func (store *EffectStore) FailTerminal(ctx context.Context, ref effect.Ref, lease effect.Lease, code agent.ErrorCode, now time.Time) error {
	return store.finish(ctx, ref, lease, effect.StateFailedTerminal, code, "", now)
}

func (store *EffectStore) MarkUnknown(ctx context.Context, ref effect.Ref, lease effect.Lease, code agent.ErrorCode, now time.Time) error {
	return store.finish(ctx, ref, lease, effect.StateUnknownOutcome, code, "", now)
}

func (store *EffectStore) Skip(ctx context.Context, ref effect.Ref, lease effect.Lease, code agent.ErrorCode, now time.Time) error {
	return store.finish(ctx, ref, lease, effect.StateSkipped, code, "", now)
}

func (store *EffectStore) finish(ctx context.Context, ref effect.Ref, lease effect.Lease, state effect.State, code agent.ErrorCode, receipt string, now time.Time) error {
	if err := ref.Validate(); err != nil || lease == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "finish typed effect", errors.New("reference, lease, and time are required"))
	}
	if state != effect.StateSucceeded && code == "" {
		return agent.NewError(agent.ErrorInvalidArgument, "finish typed effect", errors.New("terminal errors need a code"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin typed effect completion", err)
	}
	defer tx.Rollback()
	stored, _, err := loadEffect(ctx, tx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.NewError(agent.ErrorNotFound, "finish typed effect", errors.New("effect does not exist"))
	}
	if err != nil {
		return err
	}
	if stored.State == state && stored.CompletedAt != nil {
		return tx.Commit()
	}
	if stored.State != effect.StateExecuting || stored.Lease != lease {
		return agent.NewError(agent.ErrorConflict, "finish typed effect", errors.New("effect execution lease changed"))
	}
	if err := finishEffectTx(ctx, tx, stored, state, code, receipt, now.UTC().UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit typed effect completion", err)
	}
	return nil
}

func requireEffectChat(ctx context.Context, tx *sql.Tx, key agent.Key) error {
	var value int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM chats WHERE tenant_id = ? AND account_id = ? AND id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.NewError(agent.ErrorNotFound, "plan typed effect", errors.New("chat does not exist"))
	}
	if err != nil {
		return storageError("read typed effect chat", err)
	}
	return nil
}

func requireEffectTarget(ctx context.Context, tx *sql.Tx, key agent.Key, target identity.MessageID) error {
	var value int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM history_entries
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND message_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), target.String(),
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.NewError(agent.ErrorNotFound, "plan typed effect", errors.New("target message does not belong to chat"))
	}
	if err != nil {
		return storageError("validate typed effect target", err)
	}
	return nil
}

func effectTarget(value effect.Effect) (identity.MessageID, bool) {
	switch typed := value.(type) {
	case effect.React:
		return typed.TargetMessageID, true
	case effect.DeleteMessage:
		return typed.TargetMessageID, true
	case effect.MarkRead:
		return typed.TargetMessageID, true
	case effect.RunGroupCommand:
		if !typed.TargetMessageID.IsZero() {
			return typed.TargetMessageID, true
		}
	default:
		return identity.MessageID{}, false
	}
	return identity.MessageID{}, false
}

func storedPrincipal(principal policy.Principal) (participantID, lid, invocationID any) {
	switch principal.Kind {
	case policy.PrincipalHuman:
		return principal.ParticipantID.String(), principal.LID.String(), nil
	case policy.PrincipalModel, policy.PrincipalRecovery:
		return nil, nil, principal.InvocationID.String()
	default:
		return nil, nil, nil
	}
}

func storedEffect(value effect.Effect) (target, emoji, presence, commandText any) {
	switch typed := value.(type) {
	case effect.React:
		return typed.TargetMessageID.String(), typed.Emoji, nil, nil
	case effect.DeleteMessage:
		return typed.TargetMessageID.String(), nil, nil, nil
	case effect.MarkRead:
		return typed.TargetMessageID.String(), nil, nil, nil
	case effect.SetChatPresence:
		return nil, nil, string(typed.State), nil
	case effect.RunGroupCommand:
		return nullableMessageID(typed.TargetMessageID), nil, nil, typed.Command
	default:
		return nil, nil, nil, nil
	}
}

func nullableMessageID(value identity.MessageID) any {
	if value.IsZero() {
		return nil
	}
	return value.String()
}

type effectQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadEffect(ctx context.Context, query effectQuerier, ref effect.Ref) (effect.Stored, sql.NullInt64, error) {
	var (
		invocationValue                                         string
		principalParticipant, principalLID, principalInvocation sql.NullString
		principalKind, effectKind, state                        int64
		target, emoji, presence, commandText, lease, receipt    sql.NullString
		payloadDigest                                           []byte
		completedAt, leaseUntil                                 sql.NullInt64
	)
	err := query.QueryRowContext(ctx, `SELECT invocation_id, principal_kind, principal_participant_id, principal_lid, principal_invocation_id,
		effect_kind, target_message_id, emoji, presence_state, command_text, payload_digest, state, effect_lease,
        effect_lease_until_ms, provider_receipt, completed_at_ms
      FROM typed_effects WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?`,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(),
	).Scan(&invocationValue, &principalKind, &principalParticipant, &principalLID, &principalInvocation,
		&effectKind, &target, &emoji, &presence, &commandText, &payloadDigest, &state, &lease, &leaseUntil, &receipt, &completedAt)
	if err != nil {
		return effect.Stored{}, leaseUntil, err
	}
	invocationID, err := identity.ParseInvocationID(invocationValue)
	if err != nil {
		return effect.Stored{}, leaseUntil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", err)
	}
	principal, err := decodeEffectPrincipal(ref.Key, policy.PrincipalKind(principalKind), principalParticipant, principalLID, principalInvocation)
	if err != nil {
		return effect.Stored{}, leaseUntil, err
	}
	payload, err := decodeStoredEffect(effect.Kind(effectKind), target, emoji, presence, commandText)
	if err != nil {
		return effect.Stored{}, leaseUntil, err
	}
	stored := effect.Stored{Request: effect.PlanRequest{Ref: ref, InvocationID: invocationID, Principal: principal, Effect: payload}, State: effect.State(state), ProviderReceipt: receipt.String}
	if err := stored.Request.Validate(); err != nil {
		return effect.Stored{}, leaseUntil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", err)
	}
	wantedDigest, err := effect.DigestPlan(stored.Request)
	if err != nil || len(payloadDigest) != len(wantedDigest) || !bytes.Equal(payloadDigest, wantedDigest[:]) {
		return effect.Stored{}, leaseUntil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", errors.New("payload digest mismatch"))
	}
	if stored.State < effect.StatePending || stored.State > effect.StateSkipped {
		return effect.Stored{}, leaseUntil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", errors.New("effect state is invalid"))
	}
	if lease.Valid {
		stored.Lease = effect.Lease(lease.String)
	}
	if completedAt.Valid {
		value := time.UnixMilli(completedAt.Int64).UTC()
		stored.CompletedAt = &value
	}
	return stored, leaseUntil, nil
}

func decodeEffectPrincipal(key agent.Key, kind policy.PrincipalKind, participant, lid, invocation sql.NullString) (policy.Principal, error) {
	principal := policy.Principal{Kind: kind, TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID}
	var err error
	switch kind {
	case policy.PrincipalHuman:
		principal.ParticipantID, err = identity.ParseParticipantID(participant.String)
		if err == nil {
			principal.LID, err = identity.ParseLID(lid.String)
		}
	case policy.PrincipalModel, policy.PrincipalRecovery:
		principal.InvocationID, err = identity.ParseInvocationID(invocation.String)
	case policy.PrincipalSystem:
	default:
		err = errors.New("principal kind is invalid")
	}
	if err != nil {
		return policy.Principal{}, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect principal", err)
	}
	if err := principal.Validate(); err != nil {
		return policy.Principal{}, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect principal", err)
	}
	return principal, nil
}

func decodeStoredEffect(kind effect.Kind, target, emoji, presence, commandText sql.NullString) (effect.Effect, error) {
	switch kind {
	case effect.KindReact:
		messageID, err := identity.ParseMessageID(target.String)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode reaction effect", err)
		}
		return effect.React{TargetMessageID: messageID, Emoji: emoji.String}, nil
	case effect.KindDeleteMessage:
		messageID, err := identity.ParseMessageID(target.String)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode delete effect", err)
		}
		return effect.DeleteMessage{TargetMessageID: messageID}, nil
	case effect.KindMarkRead:
		messageID, err := identity.ParseMessageID(target.String)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode mark-read effect", err)
		}
		return effect.MarkRead{TargetMessageID: messageID}, nil
	case effect.KindSetChatPresence:
		return effect.SetChatPresence{State: effect.PresenceState(presence.String)}, nil
	case effect.KindRunGroupCommand:
		var messageID identity.MessageID
		var err error
		if target.Valid {
			messageID, err = identity.ParseMessageID(target.String)
			if err != nil {
				return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode group command effect", err)
			}
		}
		return effect.RunGroupCommand{Command: commandText.String, TargetMessageID: messageID}, nil
	default:
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", errors.New("effect kind is invalid"))
	}
}

func finishEffectTx(ctx context.Context, tx *sql.Tx, stored effect.Stored, state effect.State, code agent.ErrorCode, receipt string, nowMS int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE typed_effects SET
        state = ?, effect_lease = NULL, effect_lease_until_ms = NULL, provider_receipt = ?,
        last_error_code = ?, completed_at_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?
        AND state = ? AND effect_lease = ?`,
		uint8(state), nullableString(receipt), nullableErrorCode(code), nowMS, nowMS,
		stored.Request.Ref.Key.TenantID.String(), stored.Request.Ref.Key.AccountID.String(), stored.Request.Ref.Key.ChatID.String(), stored.Request.Ref.EffectID.String(),
		uint8(effect.StateExecuting), string(stored.Lease),
	)
	return requireOne(result, err, "finish typed effect")
}
