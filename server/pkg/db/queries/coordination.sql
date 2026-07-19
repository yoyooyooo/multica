-- name: CoordinationAdvisoryXactLock :exec
SELECT pg_advisory_xact_lock(@namespace::int4, @workspace_key::int4);

-- name: CoordinationAdvisorySessionLock :exec
SELECT pg_advisory_lock(@namespace::int4, @workspace_key::int4);

-- name: CoordinationAdvisorySessionUnlock :one
SELECT pg_advisory_unlock(@namespace::int4, @workspace_key::int4)::bool;

-- name: CreateCoordinationScope :one
INSERT INTO coordination_scope (
    id, workspace_id, scope_kind, state, root_issue_id, workflow_profile_key,
    revision, next_receipt_ordinal, created_by_type, created_by_id, created_task_id,
    created_at, updated_at
) VALUES (
    @id, @workspace_id, 'root', 'active', @root_issue_id, @workflow_profile_key,
    0, 0, @created_by_type, @created_by_id, sqlc.narg('created_task_id'),
    clock_timestamp(), clock_timestamp()
)
RETURNING *;

-- name: GetCoordinationScopeByID :one
SELECT * FROM coordination_scope
WHERE workspace_id = @workspace_id AND id = @id;

-- name: GetActiveCoordinationScopeByRoot :one
SELECT * FROM coordination_scope
WHERE workspace_id = @workspace_id
  AND root_issue_id = @root_issue_id
  AND workflow_profile_key = @workflow_profile_key
  AND state = 'active';

-- name: LockCoordinationScope :one
SELECT * FROM coordination_scope
WHERE workspace_id = @workspace_id AND id = @id
FOR UPDATE;

-- name: IncrementCoordinationScopeRevisionCAS :one
UPDATE coordination_scope
SET revision = revision + 1,
    updated_at = clock_timestamp()
WHERE workspace_id = @workspace_id
  AND id = @id
  AND revision = @expected_revision
RETURNING *;

-- name: AllocateCoordinationReceiptOrdinal :one
UPDATE coordination_scope
SET next_receipt_ordinal = next_receipt_ordinal + 1
WHERE workspace_id = @workspace_id
  AND id = @id
  AND next_receipt_ordinal < 9223372036854775807
RETURNING next_receipt_ordinal;

-- name: GetCoordinationReceiptByIdempotencyKey :one
SELECT * FROM coordination_receipt
WHERE workspace_id = @workspace_id AND idempotency_key = @idempotency_key;

-- name: InsertCoordinationReceipt :one
INSERT INTO coordination_receipt (
    id, workspace_id, coordination_scope_id, receipt_ordinal, operation,
    idempotency_key, request_hash, resource_type, resource_id, revision_before,
    revision_after, result_snapshot, actor_type, actor_id, actor_task_id, created_at
) VALUES (
    @id, @workspace_id, @coordination_scope_id, @receipt_ordinal, @operation,
    @idempotency_key, @request_hash, @resource_type, @resource_id, @revision_before,
    @revision_after, @result_snapshot, @actor_type, @actor_id, sqlc.narg('actor_task_id'), clock_timestamp()
)
RETURNING *;

-- name: GetCoordinationReceiptByScopeOrdinal :one
SELECT * FROM coordination_receipt
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND receipt_ordinal = @receipt_ordinal;

-- name: ListCoordinationReceiptsByScope :many
SELECT * FROM coordination_receipt
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND (sqlc.narg('before_ordinal')::bigint IS NULL OR receipt_ordinal < sqlc.narg('before_ordinal')::bigint)
ORDER BY receipt_ordinal DESC
LIMIT @limit_rows;

-- name: ValidateIssueActualRoot :one
WITH RECURSIVE chain AS (
    SELECT
        i.id,
        i.parent_issue_id,
        ARRAY[i.id]::uuid[] AS path,
        0::int AS depth,
        NULL::text AS stop_reason
    FROM issue i
    WHERE i.workspace_id = @workspace_id AND i.id = @issue_id
  UNION ALL
    SELECT
        COALESCE(p.id, chain.parent_issue_id) AS id,
        CASE WHEN p.id IS NULL THEN NULL ELSE p.parent_issue_id END AS parent_issue_id,
        CASE
            WHEN p.id IS NULL OR chain.parent_issue_id = ANY(chain.path) THEN chain.path
            ELSE chain.path || p.id
        END AS path,
        chain.depth + 1 AS depth,
        CASE
            WHEN chain.depth >= 256 THEN 'depth_exceeded'
            WHEN chain.parent_issue_id = ANY(chain.path) THEN 'cycle'
            WHEN p.id IS NULL AND foreign_parent.id IS NOT NULL THEN 'foreign_parent'
            WHEN p.id IS NULL THEN 'missing'
            ELSE NULL
        END AS stop_reason
    FROM chain
    LEFT JOIN issue p
        ON p.workspace_id = @workspace_id
       AND p.id = chain.parent_issue_id
    LEFT JOIN issue foreign_parent
        ON foreign_parent.id = chain.parent_issue_id
       AND foreign_parent.workspace_id <> @workspace_id
    WHERE chain.parent_issue_id IS NOT NULL
      AND chain.stop_reason IS NULL
), requested AS (
    SELECT
        EXISTS (SELECT 1 FROM issue WHERE id = @issue_id) AS exists_any_workspace,
        EXISTS (SELECT 1 FROM issue WHERE workspace_id = @workspace_id AND id = @issue_id) AS exists_in_workspace
), terminal AS (
    SELECT id, parent_issue_id, depth, stop_reason
    FROM chain
    ORDER BY depth DESC
    LIMIT 1
)
SELECT
    CASE
        WHEN NOT requested.exists_any_workspace THEN 'missing'
        WHEN NOT requested.exists_in_workspace THEN 'cross_workspace'
        WHEN terminal.stop_reason IS NOT NULL THEN terminal.stop_reason
        WHEN terminal.parent_issue_id IS NOT NULL THEN 'depth_exceeded'
        ELSE 'ok'
    END::text AS status,
    CASE
        WHEN requested.exists_in_workspace AND terminal.stop_reason IS NULL AND terminal.parent_issue_id IS NULL THEN terminal.id
        ELSE NULL::uuid
    END AS root_issue_id,
    COALESCE(terminal.depth, 0)::int AS depth
FROM requested
LEFT JOIN terminal ON true;

-- name: GetActiveCoordinationDependencyByPair :one
SELECT * FROM coordination_dependency
WHERE workspace_id = @workspace_id
  AND downstream_issue_id = @downstream_issue_id
  AND upstream_issue_id = @upstream_issue_id
  AND resolved_at IS NULL;

-- name: CreateCoordinationDependency :one
INSERT INTO coordination_dependency (
    id, workspace_id, coordination_scope_id, downstream_issue_id, upstream_issue_id,
    created_by_type, created_by_id, created_task_id, created_at
) VALUES (
    @id, @workspace_id, @coordination_scope_id, @downstream_issue_id, @upstream_issue_id,
    @created_by_type, @created_by_id, sqlc.narg('created_task_id'), clock_timestamp()
)
RETURNING *;

-- name: GetCoordinationDependencyByID :one
SELECT * FROM coordination_dependency
WHERE workspace_id = @workspace_id AND id = @id;

-- name: LockCoordinationDependency :one
SELECT * FROM coordination_dependency
WHERE workspace_id = @workspace_id AND id = @id
FOR UPDATE;

-- name: ListActiveCoordinationDependenciesByScope :many
SELECT * FROM coordination_dependency
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND resolved_at IS NULL
  AND (
      sqlc.narg('visible_endpoint_issue_id')::uuid IS NULL
      OR downstream_issue_id = sqlc.narg('visible_endpoint_issue_id')::uuid
      OR upstream_issue_id = sqlc.narg('visible_endpoint_issue_id')::uuid
  )
  AND (
      sqlc.narg('cursor_created_at')::timestamptz IS NULL
      OR (created_at, id) > (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
ORDER BY created_at ASC, id ASC
LIMIT @limit_rows;

-- name: CountActiveCoordinationDependenciesByScope :one
SELECT count(*)::bigint
FROM coordination_dependency
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND resolved_at IS NULL;

-- name: ResolveCoordinationDependency :one
UPDATE coordination_dependency
SET resolved_by_type = @resolved_by_type,
    resolved_by_id = @resolved_by_id,
    resolved_task_id = sqlc.narg('resolved_task_id'),
    resolved_at = clock_timestamp()
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND id = @id
  AND resolved_at IS NULL
RETURNING *;

-- name: CoordinationDependencyPathExists :one
WITH RECURSIVE reachable(issue_id) AS (
    SELECT @start_issue_id::uuid
  UNION
    SELECT dependency.upstream_issue_id
    FROM reachable
    JOIN coordination_dependency dependency
      ON dependency.workspace_id = @workspace_id
     AND dependency.downstream_issue_id = reachable.issue_id
     AND dependency.resolved_at IS NULL
)
SELECT EXISTS (
    SELECT 1 FROM reachable WHERE reachable.issue_id = @target_issue_id::uuid
)::bool;

-- name: CreateCoordinationRecord :one
INSERT INTO coordination_record (
    id, workspace_id, coordination_scope_id, kind, schema_version, status,
    root_issue_id, downstream_issue_id, upstream_issue_id, dependency_id,
    reason_code, created_by_type, created_by_id, created_task_id, created_at
) VALUES (
    @id, @workspace_id, @coordination_scope_id, 'blocker', 1, 'open',
    @root_issue_id, @downstream_issue_id, @upstream_issue_id, sqlc.narg('dependency_id'),
    'waiting_on_issue', @created_by_type, @created_by_id, sqlc.narg('created_task_id'), clock_timestamp()
)
RETURNING *;

-- name: GetCoordinationRecordByID :one
SELECT * FROM coordination_record
WHERE workspace_id = @workspace_id AND id = @id;

-- name: LockCoordinationRecord :one
SELECT * FROM coordination_record
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND id = @id
FOR UPDATE;

-- name: CountOpenCoordinationRecordsByScope :one
SELECT count(*)::bigint
FROM coordination_record
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND status = 'open';

-- name: ListCoordinationRecordsByScope :many
SELECT * FROM coordination_record
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND (@status_filter::text = 'all' OR status = @status_filter::text)
  AND (
      sqlc.narg('visible_endpoint_issue_id')::uuid IS NULL
      OR downstream_issue_id = sqlc.narg('visible_endpoint_issue_id')::uuid
      OR upstream_issue_id = sqlc.narg('visible_endpoint_issue_id')::uuid
  )
  AND (
      sqlc.narg('cursor_created_at')::timestamptz IS NULL
      OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT @limit_rows;

-- name: ResolveCoordinationRecord :one
UPDATE coordination_record
SET status = 'resolved',
    resolution_code = @resolution_code,
    resolved_by_type = @resolved_by_type,
    resolved_by_id = @resolved_by_id,
    resolved_task_id = sqlc.narg('resolved_task_id'),
    resolved_at = clock_timestamp()
WHERE workspace_id = @workspace_id
  AND coordination_scope_id = @coordination_scope_id
  AND id = @id
  AND status = 'open'
RETURNING *;

-- name: InsertCoordinationRecordIssueRef :one
INSERT INTO coordination_record_issue_ref (
    id, workspace_id, coordination_scope_id, record_id, phase, issue_id, position, created_at
) VALUES (
    @id, @workspace_id, @coordination_scope_id, @record_id, @phase, @issue_id, @position, clock_timestamp()
)
RETURNING *;

-- name: ListCoordinationRecordIssueRefs :many
SELECT * FROM coordination_record_issue_ref
WHERE workspace_id = @workspace_id
  AND record_id = @record_id
  AND phase = @phase
ORDER BY position ASC, issue_id ASC;

-- name: CountCoordinationRecordsByIssueIDs :one
SELECT (
    (SELECT count(*) FROM coordination_record record
     WHERE record.workspace_id = sqlc.arg('workspace_id')
       AND (
           record.root_issue_id = ANY(sqlc.arg('issue_ids')::uuid[])
           OR record.downstream_issue_id = ANY(sqlc.arg('issue_ids')::uuid[])
           OR record.upstream_issue_id = ANY(sqlc.arg('issue_ids')::uuid[])
       ))
    +
    (SELECT count(*) FROM coordination_record_issue_ref ref
     WHERE ref.workspace_id = sqlc.arg('workspace_id')
       AND ref.issue_id = ANY(sqlc.arg('issue_ids')::uuid[]))
)::bigint;

-- name: CountCoordinationRecordsByWorkspace :one
SELECT (
    (SELECT count(*) FROM coordination_record record
     WHERE record.workspace_id = sqlc.arg('workspace_id'))
    +
    (SELECT count(*) FROM coordination_record_issue_ref ref
     WHERE ref.workspace_id = sqlc.arg('workspace_id'))
)::bigint;

-- name: CountCoordinationDependenciesByIssueIDs :one
SELECT count(*)::bigint
FROM coordination_dependency
WHERE workspace_id = @workspace_id
  AND (
      downstream_issue_id = ANY(@issue_ids::uuid[])
      OR upstream_issue_id = ANY(@issue_ids::uuid[])
  );

-- name: CountCoordinationDependenciesByWorkspace :one
SELECT count(*)::bigint
FROM coordination_dependency
WHERE workspace_id = @workspace_id;

-- name: LockIssuesForCoordinationDelete :many
SELECT * FROM issue
WHERE workspace_id = @workspace_id
  AND id = ANY(@issue_ids::uuid[])
ORDER BY id
FOR UPDATE;

-- name: CountCoordinationScopesByRootIssues :one
SELECT count(*)::bigint FROM coordination_scope
WHERE workspace_id = @workspace_id
  AND root_issue_id = ANY(@issue_ids::uuid[])
  AND state = 'active';

-- name: CountCoordinationScopesByWorkspace :one
SELECT count(*)::bigint FROM coordination_scope
WHERE workspace_id = @workspace_id;

-- name: LockWorkspaceForCoordinationDelete :one
SELECT * FROM workspace
WHERE id = @workspace_id
FOR UPDATE;

-- name: CancelAgentTasksByWorkspaceForCoordination :many
UPDATE agent_task_queue
SET status = 'cancelled', completed_at = now(), prepare_lease_expires_at = NULL
WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = @workspace_id)
  AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
RETURNING *;

-- name: DeleteTaskTokensByWorkspaceForCoordination :exec
DELETE FROM task_token WHERE workspace_id = @workspace_id;

-- name: FailAutopilotRunsByWorkspaceForCoordination :exec
UPDATE autopilot_run
SET status = 'failed', completed_at = now(), failure_reason = 'workspace was deleted'
WHERE autopilot_id IN (SELECT id FROM autopilot WHERE workspace_id = @workspace_id)
  AND status IN ('issue_created', 'running');
