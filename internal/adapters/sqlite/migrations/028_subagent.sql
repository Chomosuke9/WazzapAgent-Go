-- Sub-agent tasks started with execute_subtask. A task is running until the
-- service calls back or it times out, then waits to be delivered to its chat:
-- delivered_at_ms is set once the report and every output file were sent.
-- invocation_id is the turn that delivers the result.
CREATE TABLE subagent_tasks (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    instruction TEXT NOT NULL,
    high_quality INTEGER NOT NULL CHECK (high_quality IN (0, 1)),
    previous_session_id TEXT,
    file_message_ids TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    causation_id TEXT NOT NULL,
    status INTEGER NOT NULL CHECK (status IN (1, 2, 3)),
    submitted INTEGER NOT NULL DEFAULT 0 CHECK (submitted IN (0, 1)),
    result_json TEXT NOT NULL DEFAULT '{}',
    report TEXT NOT NULL DEFAULT '',
    started_at_ms INTEGER NOT NULL,
    ended_at_ms INTEGER,
    delivered_at_ms INTEGER,
    PRIMARY KEY (tenant_id, session_id),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;
-- A chat runs one task at a time; a second execute_subtask steers it.
CREATE UNIQUE INDEX subagent_tasks_active_idx
    ON subagent_tasks(tenant_id, account_id, chat_id) WHERE status = 1;
CREATE INDEX subagent_tasks_undelivered_idx
    ON subagent_tasks(tenant_id, started_at_ms) WHERE delivered_at_ms IS NULL;
CREATE INDEX subagent_tasks_invocation_idx
    ON subagent_tasks(tenant_id, account_id, chat_id, invocation_id);

-- The parts of a result (the report, each file) already sent, so a delivery
-- retried after a failure or restart sends nothing twice.
CREATE TABLE subagent_delivery_parts (
    tenant_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    part TEXT NOT NULL,
    PRIMARY KEY (tenant_id, session_id, part),
    FOREIGN KEY (tenant_id, session_id)
        REFERENCES subagent_tasks(tenant_id, session_id) ON DELETE CASCADE
) STRICT;

-- Files the bot sent for a task, by the assistant message that carries each,
-- so a later task can take one as input and a reply to one counts as a reply
-- to the bot.
CREATE TABLE subagent_files (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    provider_receipt TEXT NOT NULL,
    path TEXT NOT NULL,
    name TEXT NOT NULL,
    mime TEXT NOT NULL,
    kind TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, message_id),
    FOREIGN KEY (tenant_id, account_id, chat_id, message_id)
        REFERENCES history_entries(tenant_id, account_id, chat_id, message_id) ON DELETE CASCADE
) STRICT;
CREATE INDEX subagent_files_receipt_idx
    ON subagent_files(tenant_id, account_id, chat_id, provider_receipt);

-- /subagent on|off for one chat, and /subagent default for the others.
CREATE TABLE subagent_chat_settings (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    PRIMARY KEY (tenant_id, account_id, chat_id),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;
CREATE TABLE subagent_account_settings (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    default_enabled INTEGER NOT NULL CHECK (default_enabled IN (0, 1)),
    PRIMARY KEY (tenant_id, account_id)
) STRICT;
