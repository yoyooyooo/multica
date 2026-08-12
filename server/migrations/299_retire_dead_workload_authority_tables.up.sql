-- T016: retire Multica dead workload authority + PR merge delegation schema.
-- No CASCADE. External PR tables and execution_id columns are intentionally kept.
-- Index drops for these tables are in migrations 292-298 (CONCURRENTLY, one each).
--
-- Bounded lock timeout matches other fork reconciliation migrations: fail closed
-- under write pressure rather than queue unbounded locks. Retry after drain.
BEGIN;
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS workspace_workload_authority_on_member_change ON member;
DROP TRIGGER IF EXISTS workspace_workload_authority_on_workspace_create ON workspace;

DROP FUNCTION IF EXISTS advance_workspace_workload_membership_epoch();
DROP FUNCTION IF EXISTS advance_existing_workspace_workload_membership_epoch();
DROP FUNCTION IF EXISTS ensure_workspace_workload_authority();

DROP TABLE IF EXISTS workload_pr_merge_delegation_event;
DROP TABLE IF EXISTS workload_pr_merge_delegation;
DROP TABLE IF EXISTS workspace_workload_authority;
COMMIT;
