ALTER TABLE inbound_events ADD COLUMN sender_is_admin INTEGER NOT NULL DEFAULT 0 CHECK (sender_is_admin IN (0, 1));
ALTER TABLE inbound_events ADD COLUMN sender_is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK (sender_is_super_admin IN (0, 1));
ALTER TABLE inbound_events ADD COLUMN quoted_sender_is_admin INTEGER NOT NULL DEFAULT 0 CHECK (quoted_sender_is_admin IN (0, 1));
ALTER TABLE inbound_events ADD COLUMN quoted_sender_is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK (quoted_sender_is_super_admin IN (0, 1));

ALTER TABLE history_entries ADD COLUMN sender_is_admin INTEGER NOT NULL DEFAULT 0 CHECK (sender_is_admin IN (0, 1));
ALTER TABLE history_entries ADD COLUMN sender_is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK (sender_is_super_admin IN (0, 1));
ALTER TABLE history_entries ADD COLUMN quoted_sender_is_admin INTEGER NOT NULL DEFAULT 0 CHECK (quoted_sender_is_admin IN (0, 1));
ALTER TABLE history_entries ADD COLUMN quoted_sender_is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK (quoted_sender_is_super_admin IN (0, 1));
