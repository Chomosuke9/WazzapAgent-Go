ALTER TABLE agent_configs
    ADD COLUMN trigger_mention INTEGER NOT NULL DEFAULT 1 CHECK (trigger_mention IN (0, 1));

ALTER TABLE agent_configs
    ADD COLUMN trigger_name INTEGER NOT NULL DEFAULT 0 CHECK (trigger_name IN (0, 1));

ALTER TABLE agent_configs
    ADD COLUMN trigger_reply INTEGER NOT NULL DEFAULT 1 CHECK (trigger_reply IN (0, 1));

ALTER TABLE agent_configs
    ADD COLUMN trigger_name_regex INTEGER NOT NULL DEFAULT 0 CHECK (trigger_name_regex IN (0, 1));

ALTER TABLE agent_configs
    ADD COLUMN trigger_name_pattern TEXT NOT NULL DEFAULT '';
