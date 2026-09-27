-- command_media keeps the provider payload of the image, video or sticker a
-- slash command carried or replied to, so the command can download it.
CREATE TABLE command_media (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    message_json BLOB NOT NULL CHECK (length(message_json) BETWEEN 1 AND 1048576),
    PRIMARY KEY (tenant_id, account_id, chat_id, invocation_id),
    FOREIGN KEY (tenant_id, account_id, chat_id, invocation_id)
        REFERENCES inbound_events(tenant_id, account_id, chat_id, invocation_id) ON DELETE CASCADE
) STRICT;

-- stickers is each chat's named sticker catalog. A sticker is either WebP
-- bytes or, for WhatsApp's Lottie stickers, the provider payload resent as is.
CREATE TABLE stickers (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    name TEXT NOT NULL,
    webp BLOB,
    animated INTEGER NOT NULL CHECK (animated IN (0, 1)),
    lottie_json BLOB,
    created_at_ms INTEGER NOT NULL,
    CHECK ((webp IS NULL) <> (lottie_json IS NULL)),
    PRIMARY KEY (tenant_id, account_id, chat_id, name),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;
