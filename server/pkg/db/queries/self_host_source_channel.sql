-- name: UpsertSelfHostSourceChannel :one
INSERT INTO self_host_source_channel (
    schema_version,
    channel,
    instance_hash,
    subject_hash
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (instance_hash, subject_hash) DO UPDATE SET
    schema_version = EXCLUDED.schema_version,
    channel = EXCLUDED.channel,
    last_received_at = now(),
    report_count = self_host_source_channel.report_count + 1
RETURNING *;
