-- Drop the lease and batch-staging columns. One process owns the database
-- (the data-root lock) and one in-memory worker owns each chat, so a
-- conditional state update is the only claim a row needs. Debounced batches
-- now live in memory until they are claimed.
--
-- The claimed state (2) meant "leased but not started"; nothing was sent, so
-- those rows go back to pending. Retryable actions (5) are pending too now.
UPDATE outbound_actions SET state = 1, action_lease = NULL, action_lease_until_ms = NULL
    WHERE state IN (2, 5);
UPDATE typed_effects SET state = 1, effect_lease = NULL, effect_lease_until_ms = NULL
    WHERE state = 2;

DROP INDEX inbound_events_batch_idx;
DROP INDEX inbound_events_batch_ready_idx;
ALTER TABLE inbound_events DROP COLUMN batch_ready_at_ms;
ALTER TABLE inbound_events DROP COLUMN batch_anchor_invocation_id;
ALTER TABLE inbound_events DROP COLUMN batch_position;
ALTER TABLE inbound_events DROP COLUMN generation_lease;
ALTER TABLE inbound_events DROP COLUMN generation_lease_until_ms;
ALTER TABLE inbound_events DROP COLUMN retry_after_ms;

DROP INDEX outbound_actions_state_idx;
ALTER TABLE outbound_actions DROP COLUMN action_lease;
ALTER TABLE outbound_actions DROP COLUMN action_lease_until_ms;
ALTER TABLE outbound_actions DROP COLUMN retry_after_ms;
CREATE INDEX outbound_actions_state_idx ON outbound_actions(tenant_id, state, created_at_ms);

ALTER TABLE typed_effects DROP COLUMN effect_lease;
ALTER TABLE typed_effects DROP COLUMN effect_lease_until_ms;
