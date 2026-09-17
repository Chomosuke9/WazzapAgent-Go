ALTER TABLE sender_refs ADD COLUMN display_name TEXT NOT NULL DEFAULT '';

-- Mention metadata is keyed by provider-neutral message ID rather than an
-- inbound row. It may outlive inbound retention while retained history still
-- references the original message; maintenance removes unreachable rows.
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
