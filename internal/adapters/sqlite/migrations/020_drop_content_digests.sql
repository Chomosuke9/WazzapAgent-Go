-- Drop the SHA-256 digests used to tell idempotent replay from an ID
-- collision. IDs are minted by this process, so a collision could only be a
-- local bug, and every history read re-hashed rows to check them.
--
-- invocation_digest also marked "this inbound event was claimed for a turn
-- or a command reply"; turn_claimed keeps that meaning explicitly.
ALTER TABLE inbound_events ADD COLUMN turn_claimed INTEGER NOT NULL DEFAULT 0 CHECK (turn_claimed IN (0, 1));
UPDATE inbound_events SET turn_claimed = 1 WHERE invocation_digest IS NOT NULL;
ALTER TABLE inbound_events DROP COLUMN invocation_digest;
ALTER TABLE history_entries DROP COLUMN content_digest;
