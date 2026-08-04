-- name: CreateTaskToken :one
INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, execution_id, expires_at)
VALUES (@token_hash, @task_id, @agent_id, @workspace_id, @user_id, @execution_id, @expires_at)
RETURNING *;

-- name: GetTaskTokenByHash :one
-- A task token is executable authority only while both the token and its task
-- are live. Terminal task-token rows may survive best-effort cleanup, but they
-- must not authenticate any new request.
SELECT tt.* FROM task_token tt
JOIN agent_task_queue atq ON atq.id = tt.task_id
WHERE tt.token_hash = $1
  AND tt.expires_at > now()
  AND atq.status = 'running'
  AND tt.execution_id IS NOT DISTINCT FROM atq.execution_id;

-- name: LockRunningTaskTokenForAssertion :one
-- Linearization point for assertion issuance versus task terminalization.
-- Complete/fail/cancel updates the same agent_task_queue row and therefore
-- waits if assertion issuance locked it first; if terminalization commits first,
-- this query returns no row and no assertion is minted.
SELECT tt.* FROM task_token tt
JOIN agent_task_queue atq ON atq.id = tt.task_id
WHERE tt.token_hash = sqlc.arg('token_hash')
  AND tt.task_id = sqlc.arg('task_id')
  AND tt.workspace_id = sqlc.arg('workspace_id')
  AND tt.expires_at > now()
  AND atq.status = 'running'
  AND tt.execution_id IS NOT DISTINCT FROM atq.execution_id
FOR UPDATE OF tt, atq;

-- name: DeleteTaskTokensByTask :exec
DELETE FROM task_token WHERE task_id = $1;

-- name: DeleteExpiredTaskTokens :exec
DELETE FROM task_token WHERE expires_at <= now();
