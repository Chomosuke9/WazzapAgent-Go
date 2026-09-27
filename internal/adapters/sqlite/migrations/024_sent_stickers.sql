-- sent_stickers binds each sticker the bot sent to an assistant message ID,
-- so a reply to it counts as a reply to the bot.
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
CREATE INDEX sent_stickers_age_idx ON sent_stickers(tenant_id, sent_at_ms);
