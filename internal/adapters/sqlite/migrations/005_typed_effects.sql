CREATE TABLE typed_effects (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    effect_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    principal_kind INTEGER NOT NULL CHECK (principal_kind IN (1, 2, 3, 4)),
    principal_participant_id TEXT,
    principal_lid TEXT,
    principal_invocation_id TEXT,
    effect_kind INTEGER NOT NULL CHECK (effect_kind IN (1, 2, 3, 4)),
    target_message_id TEXT,
    emoji TEXT,
    presence_state TEXT,
    payload_digest BLOB NOT NULL,
    state INTEGER NOT NULL CHECK (state IN (1, 2, 3, 4, 5, 6, 7)),
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
        (effect_kind = 1 AND target_message_id IS NOT NULL AND emoji IS NOT NULL AND presence_state IS NULL) OR
        (effect_kind IN (2, 3) AND target_message_id IS NOT NULL AND emoji IS NULL AND presence_state IS NULL) OR
        (effect_kind = 4 AND target_message_id IS NULL AND emoji IS NULL AND presence_state IN ('composing', 'paused'))
    )
) STRICT;

CREATE INDEX typed_effects_recovery_idx
    ON typed_effects(tenant_id, state, updated_at_ms);
CREATE INDEX typed_effects_target_idx
    ON typed_effects(tenant_id, account_id, chat_id, target_message_id)
    WHERE target_message_id IS NOT NULL;
