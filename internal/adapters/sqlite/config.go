package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func (store *ConfigStore) LoadOrCreate(ctx context.Context, key agent.Key, defaults agent.ConfigValues) (agent.ConfigSnapshot, error) {
	if err := key.Validate(); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	if err := agent.ValidateConfigValues(defaults); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.ConfigSnapshot{}, storageError("begin config load", err)
	}
	defer tx.Rollback()
	if err := ensureScope(ctx, tx, key, store.clock.Now().UnixMilli()); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	snapshot, err := loadConfig(ctx, tx, key)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return agent.ConfigSnapshot{}, storageError("commit config load", err)
		}
		return snapshot, nil
	}
	if !agent.IsCode(err, agent.ErrorNotFound) {
		return agent.ConfigSnapshot{}, err
	}
	mode, text := nullableOverride(defaults.PromptOverride)
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_configs(
        tenant_id, account_id, chat_id, version, provider_id, model,
        max_output_tokens, prompt, prompt_override_mode, prompt_override_text,
		policy_id, policy_revision, model_capabilities, moderation_level, updated_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), uint64(agent.InitialConfigVersion),
		defaults.Model.ProviderID.String(), defaults.Model.Model, defaults.Model.MaxOutputTokens, defaults.Prompt,
		mode, text, defaults.Permission.PolicyID.String(), defaults.Permission.Revision, "[]", uint8(defaults.Permission.ModerationLevel), store.clock.Now().UnixMilli(),
	)
	if err != nil {
		return agent.ConfigSnapshot{}, storageError("create agent config", err)
	}
	snapshot, err = loadConfig(ctx, tx, key)
	if err != nil {
		return agent.ConfigSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return agent.ConfigSnapshot{}, storageError("commit config create", err)
	}
	return snapshot, nil
}

func (store *ConfigStore) Load(ctx context.Context, key agent.Key) (agent.ConfigSnapshot, error) {
	if err := key.Validate(); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	return loadConfig(ctx, store.db, key)
}

func (store *ConfigStore) CompareAndSwap(
	ctx context.Context,
	key agent.Key,
	expected agent.ConfigVersion,
	values agent.ConfigValues,
) (agent.ConfigSnapshot, error) {
	if err := key.Validate(); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	if err := agent.ValidateConfigValues(values); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	if expected == 0 {
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorInvalidArgument, "compare and swap config", fmt.Errorf("expected version is required"))
	}
	if uint64(expected) >= math.MaxInt64 {
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorIntegrityFailure, "compare and swap config", fmt.Errorf("config version exhausted"))
	}
	mode, text := nullableOverride(values.PromptOverride)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.ConfigSnapshot{}, storageError("begin config update", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_configs SET
        version = version + 1,
        provider_id = ?, model = ?, max_output_tokens = ?, prompt = ?,
        prompt_override_mode = ?, prompt_override_text = ?,
		policy_id = ?, policy_revision = ?, model_capabilities = '[]', moderation_level = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND version = ?`,
		values.Model.ProviderID.String(), values.Model.Model, values.Model.MaxOutputTokens, values.Prompt,
		mode, text, values.Permission.PolicyID.String(), values.Permission.Revision, uint8(values.Permission.ModerationLevel), store.clock.Now().UnixMilli(),
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), uint64(expected),
	)
	if err != nil {
		return agent.ConfigSnapshot{}, storageError("update agent config", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return agent.ConfigSnapshot{}, storageError("inspect config update", err)
	}
	if rows != 1 {
		var count int
		if queryErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_configs
            WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
			key.TenantID.String(), key.AccountID.String(), key.ChatID.String()).Scan(&count); queryErr != nil {
			return agent.ConfigSnapshot{}, storageError("inspect config conflict", queryErr)
		}
		if count == 0 {
			return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorNotFound, "compare and swap config", fmt.Errorf("config does not exist"))
		}
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorConflict, "compare and swap config", fmt.Errorf("stale config version"))
	}
	snapshot, err := loadConfig(ctx, tx, key)
	if err != nil {
		return agent.ConfigSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return agent.ConfigSnapshot{}, storageError("commit config update", err)
	}
	return snapshot, nil
}

type configQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadConfig(ctx context.Context, query configQuerier, key agent.Key) (agent.ConfigSnapshot, error) {
	var (
		version         uint64
		providerValue   string
		model           string
		maxOutputTokens uint32
		prompt          string
		overrideMode    sql.NullInt64
		overrideText    sql.NullString
		policyValue     string
		policyRevision  uint64
		moderationLevel uint8
	)
	err := query.QueryRowContext(ctx, `SELECT version, provider_id, model, max_output_tokens,
        prompt, prompt_override_mode, prompt_override_text, policy_id, policy_revision, moderation_level
      FROM agent_configs WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&version, &providerValue, &model, &maxOutputTokens, &prompt, &overrideMode, &overrideText, &policyValue, &policyRevision, &moderationLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorNotFound, "load agent config", fmt.Errorf("config does not exist"))
	}
	if err != nil {
		return agent.ConfigSnapshot{}, storageError("load agent config", err)
	}
	providerID, err := identity.ParseProviderID(providerValue)
	if err != nil {
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorIntegrityFailure, "decode agent config", err)
	}
	policyID, err := identity.ParsePolicyID(policyValue)
	if err != nil {
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorIntegrityFailure, "decode agent config", err)
	}
	var override *agent.PromptOverride
	if overrideMode.Valid != overrideText.Valid {
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorIntegrityFailure, "decode agent config", fmt.Errorf("partial prompt override"))
	}
	if overrideMode.Valid {
		override = &agent.PromptOverride{Mode: agent.PromptOverrideMode(overrideMode.Int64), Text: overrideText.String}
	}
	return agent.ConfigSnapshot{
		Version: agent.ConfigVersion(version),
		Model: agent.ModelConfig{
			ProviderID:      providerID,
			Model:           model,
			MaxOutputTokens: maxOutputTokens,
		},
		Prompt:         prompt,
		PromptOverride: override,
		Permission: agent.PermissionConfig{
			PolicyID:        policyID,
			Revision:        policyRevision,
			ModerationLevel: agent.ModerationLevel(moderationLevel),
		},
	}, nil
}

func nullableOverride(value *agent.PromptOverride) (any, any) {
	if value == nil {
		return nil, nil
	}
	return uint8(value.Mode), value.Text
}

func ensureScope(ctx context.Context, tx *sql.Tx, key agent.Key, nowMS int64) error {
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT OR IGNORE INTO tenants(id, created_at_ms) VALUES (?, ?)", []any{key.TenantID.String(), nowMS}},
		{"INSERT OR IGNORE INTO accounts(tenant_id, id, created_at_ms) VALUES (?, ?, ?)", []any{key.TenantID.String(), key.AccountID.String(), nowMS}},
		{"INSERT OR IGNORE INTO chats(tenant_id, account_id, id, created_at_ms) VALUES (?, ?, ?, ?)", []any{key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), nowMS}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return storageError("ensure tenant scope", err)
		}
	}
	return nil
}

func storageError(operation string, err error) error {
	if agent.CodeOf(err) != agent.ErrorInternal {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return agent.NewError(agent.ErrorTimeout, operation, err)
	}
	if errors.Is(err, context.Canceled) {
		return agent.NewError(agent.ErrorCancelled, operation, err)
	}
	return agent.NewError(agent.ErrorStorageFailure, operation, err)
}
