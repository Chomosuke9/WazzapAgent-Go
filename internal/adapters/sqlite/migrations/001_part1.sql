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
    kind INTEGER NOT NULL DEFAULT 0,
    allowlisted INTEGER NOT NULL DEFAULT 0 CHECK (allowlisted IN (0, 1)),
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, id),
    UNIQUE (tenant_id, account_id, provider_address),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts(tenant_id, id) ON DELETE CASCADE
) STRICT;

CREATE TABLE participants (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    id TEXT NOT NULL,
    provider_address TEXT NOT NULL,
    owner INTEGER NOT NULL DEFAULT 0 CHECK (owner IN (0, 1)),
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, id),
    UNIQUE (tenant_id, account_id, provider_address),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts(tenant_id, id) ON DELETE CASCADE
) STRICT;

CREATE TABLE sender_refs (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    participant_id TEXT NOT NULL,
    sender_ref TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, participant_id),
    UNIQUE (tenant_id, account_id, chat_id, sender_ref),
    FOREIGN KEY (tenant_id, account_id, chat_id) REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id, participant_id) REFERENCES participants(tenant_id, account_id, id) ON DELETE CASCADE
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
    PRIMARY KEY (tenant_id, account_id, chat_id),
    FOREIGN KEY (tenant_id, account_id, chat_id) REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    CHECK ((prompt_override_mode IS NULL) = (prompt_override_text IS NULL))
) STRICT;

CREATE TABLE inbound_events (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    causation_id TEXT NOT NULL,
    provider_message_id TEXT,
    participant_id TEXT,
    sender_ref TEXT,
    sender_name TEXT NOT NULL DEFAULT '',
    input_text TEXT NOT NULL,
    chat_kind INTEGER NOT NULL DEFAULT 0,
    mentions_bot INTEGER NOT NULL DEFAULT 0 CHECK (mentions_bot IN (0, 1)),
    from_me INTEGER NOT NULL DEFAULT 0 CHECK (from_me IN (0, 1)),
    owner INTEGER NOT NULL DEFAULT 0 CHECK (owner IN (0, 1)),
    allowlisted INTEGER NOT NULL DEFAULT 0 CHECK (allowlisted IN (0, 1)),
    occurred_at_ms INTEGER NOT NULL,
    received_at_ms INTEGER NOT NULL,
    invocation_digest BLOB,
    config_version INTEGER,
    turn_state INTEGER NOT NULL DEFAULT 0,
    generation_lease TEXT,
    generation_lease_until_ms INTEGER,
    retry_after_ms INTEGER,
    generation_attempts INTEGER NOT NULL DEFAULT 0 CHECK (generation_attempts >= 0),
    response_id TEXT,
    action_id TEXT,
    response_text TEXT,
    delivery_status INTEGER NOT NULL DEFAULT 0,
    last_error_code TEXT,
	ignored_reason TEXT,
	content_scrubbed INTEGER NOT NULL DEFAULT 0 CHECK (content_scrubbed IN (0, 1)),
	command_kind INTEGER,
	command_digest BLOB,
	command_expected_version INTEGER,
	command_applied_version INTEGER,
    updated_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, invocation_id),
    UNIQUE (tenant_id, account_id, chat_id, provider_message_id),
    UNIQUE (tenant_id, account_id, chat_id, message_id),
    UNIQUE (tenant_id, account_id, chat_id, causation_id),
    FOREIGN KEY (tenant_id, account_id, chat_id) REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id, participant_id) REFERENCES participants(tenant_id, account_id, id),
    FOREIGN KEY (tenant_id, account_id, chat_id, sender_ref) REFERENCES sender_refs(tenant_id, account_id, chat_id, sender_ref),
    CHECK ((response_id IS NULL) = (action_id IS NULL)),
	CHECK ((response_id IS NULL) = (response_text IS NULL)),
	CHECK ((command_kind IS NULL) = (command_digest IS NULL)),
	CHECK ((command_kind IS NULL) = (command_expected_version IS NULL))
) STRICT;

CREATE TABLE outbound_actions (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    action_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    response_id TEXT NOT NULL,
    payload_digest BLOB NOT NULL,
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
	content_scrubbed INTEGER NOT NULL DEFAULT 0 CHECK (content_scrubbed IN (0, 1)),
    PRIMARY KEY (tenant_id, account_id, chat_id, action_id),
    UNIQUE (tenant_id, account_id, chat_id, invocation_id),
    FOREIGN KEY (tenant_id, account_id, chat_id, invocation_id)
        REFERENCES inbound_events(tenant_id, account_id, chat_id, invocation_id) ON DELETE CASCADE
) STRICT;

CREATE TABLE action_receipts (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    action_id TEXT NOT NULL,
    status INTEGER NOT NULL,
    provider_receipt TEXT,
    error_code TEXT,
    completed_at_ms INTEGER,
    updated_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, action_id),
    FOREIGN KEY (tenant_id, account_id, chat_id, action_id)
        REFERENCES outbound_actions(tenant_id, account_id, chat_id, action_id) ON DELETE CASCADE
) STRICT;

CREATE INDEX inbound_events_provider_idx
    ON inbound_events(tenant_id, account_id, chat_id, provider_message_id);
CREATE INDEX outbound_actions_state_idx
    ON outbound_actions(tenant_id, state, retry_after_ms, updated_at_ms);
