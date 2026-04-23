CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TYPE task_status AS ENUM (
    'pending', 'claimed', 'running', 'completed', 'failed'
);

CREATE TABLE tasks (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              TEXT NOT NULL,
    payload           JSONB NOT NULL DEFAULT '{}'::jsonb,
    priority          INTEGER NOT NULL DEFAULT 0,
    status            task_status NOT NULL DEFAULT 'pending',
    scheduled_for     TIMESTAMPTZ,
    attempt_count     INTEGER NOT NULL DEFAULT 0,
    max_attempts      INTEGER NOT NULL DEFAULT 3,
    claimed_by        TEXT,
    claimed_at        TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    last_error        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tasks_claimable
    ON tasks (priority DESC, created_at ASC)
    WHERE status = 'pending';

CREATE INDEX idx_tasks_heartbeat
    ON tasks (last_heartbeat_at)
    WHERE status IN ('claimed', 'running');
