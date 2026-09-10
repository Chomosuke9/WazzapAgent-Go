ALTER TABLE agent_configs ADD COLUMN moderation_level INTEGER NOT NULL DEFAULT 0 CHECK (moderation_level BETWEEN 0 AND 3);

-- The previous Part 3 draft used this column for UX features. Those grants
-- are intentionally revoked during migration; react is now always derived and
-- mark-read/presence are runtime automation rather than model permissions.
UPDATE agent_configs SET model_capabilities = '[]';
