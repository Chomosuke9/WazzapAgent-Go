-- The complete application schema. Discord identities are snowflakes: a
-- chat's provider_address is its channel ID and a participant's user_id is
-- the member's user ID.

CREATE TABLE tenants (
    id TEXT PRIMARY KEY,
    created_at_ms INTEGER NOT NULL
) STRICT;

CREATE TABLE accounts (
    tenant_id TEXT NOT NULL,
    id TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE chats (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    id TEXT NOT NULL,
    provider_address TEXT,
    -- guild_address is the channel's Discord server, and alias_address a
    -- thread's parent channel or a direct chat's user: allowlist entries
    -- naming either admit the chat too.
    guild_address TEXT NOT NULL DEFAULT '',
    alias_address TEXT NOT NULL DEFAULT '',
    kind INTEGER NOT NULL DEFAULT 0,
    allowlisted INTEGER NOT NULL DEFAULT 0 CHECK (allowlisted IN (0, 1)),
    created_at_ms INTEGER NOT NULL,
    group_name TEXT NOT NULL DEFAULT '' CHECK (length(group_name) <= 512),
    PRIMARY KEY (tenant_id, account_id, id),
    UNIQUE (tenant_id, account_id, provider_address),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts(tenant_id, id) ON DELETE CASCADE
) STRICT;

CREATE TABLE agent_configs (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    provider_id TEXT NOT NULL,
    model TEXT NOT NULL,
    max_output_tokens INTEGER NOT NULL CHECK (max_output_tokens > 0),
    prompt TEXT NOT NULL,
    prompt_override_mode INTEGER,
    prompt_override_text TEXT,
    policy_id TEXT NOT NULL,
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    updated_at_ms INTEGER NOT NULL,
    model_capabilities TEXT NOT NULL DEFAULT '[]',
    moderation_level INTEGER NOT NULL DEFAULT 0 CHECK (moderation_level BETWEEN 0 AND 3),
    trigger_mention INTEGER NOT NULL DEFAULT 1 CHECK (trigger_mention IN (0, 1)),
    trigger_name INTEGER NOT NULL DEFAULT 0 CHECK (trigger_name IN (0, 1)),
    trigger_reply INTEGER NOT NULL DEFAULT 1 CHECK (trigger_reply IN (0, 1)),
    trigger_name_regex INTEGER NOT NULL DEFAULT 0 CHECK (trigger_name_regex IN (0, 1)),
    trigger_name_pattern TEXT NOT NULL DEFAULT '',
    trigger_smart INTEGER NOT NULL DEFAULT 0 CHECK (trigger_smart IN (0, 1)),
    trigger_smart_rules TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, account_id, chat_id),
    FOREIGN KEY (tenant_id, account_id, chat_id) REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    CHECK ((prompt_override_mode IS NULL) = (prompt_override_text IS NULL))
) STRICT;

CREATE TABLE history_entries (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    causation_kind INTEGER NOT NULL,
    causation_id TEXT NOT NULL,
    role INTEGER NOT NULL CHECK (role IN (1, 2, 3)),
    participant_id TEXT,
    sender_ref TEXT,
    sender_name TEXT NOT NULL DEFAULT '',
    quoted_message_id TEXT,
    quoted_role INTEGER,
    quoted_sender_ref TEXT,
    quoted_text TEXT,
    content_text TEXT NOT NULL,
    delivery_status INTEGER NOT NULL DEFAULT 0,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    quoted_sequence INTEGER, sender_is_admin INTEGER NOT NULL DEFAULT 0 CHECK (sender_is_admin IN (0, 1)),
    sender_is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK (sender_is_super_admin IN (0, 1)),
    quoted_sender_is_admin INTEGER NOT NULL DEFAULT 0 CHECK (quoted_sender_is_admin IN (0, 1)),
    quoted_sender_is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK (quoted_sender_is_super_admin IN (0, 1)),
    UNIQUE (tenant_id, account_id, chat_id, message_id),
    UNIQUE (tenant_id, account_id, chat_id, invocation_id, role),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id, participant_id)
        REFERENCES participants(tenant_id, account_id, id),
    FOREIGN KEY (tenant_id, account_id, chat_id, sender_ref)
        REFERENCES sender_refs(tenant_id, account_id, chat_id, sender_ref),
    CHECK ((role = 1) = (participant_id IS NOT NULL)),
    CHECK ((participant_id IS NULL) = (sender_ref IS NULL)),
    CHECK ((quoted_message_id IS NULL) = (quoted_role IS NULL)),
    CHECK ((quoted_message_id IS NULL) = (quoted_text IS NULL))
) STRICT;

CREATE TABLE history_resets (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    cutoff_sequence INTEGER NOT NULL CHECK (cutoff_sequence >= 0),
    config_version INTEGER NOT NULL CHECK (config_version >= 1),
    reset_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;

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

CREATE TABLE message_mentions (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 128),
    token TEXT NOT NULL CHECK (length(token) BETWEEN 2 AND 33),
    sender_ref TEXT,
    is_bot INTEGER NOT NULL CHECK (is_bot IN (0, 1)),
    PRIMARY KEY (tenant_id, account_id, chat_id, message_id, ordinal),
    UNIQUE (tenant_id, account_id, chat_id, message_id, token),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id, chat_id, sender_ref)
        REFERENCES sender_refs(tenant_id, account_id, chat_id, sender_ref),
    CHECK ((is_bot = 1) = (sender_ref IS NULL))
) STRICT;

CREATE TABLE manual_message_targets (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    provider_receipt TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, message_id),
    UNIQUE (tenant_id, account_id, chat_id, provider_receipt),
    FOREIGN KEY (tenant_id, account_id, chat_id, message_id)
        REFERENCES history_entries(tenant_id, account_id, chat_id, message_id) ON DELETE CASCADE
) STRICT;

CREATE TABLE broadcast_schedules (
    id TEXT PRIMARY KEY NOT NULL,
    scheduled_at_ms INTEGER NOT NULL,
    format TEXT NOT NULL CHECK (format IN ('text', 'payload')),
    payload TEXT NOT NULL,
    batch_size INTEGER NOT NULL CHECK (batch_size BETWEEN 1 AND 100),
    batch_delay_seconds INTEGER NOT NULL CHECK (batch_delay_seconds BETWEEN 0 AND 300),
    targets_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('scheduled', 'sending', 'completed', 'partial', 'failed', 'cancelled')),
    results_json TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
) STRICT;

CREATE TABLE catch_raw_quotes (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    provider_message_id TEXT NOT NULL,
    from_me INTEGER CHECK (from_me IS NULL OR from_me IN (0, 1)),
    message_json BLOB NOT NULL CHECK (length(message_json) BETWEEN 1 AND 1048576),
    PRIMARY KEY (tenant_id, account_id, chat_id, invocation_id),
    FOREIGN KEY (tenant_id, account_id, chat_id, invocation_id)
        REFERENCES inbound_events(tenant_id, account_id, chat_id, invocation_id) ON DELETE CASCADE
) STRICT;

CREATE TABLE participants (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    owner INTEGER NOT NULL DEFAULT 0 CHECK (owner IN (0, 1)),
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, id),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts(tenant_id, id) ON DELETE CASCADE
) STRICT;

CREATE TABLE sender_refs (
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

CREATE TABLE inbound_events (
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
    config_version INTEGER,
    turn_state INTEGER NOT NULL DEFAULT 0,
    generation_attempts INTEGER NOT NULL DEFAULT 0 CHECK (generation_attempts >= 0),
    last_error_code TEXT,
    ignored_reason TEXT,
    updated_at_ms INTEGER NOT NULL,
    turn_claimed INTEGER NOT NULL DEFAULT 0 CHECK (turn_claimed IN (0, 1)),
    PRIMARY KEY (tenant_id, account_id, chat_id, invocation_id),
    UNIQUE (tenant_id, account_id, chat_id, provider_message_id),
    UNIQUE (tenant_id, account_id, chat_id, message_id),
    UNIQUE (tenant_id, account_id, chat_id, causation_id),
    FOREIGN KEY (tenant_id, account_id, chat_id) REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id, participant_id) REFERENCES participants(tenant_id, account_id, id),
    FOREIGN KEY (tenant_id, account_id, chat_id, sender_ref) REFERENCES sender_refs(tenant_id, account_id, chat_id, sender_ref)
) STRICT;

CREATE TABLE outbound_actions (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    action_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    response_id TEXT NOT NULL,
    reply_to_message_id TEXT,
    text TEXT NOT NULL,
    state INTEGER NOT NULL,
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

CREATE TABLE typed_effects (
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
    provider_receipt TEXT,
    last_error_code TEXT,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    completed_at_ms INTEGER,
    PRIMARY KEY (tenant_id, account_id, chat_id, effect_id),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;

CREATE TABLE scheduled_tasks (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    source_message_id TEXT NOT NULL,
    prompt TEXT NOT NULL,
    fire_at_ms INTEGER NOT NULL,
    created_at_ms INTEGER NOT NULL,
    daily_minute INTEGER CHECK (daily_minute BETWEEN 0 AND 1439),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, account_id, chat_id, source_message_id)
);

CREATE TABLE sent_stickers (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    provider_receipt TEXT NOT NULL,
    message_id TEXT NOT NULL,
    name TEXT NOT NULL,
    sent_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, provider_receipt),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;

-- receipt_aliases maps the later parts of a reply Discord got as several
-- messages (it caps a message at 2000 characters) to the first part's
-- receipt, so replying to any part is a reply to the bot's message.
CREATE TABLE receipt_aliases (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    alias_receipt TEXT NOT NULL,
    primary_receipt TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, alias_receipt),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX receipt_aliases_age_idx ON receipt_aliases(tenant_id, created_at_ms);

CREATE INDEX history_entries_window_idx
    ON history_entries(tenant_id, account_id, chat_id, sequence DESC);

CREATE INDEX history_entries_retention_idx
    ON history_entries(tenant_id, account_id, chat_id, created_at_ms, delivery_status);

CREATE INDEX history_entries_message_lookup_idx
    ON history_entries(tenant_id, account_id, chat_id, message_id, sequence);

CREATE INDEX broadcast_schedules_due
    ON broadcast_schedules(status, scheduled_at_ms, created_at_ms);

CREATE INDEX broadcast_schedules_recent
    ON broadcast_schedules(status, updated_at_ms DESC);

CREATE UNIQUE INDEX participants_user_id_idx
    ON participants(tenant_id, account_id, user_id);

CREATE INDEX inbound_events_retention_idx
    ON inbound_events(tenant_id, turn_state, updated_at_ms);

CREATE UNIQUE INDEX outbound_actions_provider_receipt_idx
    ON outbound_actions(tenant_id, account_id, chat_id, provider_receipt)
    WHERE provider_receipt IS NOT NULL;

CREATE INDEX typed_effects_recovery_idx ON typed_effects(tenant_id, state, updated_at_ms);

CREATE INDEX typed_effects_target_idx
    ON typed_effects(tenant_id, account_id, chat_id, target_message_id)
    WHERE target_message_id IS NOT NULL;

CREATE UNIQUE INDEX typed_effects_model_call_idx
    ON typed_effects(tenant_id, account_id, chat_id, invocation_id, model_call_id)
    WHERE model_call_id IS NOT NULL;

CREATE INDEX outbound_actions_state_idx ON outbound_actions(tenant_id, state, created_at_ms);

CREATE INDEX sent_stickers_age_idx ON sent_stickers(tenant_id, sent_at_ms);
