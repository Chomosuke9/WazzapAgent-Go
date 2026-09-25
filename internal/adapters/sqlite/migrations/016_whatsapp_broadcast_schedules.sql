CREATE TABLE whatsapp_broadcast_schedules (
    id TEXT PRIMARY KEY NOT NULL,
    scheduled_at_ms INTEGER NOT NULL,
    format TEXT NOT NULL CHECK (format IN ('text', 'payload')),
    payload TEXT NOT NULL,
    batch_size INTEGER NOT NULL CHECK (batch_size BETWEEN 1 AND 100),
    batch_delay_seconds INTEGER NOT NULL CHECK (batch_delay_seconds BETWEEN 0 AND 300),
    targets_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('scheduled', 'sending', 'completed', 'partial', 'failed', 'cancelled')),
    results_json TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
) STRICT;

CREATE INDEX whatsapp_broadcast_schedules_due
    ON whatsapp_broadcast_schedules(status, scheduled_at_ms, created_at_ms);

CREATE INDEX whatsapp_broadcast_schedules_recent
    ON whatsapp_broadcast_schedules(status, updated_at_ms DESC);
