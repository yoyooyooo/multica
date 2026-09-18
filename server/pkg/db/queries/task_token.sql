-- name: CreateTaskToken :one
INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at, id)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()))
RETURNING *;

-- name: GetTaskTokenByHash :one
SELECT * FROM task_token
WHERE token_hash = $1 AND expires_at > now();

-- name: LockRunningTaskTokenForExecutionContext :one
-- Linearization point for execution-context reads versus task terminalization.
SELECT tt.* FROM task_token tt
JOIN agent_task_queue atq ON atq.id = tt.task_id
WHERE tt.token_hash = sqlc.arg('token_hash')
  AND tt.task_id = sqlc.arg('task_id')
  AND tt.workspace_id = sqlc.arg('workspace_id')
  AND tt.expires_at > now()
  AND atq.status = 'running'
FOR UPDATE OF tt, atq;

-- name: DeleteTaskTokensByTask :exec
DELETE FROM task_token WHERE task_id = $1;

-- name: DeleteExpiredTaskTokens :exec
DELETE FROM task_token WHERE expires_at <= now();
