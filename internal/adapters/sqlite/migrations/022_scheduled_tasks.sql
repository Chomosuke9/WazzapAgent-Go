-- Tasks scheduled with /schedule-task. A row is deleted once its turn has
-- run; rows left at shutdown are armed again on the next start.
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
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, account_id, chat_id, source_message_id)
);
