ALTER TABLE chats ADD COLUMN group_name TEXT NOT NULL DEFAULT '' CHECK (length(group_name) <= 512);
