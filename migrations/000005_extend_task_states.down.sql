-- Postgres has no DROP VALUE for enum types; the new values become inert.
-- Operators rolling back must finalize or cancel any tasks in pr_opened or
-- review_requested first, otherwise the rename below errors out.
ALTER TYPE task_status RENAME VALUE 'in_progress' TO 'running';

DROP INDEX IF EXISTS idx_tasks_heartbeat;
CREATE INDEX idx_tasks_heartbeat
    ON tasks (last_heartbeat_at)
    WHERE status IN ('claimed', 'running');
