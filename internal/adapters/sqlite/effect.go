package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
	kind, target, payload, err := encodeEffect(request)
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
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO typed_effects(
        tenant_id, account_id, chat_id, effect_id, invocation_id,
		kind, target_message_id, payload, state, created_at_ms, updated_at_ms
	  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.Ref.Key.TenantID.String(), request.Ref.Key.AccountID.String(), request.Ref.Key.ChatID.String(), request.Ref.EffectID.String(), request.InvocationID.String(),
		kind, target, payload, uint8(effect.StatePending), now.UTC().UnixMilli(), now.UTC().UnixMilli(),
	)
	if err != nil {
		return effect.Stored{}, storageError("insert typed effect", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return effect.Stored{}, storageError("inspect typed effect plan", err)
	}
	stored, err := loadEffect(ctx, tx, request.Ref)
	if err != nil {
		return effect.Stored{}, err
	}
	if changed == 0 {
		_, _, storedPayload, encodeErr := encodeEffect(stored.Request)
		if encodeErr != nil || storedPayload != payload || stored.Request.InvocationID != request.InvocationID {
			return effect.Stored{}, agent.NewError(agent.ErrorConflict, "plan typed effect", errors.New("effect ID is already bound to another payload"))
		}
	}
	if err := tx.Commit(); err != nil {
		return effect.Stored{}, storageError("commit typed effect plan", err)
	}
	return stored, nil
}

func (store *EffectStore) Load(ctx context.Context, ref effect.Ref) (effect.Stored, error) {
	if err := ref.Validate(); err != nil {
		return effect.Stored{}, err
	}
	stored, err := loadEffect(ctx, store.db, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return effect.Stored{}, agent.NewError(agent.ErrorNotFound, "load typed effect", errors.New("effect does not exist"))
	}
	return stored, err
}

// Start claims a pending effect. A model effect waits for the reply it
// follows: until that reply is sent the effect stays pending (not ready).
func (store *EffectStore) Start(ctx context.Context, ref effect.Ref, now time.Time) error {
	if err := ref.Validate(); err != nil || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "start typed effect", fmt.Errorf("reference and time are required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin typed effect start", err)
	}
	defer tx.Rollback()
	ready, err := modelEffectResponseDelivered(ctx, tx, ref)
	if err != nil {
		return err
	}
	if !ready {
		return agent.NewError(agent.ErrorNotReady, "start typed effect", errors.New("the reply this effect follows is not sent yet"))
	}
	result, err := tx.ExecContext(ctx, `UPDATE typed_effects SET state = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ? AND state = ?`,
		uint8(effect.StateExecuting), now.UTC().UnixMilli(),
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(),
		uint8(effect.StatePending),
	)
	if err := requireOne(result, err, "start typed effect"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit typed effect start", err)
	}
	return nil
}

// ListPending returns effects that are ready to run, oldest first: model
// effects only once the reply they follow has been sent. A non-zero
// plannedBefore skips effects planned at or after it.
func (store *EffectStore) ListPending(ctx context.Context, tenantID identity.TenantID, plannedBefore time.Time) ([]effect.Ref, error) {
	if tenantID.IsZero() {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list pending effects", errors.New("tenant is required"))
	}
	rows, err := store.db.QueryContext(ctx, `SELECT account_id, chat_id, effect_id FROM typed_effects
      WHERE tenant_id = ? AND state = ? AND created_at_ms < ?
		AND (
			model_call_id IS NULL OR EXISTS (
				SELECT 1 FROM outbound_actions
				WHERE outbound_actions.tenant_id = typed_effects.tenant_id
				  AND outbound_actions.account_id = typed_effects.account_id
				  AND outbound_actions.chat_id = typed_effects.chat_id
				  AND outbound_actions.invocation_id = typed_effects.invocation_id
				  AND outbound_actions.state = ?
			) OR EXISTS (
				SELECT 1 FROM inbound_events
				WHERE inbound_events.tenant_id = typed_effects.tenant_id
				  AND inbound_events.account_id = typed_effects.account_id
				  AND inbound_events.chat_id = typed_effects.chat_id
				  AND inbound_events.invocation_id = typed_effects.invocation_id
				  AND inbound_events.turn_state = ?
				  AND inbound_events.turn_claimed = 1
				  AND `+noOutboundAction("inbound_events")+`
			)
		)
      ORDER BY created_at_ms, effect_id`,
		tenantID.String(), uint8(effect.StatePending), cutoffMillis(plannedBefore), uint8(action.StateSucceeded), uint8(agent.TurnSucceeded),
	)
	if err != nil {
		return nil, storageError("list pending effects", err)
	}
	defer rows.Close()
	refs := make([]effect.Ref, 0)
	for rows.Next() {
		var accountValue, chatValue, effectValue string
		if err := rows.Scan(&accountValue, &chatValue, &effectValue); err != nil {
			return nil, storageError("scan pending effect", err)
		}
		ref, err := decodeEffectRef(tenantID, accountValue, chatValue, effectValue)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("iterate pending effects", err)
	}
	return refs, nil
}

func decodeEffectRef(tenantID identity.TenantID, accountValue, chatValue, effectValue string) (effect.Ref, error) {
	accountID, accountErr := identity.ParseAccountID(accountValue)
	chatID, chatErr := identity.ParseChatID(chatValue)
	effectID, effectErr := identity.ParseEffectID(effectValue)
	if err := errors.Join(accountErr, chatErr, effectErr); err != nil {
		return effect.Ref{}, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect reference", err)
	}
	return effect.Ref{Key: agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}, EffectID: effectID}, nil
}

// resolveInterruptedEffects runs at startup, before anything can run an
// effect: one still executing was cut off by the last shutdown or crash.
func resolveInterruptedEffects(ctx context.Context, tx *sql.Tx, tenantID identity.TenantID, nowMS int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE typed_effects SET
        state = ?, last_error_code = ?, completed_at_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND state = ?`,
		uint8(effect.StateUnknownOutcome), string(agent.ErrorUnknownOutcome), nowMS, nowMS,
		tenantID.String(), uint8(effect.StateExecuting),
	)
	if err != nil {
		return storageError("resolve interrupted effects", err)
	}
	return nil
}

// modelEffectResponseDelivered waits for a text reply's receipt, or for an
// effect-only turn to be durably committed. Non-model effects have no dependency.
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
	if completed == 1 {
		return true, nil
	}
	var effectOnly int
	err = query.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbound_events
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
        AND invocation_id = (SELECT invocation_id FROM typed_effects
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?)
		AND turn_state = ? AND turn_claimed = 1
		AND `+noOutboundAction("inbound_events")+``,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(),
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(), uint8(agent.TurnSucceeded),
	).Scan(&effectOnly)
	if err != nil {
		return false, storageError("read effect-only turn dependency", err)
	}
	return effectOnly == 1, nil
}

func (store *EffectStore) Complete(ctx context.Context, ref effect.Ref, receipt string, now time.Time) error {
	return store.finish(ctx, ref, effect.StateSucceeded, "", receipt, now, effect.StateExecuting)
}

// FailTerminal records an effect that will never run: nothing ran yet, or a
// command was refused before it did anything.
func (store *EffectStore) FailTerminal(ctx context.Context, ref effect.Ref, code agent.ErrorCode, now time.Time) error {
	return store.finish(ctx, ref, effect.StateFailedTerminal, code, "", now, effect.StatePending, effect.StateExecuting)
}

func (store *EffectStore) MarkUnknown(ctx context.Context, ref effect.Ref, code agent.ErrorCode, now time.Time) error {
	return store.finish(ctx, ref, effect.StateUnknownOutcome, code, "", now, effect.StateExecuting)
}

// finish moves an effect in one of the from states to the final state to.
func (store *EffectStore) finish(ctx context.Context, ref effect.Ref, to effect.State, code agent.ErrorCode, receipt string, now time.Time, from ...effect.State) error {
	if err := ref.Validate(); err != nil || now.IsZero() || len(from) == 0 || len(from) > 2 {
		return agent.NewError(agent.ErrorInvalidArgument, "finish typed effect", errors.New("reference, time, and source state are required"))
	}
	if to != effect.StateSucceeded && code == "" {
		return agent.NewError(agent.ErrorInvalidArgument, "finish typed effect", errors.New("terminal errors need a code"))
	}
	nowMS := now.UTC().UnixMilli()
	result, err := store.db.ExecContext(ctx, `UPDATE typed_effects SET
        state = ?, provider_receipt = ?, last_error_code = ?, completed_at_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ? AND state IN (?, ?)`,
		uint8(to), nullableString(receipt), nullableErrorCode(code), nowMS, nowMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(),
		uint8(from[0]), uint8(from[len(from)-1]),
	)
	return requireOne(result, err, "finish typed effect")
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
	case effect.RunCommand:
		if !typed.TargetMessageID.IsZero() {
			return typed.TargetMessageID, true
		}
	default:
		return identity.MessageID{}, false
	}
	return identity.MessageID{}, false
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

// effectPayload is the JSON stored in typed_effects.payload: who asked for
// the effect and its kind-specific fields. Go validates it on decode.
type effectPayload struct {
	Principal   policy.PrincipalKind `json:"principal"`
	Participant string               `json:"participant,omitempty"`
	LID         string               `json:"lid,omitempty"`
	Invocation  string               `json:"invocation,omitempty"`
	Emoji       string               `json:"emoji,omitempty"`
	Command     string               `json:"command,omitempty"`
}

// encodeEffect returns the kind, the queryable target column and the JSON
// payload for request.
func encodeEffect(request effect.PlanRequest) (uint8, any, string, error) {
	payload := effectPayload{Principal: request.Principal.Kind}
	switch request.Principal.Kind {
	case policy.PrincipalHuman:
		payload.Participant, payload.LID = request.Principal.ParticipantID.String(), request.Principal.LID.String()
	case policy.PrincipalModel, policy.PrincipalRecovery:
		payload.Invocation = request.Principal.InvocationID.String()
	}
	var target identity.MessageID
	switch typed := request.Effect.(type) {
	case effect.React:
		target, payload.Emoji = typed.TargetMessageID, typed.Emoji
	case effect.DeleteMessage:
		target = typed.TargetMessageID
	case effect.RunCommand:
		target, payload.Command = typed.TargetMessageID, typed.Command
	default:
		return 0, nil, "", agent.NewError(agent.ErrorInvalidArgument, "encode typed effect", errors.New("effect type is not supported"))
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, "", agent.NewError(agent.ErrorInternal, "encode typed effect", err)
	}
	return uint8(request.Effect.Kind()), nullableMessageID(target), string(encoded), nil
}

func decodeEffect(ref effect.Ref, kind effect.Kind, target sql.NullString, raw string) (policy.Principal, effect.Effect, error) {
	var payload effectPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return policy.Principal{}, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", err)
	}
	principal := policy.Principal{Kind: payload.Principal, TenantID: ref.Key.TenantID, AccountID: ref.Key.AccountID, ChatID: ref.Key.ChatID}
	var err error
	switch payload.Principal {
	case policy.PrincipalHuman:
		principal.ParticipantID, err = identity.ParseParticipantID(payload.Participant)
		if err == nil {
			principal.LID, err = identity.ParseLID(payload.LID)
		}
	case policy.PrincipalModel, policy.PrincipalRecovery:
		principal.InvocationID, err = identity.ParseInvocationID(payload.Invocation)
	}
	if err != nil {
		return policy.Principal{}, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect principal", err)
	}
	var targetID identity.MessageID
	if target.Valid {
		if targetID, err = identity.ParseMessageID(target.String); err != nil {
			return policy.Principal{}, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect target", err)
		}
	}
	var value effect.Effect
	switch kind {
	case effect.KindReact:
		value = effect.React{TargetMessageID: targetID, Emoji: payload.Emoji}
	case effect.KindDeleteMessage:
		value = effect.DeleteMessage{TargetMessageID: targetID}
	case effect.KindRunCommand:
		value = effect.RunCommand{Command: payload.Command, TargetMessageID: targetID}
	default:
		return policy.Principal{}, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", errors.New("effect kind is invalid"))
	}
	return principal, value, nil
}

func loadEffect(ctx context.Context, query effectQuerier, ref effect.Ref) (effect.Stored, error) {
	var (
		invocationValue, payload string
		kind, state              int64
		target, receipt          sql.NullString
		completedAt              sql.NullInt64
	)
	err := query.QueryRowContext(ctx, `SELECT invocation_id, kind, target_message_id, payload, state,
        provider_receipt, completed_at_ms
      FROM typed_effects WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_id = ?`,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.EffectID.String(),
	).Scan(&invocationValue, &kind, &target, &payload, &state, &receipt, &completedAt)
	if err != nil {
		return effect.Stored{}, err
	}
	invocationID, err := identity.ParseInvocationID(invocationValue)
	if err != nil {
		return effect.Stored{}, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", err)
	}
	principal, value, err := decodeEffect(ref, effect.Kind(kind), target, payload)
	if err != nil {
		return effect.Stored{}, err
	}
	stored := effect.Stored{Request: effect.PlanRequest{Ref: ref, InvocationID: invocationID, Principal: principal, Effect: value}, State: effect.State(state), ProviderReceipt: receipt.String}
	if err := stored.Request.Validate(); err != nil {
		return effect.Stored{}, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", err)
	}
	switch stored.State {
	case effect.StatePending, effect.StateExecuting, effect.StateSucceeded, effect.StateFailedTerminal, effect.StateUnknownOutcome:
	default:
		return effect.Stored{}, agent.NewError(agent.ErrorIntegrityFailure, "decode typed effect", errors.New("effect state is invalid"))
	}
	if completedAt.Valid {
		value := time.UnixMilli(completedAt.Int64).UTC()
		stored.CompletedAt = &value
	}
	return stored, nil
}
