CREATE TABLE group_metadata_state (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    ready INTEGER NOT NULL DEFAULT 0 CHECK (ready IN (0, 1)),
    PRIMARY KEY (tenant_id, account_id)
);

CREATE TABLE group_metadata (
    tenant_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    group_address TEXT NOT NULL,
    payload BLOB NOT NULL,
    observed_at_ms INTEGER NOT NULL CHECK (observed_at_ms > 0),
    PRIMARY KEY (tenant_id, account_id, group_address)
);
