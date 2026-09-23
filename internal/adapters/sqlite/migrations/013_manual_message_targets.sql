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
