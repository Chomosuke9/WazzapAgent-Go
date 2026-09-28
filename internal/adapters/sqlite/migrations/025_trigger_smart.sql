ALTER TABLE agent_configs
    ADD COLUMN trigger_smart INTEGER NOT NULL DEFAULT 0 CHECK (trigger_smart IN (0, 1));

ALTER TABLE agent_configs
    ADD COLUMN trigger_smart_rules TEXT NOT NULL DEFAULT '';
