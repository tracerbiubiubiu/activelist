CREATE TABLE _al_b3_stream (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    generation BIGINT NOT NULL REFERENCES data_types(id),
    profile_sha256 TEXT NOT NULL,
    cursor BIGINT NOT NULL DEFAULT 0,
    ack_cursor BIGINT NOT NULL DEFAULT 0 CHECK (ack_cursor >= 0 AND ack_cursor <= cursor)
);

CREATE TABLE _al_b3_outbox (
    cursor BIGINT PRIMARY KEY CHECK (cursor > 0),
    event_id TEXT NOT NULL UNIQUE,
    event BYTEA NOT NULL,
    payload_sha256 TEXT NOT NULL,
    attempts BIGINT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    lease_until TIMESTAMPTZ,
    stage_commit_ref TEXT
);
