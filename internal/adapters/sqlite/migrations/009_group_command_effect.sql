ALTER TABLE typed_effects RENAME TO typed_effects_v1;

CREATE TABLE typed_effects (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    effect_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    model_call_id TEXT,
    principal_kind INTEGER NOT NULL CHECK (principal_kind IN (1, 2, 3, 4)),
    principal_participant_id TEXT,
    principal_lid TEXT,
    principal_invocation_id TEXT,
    effect_kind INTEGER NOT NULL CHECK (effect_kind BETWEEN 1 AND 5),
    target_message_id TEXT,
    emoji TEXT,
    presence_state TEXT,
    command_text TEXT,
    payload_digest BLOB NOT NULL,
    state INTEGER NOT NULL CHECK (state BETWEEN 1 AND 7),
    effect_lease TEXT,
    effect_lease_until_ms INTEGER,
    provider_receipt TEXT,
    last_error_code TEXT,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    completed_at_ms INTEGER,
    PRIMARY KEY (tenant_id, account_id, chat_id, effect_id),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    CHECK (
        (principal_kind = 1 AND principal_participant_id IS NOT NULL AND principal_lid IS NOT NULL AND principal_invocation_id IS NULL) OR
        (principal_kind IN (2, 4) AND principal_participant_id IS NULL AND principal_lid IS NULL AND principal_invocation_id IS NOT NULL) OR
        (principal_kind = 3 AND principal_participant_id IS NULL AND principal_lid IS NULL AND principal_invocation_id IS NULL)
    ),
    CHECK (
        (effect_kind = 1 AND target_message_id IS NOT NULL AND emoji IS NOT NULL AND presence_state IS NULL AND command_text IS NULL) OR
        (effect_kind IN (2, 3) AND target_message_id IS NOT NULL AND emoji IS NULL AND presence_state IS NULL AND command_text IS NULL) OR
        (effect_kind = 4 AND target_message_id IS NULL AND emoji IS NULL AND presence_state IN ('composing', 'paused') AND command_text IS NULL) OR
        (effect_kind = 5 AND emoji IS NULL AND presence_state IS NULL AND command_text IS NOT NULL)
    )
) STRICT;

INSERT INTO typed_effects(
    tenant_id, account_id, chat_id, effect_id, invocation_id, model_call_id,
    principal_kind, principal_participant_id, principal_lid, principal_invocation_id,
    effect_kind, target_message_id, emoji, presence_state, payload_digest, state,
    effect_lease, effect_lease_until_ms, provider_receipt, last_error_code,
    created_at_ms, updated_at_ms, completed_at_ms
)
SELECT tenant_id, account_id, chat_id, effect_id, invocation_id, model_call_id,
    principal_kind, principal_participant_id, principal_lid, principal_invocation_id,
    effect_kind, target_message_id, emoji, presence_state, payload_digest, state,
    effect_lease, effect_lease_until_ms, provider_receipt, last_error_code,
    created_at_ms, updated_at_ms, completed_at_ms
FROM typed_effects_v1;

DROP TABLE typed_effects_v1;

CREATE INDEX typed_effects_recovery_idx ON typed_effects(tenant_id, state, updated_at_ms);
CREATE INDEX typed_effects_target_idx ON typed_effects(tenant_id, account_id, chat_id, target_message_id) WHERE target_message_id IS NOT NULL;
CREATE UNIQUE INDEX typed_effects_model_call_idx
    ON typed_effects(tenant_id, account_id, chat_id, invocation_id, model_call_id)
    WHERE model_call_id IS NOT NULL;

CREATE TABLE chat_mutes (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    sender_ref TEXT NOT NULL,
    muted_until_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, sender_ref),
    FOREIGN KEY (tenant_id, account_id, chat_id, sender_ref)
        REFERENCES sender_refs(tenant_id, account_id, chat_id, sender_ref) ON DELETE CASCADE
) STRICT;
