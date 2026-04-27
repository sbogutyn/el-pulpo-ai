ALTER TYPE task_status RENAME VALUE 'running' TO 'in_progress';
ALTER TYPE task_status ADD VALUE 'pr_opened'        AFTER 'in_progress';
ALTER TYPE task_status ADD VALUE 'review_requested' AFTER 'pr_opened';

DROP INDEX IF EXISTS idx_tasks_heartbeat;
CREATE INDEX idx_tasks_heartbeat
    ON tasks (last_heartbeat_at)
    WHERE status IN ('claimed', 'in_progress');
