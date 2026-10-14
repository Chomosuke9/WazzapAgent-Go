-- memories is each chat's long-term memory from /memory: short notes the bot
-- sees on every turn. code is the short ID people and the bot delete one by;
-- rows keep insertion order through seq.
CREATE TABLE memories (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    code TEXT NOT NULL,
    seq INTEGER NOT NULL,
    text TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 4000),
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, chat_id, code),
    UNIQUE (tenant_id, account_id, chat_id, text),
    FOREIGN KEY (tenant_id, account_id, chat_id)
        REFERENCES chats(tenant_id, account_id, id) ON DELETE CASCADE
) STRICT;
CREATE INDEX memories_order_idx ON memories(tenant_id, account_id, chat_id, seq);
