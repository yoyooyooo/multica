-- name: GetWorkspaceWorkloadAuthority :one
SELECT * FROM workspace_workload_authority
WHERE workspace_id = $1;
