-- Typed external-PR terminal continuation only. This query file must not grow
-- into a generic effect or continuation ledger.

-- name: EnqueueExternalPRTerminalWork :exec
INSERT INTO external_pr_reconcile_work (
    workspace_id, issue_id, link_id, kind, provider, external_repo,
    external_number, source_revision, source_idempotency_key,
    next_attempt_at
) VALUES ($1, $2, $3, 'external_pr_terminal', $4, $5, $6, $7, $8, now())
ON CONFLICT (workspace_id, kind, link_id, source_revision) DO UPDATE
SET provider = EXCLUDED.provider,
    external_repo = EXCLUDED.external_repo,
    external_number = EXCLUDED.external_number,
    source_idempotency_key = EXCLUDED.source_idempotency_key,
    updated_at = now(),
    next_attempt_at = CASE
        WHEN external_pr_reconcile_work.state = 'pending'
            THEN LEAST(external_pr_reconcile_work.next_attempt_at, now())
        WHEN external_pr_reconcile_work.state = 'retry_wait'
             AND NOT EXISTS (
                 SELECT 1
                 FROM external_pr_reconcile_finalization f
                 WHERE f.work_id = external_pr_reconcile_work.id
                   AND f.state IN ('pending', 'retry_wait')
             )
            THEN LEAST(external_pr_reconcile_work.next_attempt_at, now())
        ELSE external_pr_reconcile_work.next_attempt_at
    END;

-- name: SweepExternalPRTerminalWork :execrows
INSERT INTO external_pr_reconcile_work (
    workspace_id, issue_id, link_id, kind, provider, external_repo,
    external_number, source_revision, source_idempotency_key,
    next_attempt_at
)
SELECT l.workspace_id, l.issue_id, l.id, 'external_pr_terminal', l.provider,
       l.external_repo, l.external_number,
       md5(concat_ws(chr(31), l.id::text, l.state, COALESCE(l.merged_sha, ''), l.link_confidence, CASE WHEN l.completion_intent THEN 'true' ELSE 'false' END)),
       NULL::text, now()
FROM external_pull_request_link l
WHERE l.state IN ('closed', 'merged')
ON CONFLICT (workspace_id, kind, link_id, source_revision) DO UPDATE
SET provider = EXCLUDED.provider,
    external_repo = EXCLUDED.external_repo,
    external_number = EXCLUDED.external_number,
    source_idempotency_key = COALESCE(EXCLUDED.source_idempotency_key, external_pr_reconcile_work.source_idempotency_key),
    updated_at = now(),
    next_attempt_at = CASE
        WHEN external_pr_reconcile_work.state = 'pending'
            THEN LEAST(external_pr_reconcile_work.next_attempt_at, now())
        WHEN external_pr_reconcile_work.state = 'retry_wait'
             AND NOT EXISTS (
                 SELECT 1
                 FROM external_pr_reconcile_finalization f
                 WHERE f.work_id = external_pr_reconcile_work.id
                   AND f.state IN ('pending', 'retry_wait')
             )
            THEN LEAST(external_pr_reconcile_work.next_attempt_at, now())
        ELSE external_pr_reconcile_work.next_attempt_at
    END;

-- name: DeferExternalPRReconcileWorkForFinalization :execrows
UPDATE external_pr_reconcile_work w
SET state = 'retry_wait',
    attempt = GREATEST(w.attempt - 1, 0),
    next_attempt_at = GREATEST(
        now() + interval '1 minute',
        COALESCE(f.next_attempt_at, now() + interval '1 minute'),
        COALESCE(f.lease_expires_at, now() + interval '1 minute')
    ),
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    completed_at = NULL,
    updated_at = now()
FROM external_pr_reconcile_finalization f
WHERE w.id = $1
  AND w.lease_token = $2
  AND w.state = 'claimed'
  AND f.id = $3
  AND f.state IN ('pending', 'retry_wait');

-- name: ExpireExternalPRReconcileWork :execrows
UPDATE external_pr_reconcile_work
SET state = 'dead',
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_code = 'lease_expired_max_attempts',
    last_redacted_error = 'lease expired after retry budget was exhausted',
    completed_at = now(),
    updated_at = now()
WHERE state = 'claimed'
  AND lease_expires_at < now()
  AND attempt >= max_attempts;

-- name: MarkExternalPRReconcileWorkDeadAfterFinalization :execrows
UPDATE external_pr_reconcile_work
SET state = 'dead',
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_code = 'finalization_dead',
    last_redacted_error = 'external PR finalization is permanently failed',
    completed_at = now(),
    updated_at = now()
WHERE id = $1
  AND state <> 'dead';

-- name: GetExternalPRReconcileWork :one
SELECT id, workspace_id, issue_id, link_id, kind, provider,
       external_repo, external_number, source_revision,
       source_idempotency_key, state, attempt, max_attempts,
       next_attempt_at, lease_owner, lease_token, lease_expires_at,
       last_error_code, last_redacted_error, created_at, updated_at,
       completed_at
FROM external_pr_reconcile_work
WHERE id = $1;

-- name: ClaimExternalPRReconcileWork :one
WITH candidate AS (
    SELECT id
    FROM external_pr_reconcile_work
    WHERE attempt < max_attempts
      AND (
          (state IN ('pending', 'retry_wait') AND next_attempt_at <= now())
          OR (state = 'claimed' AND lease_expires_at < now())
      )
    ORDER BY next_attempt_at ASC, updated_at ASC, id ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE external_pr_reconcile_work w
SET state = 'claimed',
    attempt = w.attempt + 1,
    lease_owner = $1,
    lease_token = gen_random_uuid(),
    lease_expires_at = now() + make_interval(secs => $2),
    updated_at = now(),
    last_error_code = NULL,
    last_redacted_error = NULL
FROM candidate
WHERE w.id = candidate.id
RETURNING w.id, w.workspace_id, w.issue_id, w.link_id, w.kind, w.provider,
          w.external_repo, w.external_number, w.source_revision,
          w.source_idempotency_key, w.state, w.attempt, w.max_attempts,
          w.next_attempt_at, w.lease_owner, w.lease_token,
          w.lease_expires_at, w.last_error_code, w.last_redacted_error,
          w.created_at, w.updated_at, w.completed_at;

-- name: ClaimExternalPRReconcileWorkByID :one
UPDATE external_pr_reconcile_work
SET state = 'claimed',
    attempt = attempt + 1,
    lease_owner = $2,
    lease_token = gen_random_uuid(),
    lease_expires_at = now() + make_interval(secs => $3),
    updated_at = now(),
    last_error_code = NULL,
    last_redacted_error = NULL
WHERE id = $1
  AND attempt < max_attempts
  AND (
      (state IN ('pending', 'retry_wait') AND next_attempt_at <= now())
      OR (state = 'claimed' AND lease_expires_at < now())
  )
RETURNING id, workspace_id, issue_id, link_id, kind, provider,
          external_repo, external_number, source_revision,
          source_idempotency_key, state, attempt, max_attempts,
          next_attempt_at, lease_owner, lease_token,
          lease_expires_at, last_error_code, last_redacted_error,
          created_at, updated_at, completed_at;

-- name: CompleteExternalPRReconcileWork :execrows
UPDATE external_pr_reconcile_work
SET state = $3,
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND lease_token = $2 AND state = 'claimed';

-- name: FailExternalPRReconcileWork :execrows
UPDATE external_pr_reconcile_work
SET state = CASE WHEN attempt >= max_attempts THEN 'dead' ELSE 'retry_wait' END,
    next_attempt_at = CASE
        WHEN attempt >= max_attempts THEN next_attempt_at
        ELSE now() + make_interval(secs => LEAST(GREATEST(@delay_seconds::double precision, 0.0::double precision), 900.0::double precision))
    END,
    last_error_code = @last_error_code,
    last_redacted_error = @last_redacted_error,
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    completed_at = CASE WHEN attempt >= max_attempts THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = $1 AND lease_token = $2 AND state = 'claimed';
