-- name: LockWorkspaceForPRMergeDelegation :one
SELECT id FROM workspace WHERE id = $1 FOR KEY SHARE;

-- name: LockTaskForPRMergeDelegation :one
SELECT task.* FROM agent_task_queue task
JOIN agent ON agent.id = task.agent_id
WHERE task.id = $1 AND agent.workspace_id = $2
FOR UPDATE OF task;

-- name: LockExternalPRProjectionForMerge :one
SELECT * FROM external_pull_request_link
WHERE workspace_id = sqlc.arg(workspace_id)
  AND issue_id = sqlc.arg(issue_id)
  AND provider = 'ags'
  AND external_number = sqlc.arg(ags_pr_number)
  AND merge_provider = 'forgejo'
  AND merge_number = sqlc.arg(provider_pr_number)
  AND link_confidence = 'authoritative'
  AND state IN ('open', 'draft')
FOR SHARE;

-- name: GetCurrentPRMergeDelegationForExecution :one
SELECT * FROM workload_pr_merge_delegation
WHERE workspace_id = sqlc.arg(workspace_id)
  AND task_id = sqlc.arg(task_id)
  AND execution_id = sqlc.arg(execution_id)
  AND operation = 'pr.merge'
  AND state IN ('pending_approval', 'approved')
FOR UPDATE;

-- name: CreatePendingPRMergeDelegation :one
INSERT INTO workload_pr_merge_delegation (
    workspace_id, issue_id, external_pr_link_id, task_id, execution_id, runtime_id,
    target_instance, canonical_repository_id, canonical_repository,
    provider_binding_id, provider_binding_revision, provider_repository,
    ags_pr_number, provider_pr_number, expected_head_sha, expected_base_sha,
    base_ref, merge_method, projection_facts_revision, facts_digest, state
) VALUES (
    sqlc.arg(workspace_id), sqlc.arg(issue_id), sqlc.arg(external_pr_link_id),
    sqlc.arg(task_id), sqlc.arg(execution_id), sqlc.arg(runtime_id),
    sqlc.arg(target_instance), sqlc.arg(canonical_repository_id), sqlc.arg(canonical_repository),
    sqlc.arg(provider_binding_id), sqlc.arg(provider_binding_revision), sqlc.arg(provider_repository),
    sqlc.arg(ags_pr_number), sqlc.arg(provider_pr_number), sqlc.arg(expected_head_sha),
    sqlc.arg(expected_base_sha), sqlc.arg(base_ref), sqlc.arg(merge_method),
    sqlc.arg(projection_facts_revision), sqlc.arg(facts_digest), 'pending_approval'
)
ON CONFLICT (workspace_id, task_id, execution_id, operation)
WHERE state IN ('pending_approval', 'approved')
DO NOTHING
RETURNING *;

-- name: ListPRMergeDelegationsInWorkspace :many
SELECT * FROM workload_pr_merge_delegation
WHERE workspace_id = $1
ORDER BY updated_at DESC, created_at DESC, id DESC;

-- name: ListCurrentPRMergeDelegationsForIssue :many
SELECT * FROM workload_pr_merge_delegation
WHERE workspace_id = sqlc.arg(workspace_id)
  AND issue_id = sqlc.arg(issue_id)
  AND state IN ('pending_approval', 'approved')
ORDER BY updated_at DESC, id DESC;

-- name: GetPRMergeDelegationByID :one
SELECT * FROM workload_pr_merge_delegation WHERE id = $1;

-- name: GetPRMergeDelegationInWorkspace :one
SELECT * FROM workload_pr_merge_delegation
WHERE id = $1 AND workspace_id = $2;

-- name: ApprovePRMergeDelegationInWorkspace :one
UPDATE workload_pr_merge_delegation
SET state = 'approved',
    approved_at = sqlc.arg(approved_at),
    approved_by_user_id = sqlc.arg(approved_by_user_id),
    not_after = sqlc.arg(not_after),
    updated_at = sqlc.arg(approved_at)
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND state = 'pending_approval'
RETURNING *;

-- name: RevokePRMergeDelegationInWorkspace :one
UPDATE workload_pr_merge_delegation
SET state = 'revoked',
    revoked_at = sqlc.arg(revoked_at),
    revoked_by_user_id = sqlc.arg(revoked_by_user_id),
    revocation_reason = sqlc.arg(revocation_reason),
    updated_at = sqlc.arg(revoked_at)
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND state IN ('pending_approval', 'approved')
RETURNING *;

-- name: LockPRMergeDelegationForService :one
SELECT * FROM workload_pr_merge_delegation WHERE id = $1 FOR UPDATE;

-- name: ConsumePRMergeDelegation :one
UPDATE workload_pr_merge_delegation
SET state = 'consumed',
    consumer_instance_id = sqlc.arg(consumer_instance_id),
    consumer_intent_id = sqlc.arg(consumer_intent_id),
    consume_request_digest = sqlc.arg(consume_request_digest),
    consumption_receipt_id = sqlc.arg(consumption_receipt_id),
    consumed_at = sqlc.arg(consumed_at),
    updated_at = sqlc.arg(consumed_at)
WHERE id = sqlc.arg(id)
  AND state = 'approved'
  AND not_after > sqlc.arg(consumed_at)
RETURNING *;

-- name: ExpirePRMergeDelegationByID :one
UPDATE workload_pr_merge_delegation
SET state = 'expired', updated_at = sqlc.arg(expired_at)
WHERE id = sqlc.arg(id) AND state = 'approved' AND not_after <= sqlc.arg(expired_at)
RETURNING *;

-- name: SupersedePRMergeDelegationByID :one
UPDATE workload_pr_merge_delegation
SET state = 'superseded',
    superseded_at = sqlc.arg(superseded_at),
    supersede_reason = sqlc.arg(supersede_reason),
    updated_at = sqlc.arg(superseded_at)
WHERE id = sqlc.arg(id)
  AND state IN ('pending_approval', 'approved')
RETURNING *;

-- name: SupersedePRMergeDelegationsForExternalLink :many
UPDATE workload_pr_merge_delegation
SET state = 'superseded',
    superseded_at = sqlc.arg(superseded_at),
    supersede_reason = sqlc.arg(supersede_reason),
    updated_at = sqlc.arg(superseded_at)
WHERE external_pr_link_id = sqlc.arg(external_pr_link_id)
  AND state IN ('pending_approval', 'approved')
  AND projection_facts_revision <> sqlc.arg(projection_facts_revision)
RETURNING *;

-- name: CreatePRMergeDelegationEvent :one
INSERT INTO workload_pr_merge_delegation_event (
    workspace_id, issue_id, delegation_id, event_type, actor_type, actor_id,
    consumer_intent_id, details
) VALUES (
    sqlc.arg(workspace_id), sqlc.arg(issue_id), sqlc.arg(delegation_id),
    sqlc.arg(event_type), sqlc.arg(actor_type), sqlc.arg(actor_id),
    sqlc.narg(consumer_intent_id), sqlc.arg(details)
)
RETURNING *;

-- name: ListPRMergeDelegationEvents :many
SELECT * FROM workload_pr_merge_delegation_event
WHERE delegation_id = $1
ORDER BY created_at ASC, id ASC;

-- name: DeleteWorkspacePRMergeDelegationEvents :exec
DELETE FROM workload_pr_merge_delegation_event WHERE workspace_id = $1;

-- name: DeleteWorkspacePRMergeDelegations :exec
DELETE FROM workload_pr_merge_delegation WHERE workspace_id = $1;
