-- Historical delegation rows have no foreign keys. These two cleanup queries
-- remain solely so workspace deletion can remove retired data explicitly.

-- name: DeleteWorkspacePRMergeDelegationEvents :exec
DELETE FROM workload_pr_merge_delegation_event WHERE workspace_id = $1;

-- name: DeleteWorkspacePRMergeDelegations :exec
DELETE FROM workload_pr_merge_delegation WHERE workspace_id = $1;
