DROP TRIGGER IF EXISTS workspace_workload_authority_on_member_change ON member;
DROP FUNCTION IF EXISTS advance_workspace_workload_membership_epoch();
DROP TRIGGER IF EXISTS workspace_workload_authority_on_workspace_create ON workspace;
DROP FUNCTION IF EXISTS ensure_workspace_workload_authority();
DROP TABLE IF EXISTS workspace_workload_authority;
