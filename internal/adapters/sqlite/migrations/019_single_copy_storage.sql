-- Keep one copy of each fact.
--
-- inbound_events loses the reply copy (response_id/action_id/response_text,
-- read from outbound_actions by invocation_id), its delivery_status mirror
-- (derived from outbound_actions.state), the scrub flag, the command journal
-- and sender_lid (read from participants). action_receipts mirrored
-- outbound_actions one to one and is dropped. typed_effects stores the
-- principal and effect fields as one JSON payload validated in Go, keeping
-- target_message_id as a column because queries filter on it. participants
-- and sender_refs lose provider_address and lid copies of participants.lid.
--
-- The migration runner disables foreign keys around each migration and runs
-- foreign_key_check before commit, so these table rebuilds do not cascade.

DROP TABLE action_receipts;

CREATE TABLE participants_new (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    id TEXT NOT NULL,
    lid TEXT,
    phone_address TEXT,
    owner INTEGER NOT NULL DEFAULT 0 CHECK (owner IN (0, 1)),
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, id),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts(tenant_id, id) ON DELETE CASCADE
) STRICT;
INSERT INTO participants_new(tenant_id, account_id, id, lid, phone_address, owner, created_at_ms)
SELECT tenant_id, account_id, id, lid, phone_address, owner, created_at_ms FROM participants;
DROP TABLE participants;
ALTER TABLE participants_new RENAME TO participants;
CREATE UNIQUE INDEX participants_lid_idx
    ON participants(tenant_id, account_id, lid) WHERE lid IS NOT NULL;
CREATE UNIQUE INDEX participants_phone_idx
    ON participants(tenant_id, account_id, phone_address) WHERE phone_address IS NOT NULL;

CREATE TABLE sender_refs_new (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    participant_id TEXT NOT NULL,
    sender_ref TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, participant_id),
    UNIQUE (tenant_id, account_id, chat_id, sender_ref),
    FOREIGN KEY (tenant_id, account_id, chat_id) REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id, participant_id) REFERENCES participants(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;
INSERT INTO sender_refs_new(tenant_id, account_id, chat_id, participant_id, sender_ref, display_name, created_at_ms)
SELECT tenant_id, account_id, chat_id, participant_id, sender_ref, display_name, created_at_ms FROM sender_refs;
DROP TABLE sender_refs;
ALTER TABLE sender_refs_new RENAME TO sender_refs;

CREATE TABLE inbound_events_new (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    invocation_cause INTEGER NOT NULL DEFAULT 1,
    causation_kind INTEGER NOT NULL DEFAULT 1,
    causation_id TEXT NOT NULL,
    provider_message_id TEXT,
    participant_id TEXT,
    sender_ref TEXT,
    sender_name TEXT NOT NULL DEFAULT '',
    sender_is_admin INTEGER NOT NULL DEFAULT 0 CHECK (sender_is_admin IN (0, 1)),
    sender_is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK (sender_is_super_admin IN (0, 1)),
    input_text TEXT NOT NULL,
    chat_kind INTEGER NOT NULL DEFAULT 0,
    mentions_bot INTEGER NOT NULL DEFAULT 0 CHECK (mentions_bot IN (0, 1)),
    replied_to_bot INTEGER NOT NULL DEFAULT 0 CHECK (replied_to_bot IN (0, 1)),
    from_me INTEGER NOT NULL DEFAULT 0 CHECK (from_me IN (0, 1)),
    owner INTEGER NOT NULL DEFAULT 0 CHECK (owner IN (0, 1)),
    allowlisted INTEGER NOT NULL DEFAULT 0 CHECK (allowlisted IN (0, 1)),
    quoted_message_id TEXT,
    quoted_sequence INTEGER,
    quoted_role INTEGER,
    quoted_sender_ref TEXT,
    quoted_text TEXT,
    quoted_sender_is_admin INTEGER NOT NULL DEFAULT 0 CHECK (quoted_sender_is_admin IN (0, 1)),
    quoted_sender_is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK (quoted_sender_is_super_admin IN (0, 1)),
    occurred_at_ms INTEGER NOT NULL,
    received_at_ms INTEGER NOT NULL,
    batch_ready_at_ms INTEGER,
    batch_anchor_invocation_id TEXT,
    batch_position INTEGER,
    invocation_digest BLOB,
    config_version INTEGER,
    turn_state INTEGER NOT NULL DEFAULT 0,
    generation_lease TEXT,
    generation_lease_until_ms INTEGER,
    retry_after_ms INTEGER,
    generation_attempts INTEGER NOT NULL DEFAULT 0 CHECK (generation_attempts >= 0),
    last_error_code TEXT,
    ignored_reason TEXT,
    updated_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, invocation_id),
    UNIQUE (tenant_id, account_id, chat_id, provider_message_id),
    UNIQUE (tenant_id, account_id, chat_id, message_id),
    UNIQUE (tenant_id, account_id, chat_id, causation_id),
    FOREIGN KEY (tenant_id, account_id, chat_id) REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id, participant_id) REFERENCES participants(tenant_id, account_id, id),
    FOREIGN KEY (tenant_id, account_id, chat_id, sender_ref) REFERENCES sender_refs(tenant_id, account_id, chat_id, sender_ref)
) STRICT;
INSERT INTO inbound_events_new(
    tenant_id, account_id, chat_id, invocation_id, message_id, invocation_cause, causation_kind, causation_id,
    provider_message_id, participant_id, sender_ref, sender_name, sender_is_admin, sender_is_super_admin,
    input_text, chat_kind, mentions_bot, replied_to_bot, from_me, owner, allowlisted,
    quoted_message_id, quoted_sequence, quoted_role, quoted_sender_ref, quoted_text,
    quoted_sender_is_admin, quoted_sender_is_super_admin, occurred_at_ms, received_at_ms,
    batch_ready_at_ms, batch_anchor_invocation_id, batch_position, invocation_digest, config_version,
    turn_state, generation_lease, generation_lease_until_ms, retry_after_ms, generation_attempts,
    last_error_code, ignored_reason, updated_at_ms
)
SELECT
    tenant_id, account_id, chat_id, invocation_id, message_id, invocation_cause, causation_kind, causation_id,
    provider_message_id, participant_id, sender_ref, sender_name, sender_is_admin, sender_is_super_admin,
    input_text, chat_kind, mentions_bot, replied_to_bot, from_me, owner, allowlisted,
    quoted_message_id, quoted_sequence, quoted_role, quoted_sender_ref, quoted_text,
    quoted_sender_is_admin, quoted_sender_is_super_admin, occurred_at_ms, received_at_ms,
    batch_ready_at_ms, batch_anchor_invocation_id, batch_position, invocation_digest, config_version,
    turn_state, generation_lease, generation_lease_until_ms, retry_after_ms, generation_attempts,
    last_error_code, ignored_reason, updated_at_ms
FROM inbound_events;
DROP TABLE inbound_events;
ALTER TABLE inbound_events_new RENAME TO inbound_events;
CREATE UNIQUE INDEX inbound_events_batch_idx
    ON inbound_events(tenant_id, account_id, chat_id, batch_anchor_invocation_id, batch_position)
    WHERE batch_anchor_invocation_id IS NOT NULL AND batch_position IS NOT NULL;
CREATE INDEX inbound_events_batch_ready_idx
    ON inbound_events(tenant_id, account_id, chat_id, batch_ready_at_ms)
    WHERE turn_state = 0 AND batch_anchor_invocation_id IS NULL;
CREATE INDEX inbound_events_retention_idx
    ON inbound_events(tenant_id, turn_state, updated_at_ms);

CREATE TABLE outbound_actions_new (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    action_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    response_id TEXT NOT NULL,
    reply_to_message_id TEXT,
    text TEXT NOT NULL,
    state INTEGER NOT NULL,
    action_lease TEXT,
    action_lease_until_ms INTEGER,
    retry_after_ms INTEGER,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    provider_receipt TEXT,
    last_error_code TEXT,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    completed_at_ms INTEGER,
    PRIMARY KEY (tenant_id, account_id, chat_id, action_id),
    UNIQUE (tenant_id, account_id, chat_id, invocation_id),
    FOREIGN KEY (tenant_id, account_id, chat_id, invocation_id)
        REFERENCES inbound_events(tenant_id, account_id, chat_id, invocation_id) ON DELETE CASCADE
) STRICT;
INSERT INTO outbound_actions_new(
    tenant_id, account_id, chat_id, action_id, invocation_id, response_id, reply_to_message_id, text,
    state, action_lease, action_lease_until_ms, retry_after_ms, attempts, provider_receipt,
    last_error_code, created_at_ms, updated_at_ms, completed_at_ms
)
SELECT tenant_id, account_id, chat_id, action_id, invocation_id, response_id, reply_to_message_id, text,
    state, action_lease, action_lease_until_ms, retry_after_ms, attempts, provider_receipt,
    last_error_code, created_at_ms, updated_at_ms, completed_at_ms
FROM outbound_actions;
DROP TABLE outbound_actions;
ALTER TABLE outbound_actions_new RENAME TO outbound_actions;
CREATE INDEX outbound_actions_state_idx
    ON outbound_actions(tenant_id, state, retry_after_ms, updated_at_ms);
CREATE UNIQUE INDEX outbound_actions_provider_receipt_idx
    ON outbound_actions(tenant_id, account_id, chat_id, provider_receipt)
    WHERE provider_receipt IS NOT NULL;

CREATE TABLE typed_effects_new (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    effect_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    model_call_id TEXT,
    kind INTEGER NOT NULL,
    target_message_id TEXT,
    payload TEXT NOT NULL,
    state INTEGER NOT NULL,
    effect_lease TEXT,
    effect_lease_until_ms INTEGER,
    provider_receipt TEXT,
    last_error_code TEXT,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    completed_at_ms INTEGER,
    PRIMARY KEY (tenant_id, account_id, chat_id, effect_id),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;
INSERT INTO typed_effects_new(
    tenant_id, account_id, chat_id, effect_id, invocation_id, model_call_id, kind, target_message_id,
    payload, state, effect_lease, effect_lease_until_ms, provider_receipt, last_error_code,
    created_at_ms, updated_at_ms, completed_at_ms
)
SELECT tenant_id, account_id, chat_id, effect_id, invocation_id, model_call_id, effect_kind, target_message_id,
    json_object(
        'principal', principal_kind,
        'participant', principal_participant_id,
        'lid', principal_lid,
        'invocation', principal_invocation_id,
        'emoji', emoji,
        'presence', presence_state,
        'command', command_text
    ),
    state, effect_lease, effect_lease_until_ms, provider_receipt, last_error_code,
    created_at_ms, updated_at_ms, completed_at_ms
FROM typed_effects;
DROP TABLE typed_effects;
ALTER TABLE typed_effects_new RENAME TO typed_effects;
CREATE INDEX typed_effects_recovery_idx ON typed_effects(tenant_id, state, updated_at_ms);
CREATE INDEX typed_effects_target_idx
    ON typed_effects(tenant_id, account_id, chat_id, target_message_id)
    WHERE target_message_id IS NOT NULL;
CREATE UNIQUE INDEX typed_effects_model_call_idx
    ON typed_effects(tenant_id, account_id, chat_id, invocation_id, model_call_id)
    WHERE model_call_id IS NOT NULL;
