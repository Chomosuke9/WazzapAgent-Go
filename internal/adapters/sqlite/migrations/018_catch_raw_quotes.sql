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
