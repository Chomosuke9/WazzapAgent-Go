ALTER TABLE inbound_events ADD COLUMN quoted_sequence INTEGER;
ALTER TABLE history_entries ADD COLUMN quoted_sequence INTEGER;

CREATE INDEX history_entries_message_lookup_idx
    ON history_entries(tenant_id, account_id, chat_id, message_id, sequence);
