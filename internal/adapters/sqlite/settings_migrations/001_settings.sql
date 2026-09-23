CREATE TABLE application_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    revision INTEGER NOT NULL CHECK (revision > 0),
    values_json TEXT NOT NULL CHECK (length(values_json) > 0 AND length(values_json) <= 1048576),
    updated_at_ms INTEGER NOT NULL
) STRICT;

CREATE TABLE session_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    active_tenant_id TEXT,
    active_account_id TEXT,
    whatsapp_account_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('unpaired', 'paired', 'revoked')),
    pending_tenant_id TEXT,
    pending_account_id TEXT,
    updated_at_ms INTEGER NOT NULL,
    CHECK ((pending_tenant_id IS NULL AND pending_account_id IS NULL)
        OR (pending_tenant_id IS NOT NULL AND pending_account_id IS NOT NULL)),
    CHECK ((active_tenant_id IS NULL AND active_account_id IS NULL)
        OR (active_tenant_id IS NOT NULL AND active_account_id IS NOT NULL))
) STRICT;
