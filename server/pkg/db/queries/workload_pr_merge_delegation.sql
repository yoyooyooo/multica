-- name: LockTaskForPRMergeDelegation :one
SELECT task.* FROM agent_task_queue task
JOIN agent ON agent.id = task.agent_id
WHERE task.id = $1 AND agent.workspace_id = $2
FOR UPDATE OF task;

-- name: RevokeCurrentPRMergeDelegation :exec
UPDATE workload_pr_merge_delegation
SET revoked_at = sqlc.arg(revoked_at),
    revoked_by_user_id = sqlc.arg(revoked_by_user_id),
    revocation_reason = sqlc.arg(revocation_reason),
    updated_at = sqlc.arg(revoked_at)
WHERE workspace_id = sqlc.arg(workspace_id)
  AND task_id = sqlc.arg(task_id)
  AND run_id = sqlc.arg(run_id)
  AND operation = 'pr.merge'
  AND revoked_at IS NULL;

-- name: CreatePRMergeDelegation :one
INSERT INTO workload_pr_merge_delegation (
    workspace_id,
    task_id,
    run_id,
    operation,
    repository,
    pull_request_number,
    forgejo_pull_request_number,
    expected_head_sha,
    merge_method,
    granted_by_user_id,
    granted_at,
    expires_at
) VALUES (
    sqlc.arg(workspace_id),
    sqlc.arg(task_id),
    sqlc.arg(run_id),
    'pr.merge',
    sqlc.arg(repository),
    sqlc.arg(pull_request_number),
    sqlc.arg(forgejo_pull_request_number),
    sqlc.arg(expected_head_sha),
    sqlc.arg(merge_method),
    sqlc.arg(granted_by_user_id),
    sqlc.arg(granted_at),
    sqlc.arg(expires_at)
)
RETURNING *;

-- name: GetPRMergeDelegationInWorkspace :one
SELECT * FROM workload_pr_merge_delegation
WHERE id = $1 AND workspace_id = $2;

-- name: RevokePRMergeDelegationInWorkspace :one
UPDATE workload_pr_merge_delegation
SET revoked_at = sqlc.arg(revoked_at),
    revoked_by_user_id = sqlc.arg(revoked_by_user_id),
    revocation_reason = sqlc.arg(revocation_reason),
    updated_at = sqlc.arg(revoked_at)
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND revoked_at IS NULL
RETURNING *;

-- name: LockActivePRMergeDelegationForAssertion :one
SELECT * FROM workload_pr_merge_delegation
WHERE workspace_id = sqlc.arg(workspace_id)
  AND task_id = sqlc.arg(task_id)
  AND run_id = sqlc.arg(run_id)
  AND operation = 'pr.merge'
  AND repository = sqlc.arg(repository)
  AND pull_request_number = sqlc.arg(pull_request_number)
  AND forgejo_pull_request_number = sqlc.arg(forgejo_pull_request_number)
  AND expected_head_sha = sqlc.arg(expected_head_sha)
  AND merge_method = sqlc.arg(merge_method)
  AND revoked_at IS NULL
  AND expires_at > sqlc.arg(asserted_at)
FOR SHARE;

-- name: DeleteWorkspacePRMergeDelegations :exec
DELETE FROM workload_pr_merge_delegation WHERE workspace_id = $1;
