-- Reverse MUL-4059 migration. Drops the two indexes and the four new
-- columns, then restores the original CHECK constraint. The down path
-- cannot know which context_guard / last_activity_at rows were still
-- semantically valid, so this is destructive; do not run on a system
-- where the new sweeper may still be active (it would simply stop
-- finding anything to sweep, but pre-existing pending_context rows
-- would be stranded with an unknown status once the CHECK constraint
-- snaps back).
DROP INDEX IF EXISTS idx_agent_task_queue_pending_context;
DROP INDEX IF EXISTS idx_agent_task_queue_running_activity;

ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS max_inactivity_secs;
ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS last_activity_at;
ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS context_guard_checked_at;
ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS context_guard;

ALTER TABLE agent_task_queue
    DROP CONSTRAINT agent_task_queue_status_check;

ALTER TABLE agent_task_queue
    ADD CONSTRAINT agent_task_queue_status_check
    CHECK (status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'completed', 'failed', 'cancelled'));