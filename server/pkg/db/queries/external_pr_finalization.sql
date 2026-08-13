-- Typed, replayable finalization intent for external PR completion only.

-- name: CreateExternalPRFinalization :one
INSERT INTO external_pr_reconcile_finalization (
    workspace_id, issue_id, work_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, intended_parent_id, activity_ids
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING
RETURNING id, workspace_id, issue_id, work_id, source_revision, source,
          previous_status, terminal_status, status_activity_id, intended_parent_id, activity_ids,
          state, parent_comment_id, activity_published, issue_published,
          comment_published, parent_wake_done, attempt, max_attempts,
          next_attempt_at, last_error_code, last_redacted_error, created_at,
          updated_at, completed_at, lease_owner, lease_token, lease_expires_at;

-- name: GetExternalPRFinalization :one
SELECT id, workspace_id, issue_id, work_id, source_revision, source,
       previous_status, terminal_status, status_activity_id, intended_parent_id, activity_ids,
       state, parent_comment_id, activity_published, issue_published,
       comment_published, parent_wake_done, attempt, max_attempts,
       next_attempt_at, last_error_code, last_redacted_error, created_at,
       updated_at, completed_at, lease_owner, lease_token, lease_expires_at
FROM external_pr_reconcile_finalization
WHERE id = $1;

-- name: GetExternalPRFinalizationForUpdate :one
SELECT id, workspace_id, issue_id, work_id, source_revision, source,
       previous_status, terminal_status, status_activity_id, intended_parent_id, activity_ids,
       state, parent_comment_id, activity_published, issue_published,
       comment_published, parent_wake_done, attempt, max_attempts,
       next_attempt_at, last_error_code, last_redacted_error, created_at,
       updated_at, completed_at, lease_owner, lease_token, lease_expires_at
FROM external_pr_reconcile_finalization
WHERE id = $1
FOR UPDATE;

-- name: GetExternalPRFinalizationForWork :one
SELECT id, workspace_id, issue_id, work_id, source_revision, source,
       previous_status, terminal_status, status_activity_id, intended_parent_id, activity_ids,
       state, parent_comment_id, activity_published, issue_published,
       comment_published, parent_wake_done, attempt, max_attempts,
       next_attempt_at, last_error_code, last_redacted_error, created_at,
       updated_at, completed_at, lease_owner, lease_token, lease_expires_at
FROM external_pr_reconcile_finalization
WHERE workspace_id = $1 AND work_id = $2
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetCurrentExternalPRTerminalStatusActivityID :one
SELECT a.id
FROM activity_log a
JOIN issue i ON i.id = a.issue_id AND i.workspace_id = a.workspace_id
WHERE a.workspace_id = $1
  AND a.issue_id = $2
  AND a.action = 'status_changed'
  AND a.details->>'to' = i.status
ORDER BY a.created_at DESC, a.id DESC
LIMIT 1;

-- name: UpdateExternalPRFinalization :execrows
UPDATE external_pr_reconcile_finalization
SET state = $2,
    parent_comment_id = $3,
    activity_published = $4,
    issue_published = $5,
    comment_published = $6,
    parent_wake_done = $7,
    attempt = $8,
    last_error_code = $9,
    last_redacted_error = $10,
    lease_owner = CASE WHEN $2 IN ('succeeded', 'recorded', 'dead') THEN NULL ELSE lease_owner END,
    lease_token = CASE WHEN $2 IN ('succeeded', 'recorded', 'dead') THEN NULL ELSE lease_token END,
    lease_expires_at = CASE WHEN $2 IN ('succeeded', 'recorded', 'dead') THEN NULL ELSE lease_expires_at END,
    completed_at = CASE WHEN $2 IN ('succeeded', 'recorded', 'dead') THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = $1 AND lease_token = $11;

-- name: CreateExternalPRFinalizationComment :one
WITH touched_issue AS (
    UPDATE issue SET updated_at = now()
    WHERE issue.id = $8 AND issue.workspace_id = $9
    RETURNING issue.id, issue.workspace_id
)
INSERT INTO comment (
    issue_id, workspace_id, author_type, author_id, content, type,
    parent_id, source_task_id, quick_action_id, finalization_key
)
SELECT ti.id, ti.workspace_id, $1, $2, $3, $4, $5, $6, $7, $10
FROM touched_issue ti
ON CONFLICT DO NOTHING
RETURNING id, issue_id, author_type, author_id, content, type, created_at,
          updated_at, parent_id, workspace_id, resolved_at, resolved_by_type,
          resolved_by_id, source_task_id, quick_action_id, finalization_key;

-- name: GetCommentByFinalizationKey :one
SELECT id, issue_id, author_type, author_id, content, type, created_at,
       updated_at, parent_id, workspace_id, resolved_at, resolved_by_type,
       resolved_by_id, source_task_id, quick_action_id, finalization_key
FROM comment
WHERE finalization_key = $1;

-- name: ExpireExternalPRFinalization :many
UPDATE external_pr_reconcile_finalization
SET state = 'dead',
    next_attempt_at = now(),
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_code = 'lease_expired_max_attempts',
    last_redacted_error = 'finalization lease expired after retry budget was exhausted',
    completed_at = now(),
    updated_at = now()
WHERE state IN ('pending', 'retry_wait')
  AND attempt >= max_attempts
  AND lease_expires_at < now()
RETURNING id, work_id;

-- name: MarkExternalPRReconcileWorksDeadForDeadFinalizations :execrows
UPDATE external_pr_reconcile_work w
SET state = 'dead',
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_code = 'finalization_dead',
    last_redacted_error = 'external PR finalization is permanently failed',
    completed_at = now(),
    updated_at = now()
FROM external_pr_reconcile_finalization f
WHERE f.work_id = w.id
  AND f.state = 'dead'
  AND w.state <> 'dead';

-- name: ListDueExternalPRFinalizationIDs :many
SELECT id
FROM external_pr_reconcile_finalization
WHERE state IN ('pending', 'retry_wait')
  AND attempt < max_attempts
  AND next_attempt_at <= now()
  AND (lease_token IS NULL OR lease_expires_at IS NULL OR lease_expires_at < now())
ORDER BY next_attempt_at ASC, updated_at ASC, id ASC
LIMIT $1;

-- name: ClaimDueExternalPRFinalization :one
WITH candidate AS (
    SELECT id
    FROM external_pr_reconcile_finalization
    WHERE state IN ('pending', 'retry_wait')
      AND attempt < max_attempts
      AND next_attempt_at <= now()
      AND (lease_token IS NULL OR lease_expires_at IS NULL OR lease_expires_at < now())
    ORDER BY next_attempt_at ASC, updated_at ASC, id ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE external_pr_reconcile_finalization f
SET lease_owner = $1,
    lease_token = gen_random_uuid(),
    lease_expires_at = now() + make_interval(secs => $2),
    attempt = f.attempt + 1,
    updated_at = now()
FROM candidate
WHERE f.id = candidate.id
RETURNING f.id, f.workspace_id, f.issue_id, f.work_id, f.source_revision, f.source,
          f.previous_status, f.terminal_status, f.status_activity_id, f.intended_parent_id, f.activity_ids,
          f.state, f.parent_comment_id, f.activity_published, f.issue_published,
          f.comment_published, f.parent_wake_done, f.attempt, f.max_attempts,
          f.next_attempt_at, f.last_error_code, f.last_redacted_error, f.created_at,
          f.updated_at, f.completed_at, f.lease_owner, f.lease_token, f.lease_expires_at;

-- name: ClaimExternalPRFinalizationByID :one
UPDATE external_pr_reconcile_finalization
SET lease_owner = $2,
    lease_token = gen_random_uuid(),
    lease_expires_at = now() + make_interval(secs => $3),
    attempt = attempt + 1,
    updated_at = now()
WHERE id = $1
  AND state IN ('pending', 'retry_wait')
  AND attempt < max_attempts
  AND next_attempt_at <= now()
  AND (lease_token IS NULL OR lease_expires_at IS NULL OR lease_expires_at < now())
RETURNING id, workspace_id, issue_id, work_id, source_revision, source,
          previous_status, terminal_status, status_activity_id, intended_parent_id, activity_ids,
          state, parent_comment_id, activity_published, issue_published,
          comment_published, parent_wake_done, attempt, max_attempts,
          next_attempt_at, last_error_code, last_redacted_error, created_at,
          updated_at, completed_at, lease_owner, lease_token, lease_expires_at;

-- name: RecordExternalPRFinalizationError :execrows
UPDATE external_pr_reconcile_finalization
SET state = CASE WHEN attempt >= max_attempts THEN 'dead' ELSE 'retry_wait' END,
    next_attempt_at = CASE
        WHEN attempt >= max_attempts THEN next_attempt_at
        ELSE now() + make_interval(secs => LEAST(
            GREATEST(60.0::double precision, power(5.0::double precision, GREATEST(attempt - 1, 0)) * 60.0::double precision),
            900.0::double precision
        ))
    END,
    last_error_code = @last_error_code,
    last_redacted_error = @last_redacted_error,
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    completed_at = CASE WHEN attempt >= max_attempts THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = $1 AND lease_token = $2 AND state IN ('pending', 'retry_wait');

-- name: HasAgentTaskForTriggerComment :one
SELECT EXISTS (
    SELECT 1
    FROM agent_task_queue
    WHERE trigger_comment_id = $1
       OR $1 = ANY(coalesced_comment_ids)
) AS has_task;
