ALTER TABLE inbound_events ADD COLUMN invocation_cause INTEGER NOT NULL DEFAULT 1;
ALTER TABLE inbound_events ADD COLUMN causation_kind INTEGER NOT NULL DEFAULT 1;
ALTER TABLE inbound_events ADD COLUMN quoted_message_id TEXT;
ALTER TABLE inbound_events ADD COLUMN quoted_role INTEGER;
ALTER TABLE inbound_events ADD COLUMN quoted_sender_ref TEXT;
ALTER TABLE inbound_events ADD COLUMN quoted_text TEXT;
ALTER TABLE inbound_events ADD COLUMN replied_to_bot INTEGER NOT NULL DEFAULT 0 CHECK (replied_to_bot IN (0, 1));
ALTER TABLE inbound_events ADD COLUMN batch_ready_at_ms INTEGER;
ALTER TABLE inbound_events ADD COLUMN batch_anchor_invocation_id TEXT;
ALTER TABLE inbound_events ADD COLUMN batch_position INTEGER;

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
    content_digest BLOB NOT NULL,
    delivery_status INTEGER NOT NULL DEFAULT 0,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
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

CREATE INDEX history_entries_window_idx
    ON history_entries(tenant_id, account_id, chat_id, sequence DESC);
CREATE INDEX history_entries_retention_idx
    ON history_entries(tenant_id, account_id, chat_id, created_at_ms, delivery_status);
CREATE UNIQUE INDEX outbound_actions_provider_receipt_idx
    ON outbound_actions(tenant_id, account_id, chat_id, provider_receipt)
    WHERE provider_receipt IS NOT NULL;
CREATE UNIQUE INDEX inbound_events_batch_idx
    ON inbound_events(tenant_id, account_id, chat_id, batch_anchor_invocation_id, batch_position)
    WHERE batch_anchor_invocation_id IS NOT NULL AND batch_position IS NOT NULL;
CREATE INDEX inbound_events_batch_ready_idx
    ON inbound_events(tenant_id, account_id, chat_id, batch_ready_at_ms)
    WHERE turn_state = 0 AND batch_anchor_invocation_id IS NULL;
